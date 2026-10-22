//go:build linux

package migration

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"loki/internal/admin"
	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	hostapplication "loki/internal/host/application"
	"loki/internal/host/lifecycle"
)

// NativeCandidate owns one host-operation-bound transient systemd service.
// Its empty root exposes only the reviewed executables, candidate generation
// and private tmpfs role projection. The active generation is never mounted.
type NativeCandidate struct {
	Operation                string
	Preparation              hostapplication.Preparation
	RootDirectory            string
	BrokerHost, ProviderHost string
}

var errNativeCandidate = errors.New("owned native migration broker is unavailable; resume its original host operation")

func (c NativeCandidate) unit() string {
	return "loki-go-secret-migration-" + c.Preparation.Identity.ActiveGeneration + ".service"
}
func (c NativeCandidate) description() string {
	return "LOKI secret migration " + c.Operation + " " + c.Preparation.Identity.ActiveGeneration
}

func (c NativeCandidate) valid() bool {
	p := c.Preparation
	if os.Geteuid() != 0 || !controlpolicy.ApplicationUUID.MatchString(c.Operation) || p.Identity.Version != 2 || p.Identity.UID == 0 || p.Identity.GID == 0 || p.Layout.MigrationOperation != c.Operation || p.Layout.UID != p.Identity.UID || p.Layout.LauncherUID != 0 || !validDigest(p.Identity.PolicySHA256) || p.Identity.PolicySHA256 != p.Layout.PolicySHA256 || lifecycle.MigrationOperationUUID(p.Identity.ActiveGeneration) == "" || filepath.Base(p.HostGeneration) != p.Identity.ActiveGeneration {
		return false
	}
	runtime := filepath.Dir(p.LayoutFile)
	if p.LayoutFile != filepath.Join(runtime, "layout.json") || p.AdminLayoutFile != filepath.Join(runtime, "admin-layout.json") || p.Layout.Socket != filepath.Join(runtime, "jobs", "control.sock") || p.Layout.AdminSocket != filepath.Join(runtime, "admin", "control.sock") || p.Layout.MetadataSocket != filepath.Join(runtime, "metadata", "control.sock") || p.Layout.RuntimeRoot != filepath.Join(runtime, "private") || p.Layout.Inbox != filepath.Join(runtime, "inbox") || p.Layout.ApplicationRoot != filepath.Join(filepath.Dir(p.Layout.Root), "metadata") || filepath.Base(p.Layout.Root) != "dvt" {
		return false
	}
	for _, path := range []string{c.RootDirectory, c.BrokerHost, c.ProviderHost, p.HostGeneration, p.LayoutFile, p.Layout.Root, p.Layout.Binary, filepath.Dir(p.LayoutFile)} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || strings.ContainsAny(path, "\x00\r\n:%") {
			return false
		}
	}
	info, err := os.Lstat(c.RootDirectory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 0 {
		return false
	}
	for _, path := range []string{c.BrokerHost, c.ProviderHost} {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Mode().Perm()&0111 == 0 {
			return false
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != 0 {
			return false
		}
	}
	return true
}

func nativeCommand(ctx context.Context, binary string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent"}
	var output bytes.Buffer
	command.Stdout = &output
	// These commands receive deployment metadata only. Do not expose arbitrary
	// unit logs or child/provider diagnostics through this control boundary.
	if command.Run() != nil || output.Len() > 128<<10 {
		return nil, errNativeCandidate
	}
	return output.Bytes(), nil
}

func (c NativeCandidate) properties(ctx context.Context) (map[string]string, error) {
	body, err := nativeCommand(ctx, "/usr/bin/systemctl", "show", c.unit(), "--no-pager", "--property=LoadState,ActiveState,Description,User,Group,RootDirectory,MemoryMax,TasksMax,Transient")
	if err != nil {
		return nil, err
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(body), "\n") {
		if name, value, ok := strings.Cut(line, "="); ok {
			values[name] = value
		}
	}
	return values, nil
}

func (c NativeCandidate) owned(properties map[string]string) bool {
	return properties["Description"] == c.description() && properties["User"] == strconv.FormatUint(uint64(c.Preparation.Identity.UID), 10) && properties["Group"] == strconv.FormatUint(uint64(c.Preparation.Identity.GID), 10) && properties["RootDirectory"] == c.RootDirectory && properties["MemoryMax"] == "1073741824" && properties["TasksMax"] == "64" && properties["Transient"] == "yes"
}

func (c NativeCandidate) Client() admin.ApplicationClient {
	return admin.ApplicationClient{Socket: c.Preparation.Layout.AdminSocket, ExpectedUID: c.Preparation.Identity.UID}
}

func (c NativeCandidate) Status(ctx context.Context) (string, error) {
	if !c.valid() {
		return "", errNativeCandidate
	}
	properties, err := c.properties(ctx)
	if err != nil || !c.owned(properties) || properties["ActiveState"] != "active" {
		return "", errNativeCandidate
	}
	body, err := c.Client().Call(ctx, appvalues.AdminRequest{Action: "protection_status"}, nil)
	if err != nil {
		return "", err
	}
	return protectionState(body)
}

// Start never initializes/unlocks protection. Existing units are accepted only
// when their immutable operation, namespace, identity and resource bounds match.
func (c NativeCandidate) Start(ctx context.Context) error {
	if !c.valid() {
		return errNativeCandidate
	}
	properties, err := c.properties(ctx)
	if err != nil {
		return err
	}
	if properties["LoadState"] != "not-found" {
		if !c.owned(properties) || properties["ActiveState"] != "active" {
			return errNativeCandidate
		}
		_, err := c.Status(ctx)
		return err
	}
	p := c.Preparation
	bind := func(source, target string) string { return strconv.Quote(source) + ":" + strconv.Quote(target) }
	args := []string{"--quiet", "--collect", "--unit=" + c.unit(), "--description=" + c.description(), "--service-type=notify",
		"--property=NotifyAccess=main", "--property=User=" + strconv.FormatUint(uint64(p.Identity.UID), 10), "--property=Group=" + strconv.FormatUint(uint64(p.Identity.GID), 10), "--property=SupplementaryGroups=" + strconv.FormatUint(uint64(p.Identity.MetadataGID), 10),
		"--property=RootDirectory=" + c.RootDirectory, "--property=MountAPIVFS=yes", "--property=BindReadOnlyPaths=" + bind(c.BrokerHost, "/opt/loki/bin/loki-secret-broker") + " " + bind(c.ProviderHost, p.Layout.Binary) + " " + bind(p.LayoutFile, p.LayoutFile),
		"--property=BindPaths=" + bind(p.HostGeneration, filepath.Dir(p.Layout.Root)) + " " + bind(filepath.Dir(p.LayoutFile), filepath.Dir(p.LayoutFile)),
		"--property=ReadWritePaths=" + strconv.Quote(filepath.Dir(p.Layout.Root)) + " " + strconv.Quote(filepath.Dir(p.LayoutFile)),
		"--property=UMask=0077", "--property=NoNewPrivileges=yes", "--property=PrivateDevices=yes", "--property=PrivateTmp=yes", "--property=PrivateNetwork=yes", "--property=ProtectSystem=strict", "--property=ProtectHome=yes", "--property=ProtectClock=yes", "--property=ProtectControlGroups=yes", "--property=ProtectKernelLogs=yes", "--property=ProtectKernelModules=yes", "--property=ProtectKernelTunables=yes", "--property=RestrictAddressFamilies=AF_UNIX", "--property=IPAddressDeny=any", "--property=LockPersonality=yes", "--property=CapabilityBoundingSet=", "--property=AmbientCapabilities=", "--property=RestrictRealtime=yes", "--property=TasksMax=64", "--property=MemoryMax=1G", "--property=KillMode=control-group", "--property=TimeoutStartSec=30", "--property=TimeoutStopSec=15",
		"/opt/loki/bin/loki-secret-broker", "--layout", p.LayoutFile}
	if _, err := nativeCommand(ctx, "/usr/bin/systemd-run", args...); err != nil {
		return err
	}
	_, err = c.Status(ctx)
	return err
}

// Stop signals only the verified original unit. An absent collected unit is an
// idempotent stop; a foreign unit at the same name is preserved and refused.
func (c NativeCandidate) Stop(ctx context.Context) error {
	if !c.valid() {
		return errNativeCandidate
	}
	properties, err := c.properties(ctx)
	if err != nil {
		return err
	}
	if properties["LoadState"] == "not-found" {
		return nil
	}
	if !c.owned(properties) {
		return errNativeCandidate
	}
	if _, err := nativeCommand(ctx, "/usr/bin/systemctl", "stop", c.unit()); err != nil {
		return err
	}
	properties, err = c.properties(ctx)
	if err != nil {
		return err
	}
	if properties["LoadState"] == "not-found" {
		return nil
	}
	if !c.owned(properties) || properties["ActiveState"] != "inactive" {
		return errNativeCandidate
	}
	return nil
}
