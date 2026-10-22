//go:build linux

// Package application owns deployment metadata and filesystem preparation for
// the isolated app-value service. It never imports or reads a vault or key.
package application

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/sys/unix"
	"loki/internal/config"
	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/daemon"
	"loki/internal/execution"
	hostpolicy "loki/internal/host/policy"
	"loki/internal/platform/safeio"
)

type Identity struct {
	Version          int    `json:"version"`
	UID              uint32 `json:"uid"`
	GID              uint32 `json:"gid"`
	AgentUID         uint32 `json:"agent_uid"`
	MetadataGID      uint32 `json:"metadata_gid"`
	DeploymentID     string `json:"deployment_id"`
	RunnerID         string `json:"runner_id"`
	ActiveGeneration string `json:"active_generation"`
}

type PrepareOptions struct {
	UID, GID, AgentUID, MetadataGID                                uint32
	IdentityFile, StateRoot, ServiceStateRoot, RuntimeRoot, Binary string
	Principal                                                      string
	JobDeploymentID                                                string
	Configuration                                                  config.Config
	Contract                                                       execution.Contract
	Resources                                                      []controlpolicy.ApplicationResource
}

type Preparation struct {
	Identity        Identity
	Layout          appvalues.BrokerLayout
	HostGeneration  string
	LayoutFile      string
	AdminLayoutFile string
}

var generationID = regexp.MustCompile(`^[0-9a-f]{32}$`)
var deploymentID = regexp.MustCompile(`^deployment-[0-9a-f]{32}$`)
var invalid = errors.New("application service deployment metadata is invalid")

func newID() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	nonce[6] = nonce[6]&15 | 64
	nonce[8] = nonce[8]&63 | 128
	id := hex.EncodeToString(nonce[:])
	return id[:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:], nil
}

// Prepare creates an empty active generation and role metadata only. Crypto
// initialization and unlocking always require a subsequent administrator call.
// Only the returned HostGeneration is mounted into the broker namespace; the
// host selector and other generations remain outside that namespace.
func Prepare(options PrepareOptions) (Preparation, error) {
	return prepare(options, true)
}

// PrepareState prepares only persistent host metadata and an empty generation.
// A networkless administrator preparation container uses it before binding
// precisely that generation into the unprivileged broker namespace.
func PrepareState(options PrepareOptions) (Preparation, error) {
	return prepare(options, false)
}

func prepare(options PrepareOptions, runtimeFiles bool) (Preparation, error) {
	if options.UID == 0 || options.GID == 0 || options.AgentUID == 0 || options.MetadataGID == 0 || options.UID == options.AgentUID || options.GID == options.MetadataGID || !controlpolicy.ApplicationAlias.MatchString(options.Principal) {
		return Preparation{}, invalid
	}
	if options.JobDeploymentID != "" && !deploymentID.MatchString(options.JobDeploymentID) {
		return Preparation{}, invalid
	}
	for _, path := range []string{options.IdentityFile, options.StateRoot, options.ServiceStateRoot, options.RuntimeRoot, options.Binary} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || strings.ContainsAny(path, "\x00\r\n") {
			return Preparation{}, invalid
		}
	}
	resources, err := controlpolicy.CanonicalApplications(options.Resources, options.Contract.Environment)
	if err != nil {
		return Preparation{}, invalid
	}
	policy, err := hostpolicy.CompileApplications(options.Configuration, options.Contract, resources)
	if err != nil {
		return Preparation{}, invalid
	}
	base := make(map[string]string, len(options.Contract.Environment))
	for name, value := range options.Contract.Environment {
		base[name] = value
	}
	layout := appvalues.BrokerLayout{UID: options.UID, LauncherUID: 0, Binary: options.Binary, Root: filepath.Join(options.ServiceStateRoot, "dvt"), ApplicationRoot: filepath.Join(options.ServiceStateRoot, "metadata"), Socket: filepath.Join(options.RuntimeRoot, "jobs", "control.sock"), MetadataSocket: filepath.Join(options.RuntimeRoot, "metadata", "control.sock"), AdminSocket: filepath.Join(options.RuntimeRoot, "admin", "control.sock"), RuntimeRoot: filepath.Join(options.RuntimeRoot, "private"), Inbox: filepath.Join(options.RuntimeRoot, "inbox"), AgentUID: options.AgentUID, MetadataGID: options.MetadataGID, Principal: options.Principal, Resources: resources, BaseEnvironment: base, PolicySHA256: policy.Digest()}
	layout.JobDeploymentID = options.JobDeploymentID
	for _, path := range []string{layout.Socket, layout.MetadataSocket, layout.AdminSocket} {
		if len(path) >= 108 {
			return Preparation{}, invalid
		}
	}
	if err := ownedDirectory(options.StateRoot, 0, 0, 0700, false); err != nil {
		return Preparation{}, err
	}
	parent := filepath.Join(options.StateRoot, "generations")
	if err := ownedDirectory(parent, 0, 0, 0700, false); err != nil {
		return Preparation{}, err
	}
	identity, err := loadIdentity(options)
	if err != nil {
		return Preparation{}, err
	}
	layout.DeploymentID, layout.RunnerID = identity.DeploymentID, identity.RunnerID
	generation := filepath.Join(parent, identity.ActiveGeneration)
	if err := ownedDirectory(generation, options.UID, options.GID, 0700, false); err != nil {
		return Preparation{}, err
	}
	for _, name := range []string{"dvt", "metadata"} {
		if err := ownedDirectory(filepath.Join(generation, name), options.UID, options.GID, 0700, false); err != nil {
			return Preparation{}, err
		}
	}
	if !runtimeFiles {
		return Preparation{Identity: identity, Layout: layout, HostGeneration: generation}, nil
	}
	if err := ownedDirectory(options.RuntimeRoot, 0, 0, 0755, true); err != nil {
		return Preparation{}, err
	}
	for _, name := range []string{"private", "jobs", "admin", "inbox"} {
		if err := ownedDirectory(filepath.Join(options.RuntimeRoot, name), options.UID, options.GID, 0700, true); err != nil {
			return Preparation{}, err
		}
	}
	if err := ownedDirectory(filepath.Join(options.RuntimeRoot, "metadata"), options.UID, options.MetadataGID, 0750, true); err != nil {
		return Preparation{}, err
	}
	layoutFile := filepath.Join(options.RuntimeRoot, "layout.json")
	raw, _ := json.Marshal(layout)
	if err := safeio.PublishPrivate(layoutFile, raw, true); err != nil {
		return Preparation{}, err
	}
	if os.Chown(layoutFile, 0, int(options.GID)) != nil || os.Chmod(layoutFile, 0640) != nil {
		return Preparation{}, invalid
	}
	adminFile := filepath.Join(filepath.Dir(options.IdentityFile), "secrets-admin.json")
	raw, _ = json.Marshal(struct {
		Socket string `json:"socket"`
		UID    uint32 `json:"uid"`
	}{Socket: layout.AdminSocket, UID: options.UID})
	if err := safeio.PublishPrivate(adminFile, raw, true); err != nil {
		return Preparation{}, err
	}
	return Preparation{Identity: identity, Layout: layout, HostGeneration: generation, LayoutFile: layoutFile, AdminLayoutFile: adminFile}, nil
}

func loadIdentity(options PrepareOptions) (Identity, error) {
	var identity Identity
	err := daemon.ReadJSON(options.IdentityFile, &identity)
	if err == nil {
		if identity.Version != 1 || identity.UID != options.UID || identity.GID != options.GID || identity.AgentUID != options.AgentUID || identity.MetadataGID != options.MetadataGID || !deploymentID.MatchString(identity.DeploymentID) || !controlpolicy.ApplicationUUID.MatchString(identity.RunnerID) || !generationID.MatchString(identity.ActiveGeneration) {
			return Identity{}, invalid
		}
		return identity, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Identity{}, invalid
	}
	deployment, err := newID()
	if err != nil {
		return Identity{}, err
	}
	runner, err := newID()
	if err != nil {
		return Identity{}, err
	}
	generation := strings.ReplaceAll(deployment, "-", "")
	identity = Identity{Version: 1, UID: options.UID, GID: options.GID, AgentUID: options.AgentUID, MetadataGID: options.MetadataGID, DeploymentID: "deployment-" + generation, RunnerID: runner, ActiveGeneration: generation}
	raw, _ := json.Marshal(identity)
	if err := safeio.PublishPrivate(options.IdentityFile, raw, false); err != nil {
		return Identity{}, err
	}
	return identity, nil
}

// ownedDirectory never follows symlinks or adopts a directory containing
// foreign state. Empty root-owned preparation remnants can be completed.
func ownedDirectory(path string, uid, gid uint32, mode uint32, tmpfs bool) error {
	parent, err := unix.Openat2(unix.AT_FDCWD, filepath.Dir(path), &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return invalid
	}
	defer unix.Close(parent)
	err = unix.Mkdirat(parent, filepath.Base(path), mode)
	created := err == nil
	if err != nil && !errors.Is(err, unix.EEXIST) {
		return invalid
	}
	fd, err := unix.Openat(parent, filepath.Base(path), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return invalid
	}
	file := os.NewFile(uintptr(fd), "application-owned-directory")
	defer file.Close()
	var st unix.Stat_t
	var fs unix.Statfs_t
	if unix.Fstat(fd, &st) != nil || unix.Fstatfs(fd, &fs) != nil {
		return invalid
	}
	if tmpfs {
		if fs.Type != unix.TMPFS_MAGIC {
			return invalid
		}
	} else {
		switch fs.Type {
		case unix.EXT4_SUPER_MAGIC, unix.BTRFS_SUPER_MAGIC, unix.XFS_SUPER_MAGIC, unix.OVERLAYFS_SUPER_MAGIC, 0x2fc12fc1:
		default:
			return invalid
		}
	}
	if !created && (st.Uid != uid || st.Gid != gid || st.Mode&07777 != mode) {
		if st.Uid != 0 || st.Gid != 0 || st.Mode&07777 != mode && st.Mode&07777 != 0700 && st.Mode&07777 != 0755 {
			return invalid
		}
		entries, readErr := file.ReadDir(1)
		if len(entries) != 0 || !errors.Is(readErr, io.EOF) {
			return invalid
		}
	}
	if unix.Fchown(fd, int(uid), int(gid)) != nil || unix.Fchmod(fd, mode) != nil {
		return invalid
	}
	return nil
}
