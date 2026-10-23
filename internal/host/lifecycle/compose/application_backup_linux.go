//go:build linux

package compose

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"
	"golang.org/x/sys/unix"
	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/host/lifecycle"
	"loki/internal/platform/safeio"
)

func (b *Backend) applicationAdministrator(ctx context.Context, state runtimeState, arguments []string, input io.Reader) ([]byte, error) {
	if len(arguments) < 1 || state.validate() != nil || state.ApplicationRuntimeRoot == "" || state.ApplicationStateSource == "" {
		return nil, lifecycle.ErrApplicationBackup
	}
	switch arguments[0] {
	case "maintenance", "revision", "backup-export", "restore-preview", "restore-import":
	default:
		return nil, lifecycle.ErrApplicationBackup
	}
	private, ok := b.runner.(PrivateRunner)
	if !ok {
		return nil, lifecycle.ErrApplicationBackup
	}
	layout := filepath.Join(filepath.Dir(filepath.Dir(state.ApplicationStateSource)), "secrets-admin.json")
	args := []string{"run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--security-opt", "no-new-privileges:true", "--user", "0:0", "--memory", "134217728", "--pids-limit", "32", "--log-driver", "none", "--entrypoint", "/opt/loki/bin/loki", "--mount", applicationBind(filepath.Join(state.ApplicationRuntimeRoot, "admin"), "/run/loki/secrets/admin", true), "--mount", applicationBind(layout, "/etc/loki/secret-admin.json", true)}
	if input != nil {
		args = append(args, "--interactive")
	}
	args = append(args, state.CoreImage, "secret", "app")
	args = append(args, arguments...)
	args = append(args, "--layout", "/etc/loki/secret-admin.json")
	return private.RunPrivate(ctx, nil, input, args...)
}

func (b *Backend) applicationBackupRecipient() (string, error) {
	var configuration struct {
		Recipient string `json:"recipient"`
	}
	if err := readPrivateJSON(filepath.Join(b.root, "application-backup.json"), &configuration); err != nil {
		return "", lifecycle.ErrApplicationBackup
	}
	r, err := age.ParseX25519Recipient(configuration.Recipient)
	if err != nil || r.String() != configuration.Recipient {
		return "", lifecycle.ErrApplicationBackup
	}
	return r.String(), nil
}

func (b *Backend) publishApplicationBackupRecipient(recipient string, replace bool) error {
	r, err := age.ParseX25519Recipient(recipient)
	if err != nil || r.String() != recipient {
		return lifecycle.ErrApplicationBackup
	}
	path := filepath.Join(b.root, "application-backup.json")
	if !replace {
		if _, err := os.Lstat(path); err == nil {
			_, err = b.applicationBackupRecipient()
			return err
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	body, _ := json.Marshal(struct {
		Recipient string `json:"recipient"`
	}{recipient})
	return safeio.PublishPrivate(path, body, replace)
}

// ConfigureApplicationBackup accepts a public operator encryption recipient.
// It cannot change a pending operation's retained recipient or key session.
func (b *Backend) ConfigureApplicationBackup(ctx context.Context, recipient string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	lock, err := lifecycle.AcquireOperationLock(b.root)
	if err != nil {
		return err
	}
	defer lock.Close()
	j, err := lifecycle.OpenOperationJournal(b.root, lock, lifecycle.OperationJournalOptions{})
	if err != nil {
		return err
	}
	if _, found, err := j.Active(); err != nil || found {
		return lifecycle.ErrApplicationBackup
	}
	return b.publishApplicationBackupRecipient(recipient, true)
}

func (b *Backend) releaseApplicationBackup(ctx context.Context, state runtimeState, operation string) error {
	r, owner, err := lifecycle.CurrentHostOperation(ctx)
	if err != nil || r.ApplicationBackup == nil {
		return nil
	}
	if !owner.HoldsOperationRoot(b.root) {
		return lifecycle.ErrApplicationBackup
	}
	if r.State != lifecycle.OperationSucceeded && r.State != lifecycle.OperationRolledBack || r.ApplicationBackup.Intent.Operation != operation {
		return lifecycle.ErrApplicationBackup
	}
	body, err := b.applicationAdministrator(ctx, state, []string{"maintenance", "end", "--operation-id", operation}, nil)
	var result struct {
		Maintenance struct {
			Version   int    `json:"version"`
			Operation string `json:"operation_id"`
			Active    bool   `json:"active"`
		} `json:"maintenance"`
	}
	if err != nil || json.Unmarshal(body, &result) != nil || result.Maintenance.Version != 1 || result.Maintenance.Operation != operation || result.Maintenance.Active {
		return lifecycle.ErrApplicationBackup
	}
	return nil
}

func (b *Backend) validateApplicationSafetyRestore(ctx context.Context, state runtimeState, backup *lifecycle.ApplicationBackupCheckpoint) error {
	r, owner, err := lifecycle.CurrentHostOperation(ctx)
	if err != nil || !owner.HoldsOperationRoot(b.root) || r.State != lifecycle.OperationRecovering || backup == nil || !backup.Complete() {
		return lifecycle.ErrApplicationBackup
	}
	var catalog struct {
		Resources []controlpolicy.ApplicationResource `json:"resources"`
	}
	if err := readPrivateJSON(filepath.Join(b.root, "application-resources.json"), &catalog); err != nil {
		return err
	}
	actual, err := b.migrationMetadata(ctx, state, catalog.Resources, false)
	if err != nil {
		return err
	}
	// Safety recovery preserves the frozen current data. A requested logical
	// restore is a separate explicit operation with its own fresh generation.
	expected := backup.Intent.Identity
	expected.PolicySHA256 = actual.Identity.PolicySHA256
	if actual.Identity != expected {
		return lifecycle.ErrApplicationBackup
	}
	return nil
}

// Readiness of ordinary app listeners is insufficient for an export: this
// verifies the exact installed image, UID, limits and every finite bind mount.
func (b *Backend) applicationBackupContainer(ctx context.Context, state runtimeState) (string, error) {
	body, err := b.compose(ctx, state, "ps", "--quiet", "application-values")
	if err != nil || len(strings.Fields(string(body))) != 1 {
		return "", lifecycle.ErrApplicationBackup
	}
	id := strings.TrimSpace(string(body))
	body, err = b.runner.Run(ctx, nil, "container", "inspect", id)
	var items []migrationContainer
	if err != nil || json.Unmarshal(body, &items) != nil || len(items) != 1 {
		return "", lifecycle.ErrApplicationBackup
	}
	c := items[0]
	if c.Config.Image != state.CoreImage || c.Config.User != "10006:10006" || !c.State.Running || c.Config.Labels["com.docker.compose.project"] != b.project || c.Config.Labels["com.docker.compose.service"] != "application-values" || c.HostConfig.Privileged || !c.HostConfig.ReadonlyRootfs || c.HostConfig.NetworkMode != "none" || c.HostConfig.Memory != 1<<30 || c.HostConfig.PidsLimit == nil || *c.HostConfig.PidsLimit != 64 || len(c.HostConfig.CapAdd) != 0 || len(c.Mounts) != 7 {
		return "", lifecycle.ErrApplicationBackup
	}
	for _, m := range c.Mounts {
		var source string
		readOnly := false
		switch m.Destination {
		case "/var/lib/loki/secrets":
			source = state.ApplicationStateSource
		case "/run/loki/secrets/layout.json":
			source = filepath.Join(state.ApplicationRuntimeRoot, "layout.json")
			readOnly = true
		case "/run/loki/secrets/private", "/run/loki/secrets/jobs", "/run/loki/secrets/admin", "/run/loki/secrets/metadata", "/run/loki/secrets/inbox":
			source = filepath.Join(state.ApplicationRuntimeRoot, filepath.Base(m.Destination))
		default:
			return "", lifecycle.ErrApplicationBackup
		}
		if m.Type != "bind" || m.Source != source || m.RW == readOnly {
			return "", lifecycle.ErrApplicationBackup
		}
	}
	return id, nil
}

func verifyApplicationCipher(path string, p appvalues.ApplicationExportProof) error {
	fd, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NONBLOCK, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), "logical-application-ciphertext")
	defer file.Close()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&07777 != 0600 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || st.Size != int64(p.Bytes) {
		return lifecycle.ErrApplicationBackup
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, int64(appvalues.MaxApplicationArchive)+1))
	if err != nil || n != int64(p.Bytes) || hex.EncodeToString(hash.Sum(nil)) != p.CipherSHA256 {
		return lifecycle.ErrApplicationBackup
	}
	return nil
}

func (b *Backend) copyApplicationCipher(ctx context.Context, container, destination string, p appvalues.ApplicationExportProof) error {
	if err := verifyApplicationCipher(destination, p); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	exchange, ok := b.runner.(PrivateExchangeRunner)
	if !ok {
		return lifecycle.ErrApplicationBackup
	}
	file, err := os.CreateTemp(filepath.Dir(destination), ".application-cipher-")
	if err != nil {
		return err
	}
	defer file.Close()
	defer os.Remove(file.Name())
	source := "/var/lib/loki/secrets/metadata/exports/" + strings.ReplaceAll(p.Operation, "-", "") + "/" + strings.ReplaceAll(p.Resource, "-", "") + "/application.age"
	err = exchange.ExchangePrivate(ctx, nil, []string{"cp", container + ":" + source, "-"}, func(pipe io.ReadWriteCloser) error {
		reader := tar.NewReader(pipe)
		header, err := reader.Next()
		if err != nil || header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA || header.Size != int64(p.Bytes) || filepath.Base(header.Name) != "application.age" {
			return lifecycle.ErrApplicationBackup
		}
		if _, err := io.CopyN(file, reader, int64(p.Bytes)); err != nil {
			return err
		}
		if _, err := reader.Next(); err != io.EOF {
			return lifecycle.ErrApplicationBackup
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := verifyApplicationCipher(file.Name(), p); err != nil {
		return err
	}
	if err := unix.Renameat2(unix.AT_FDCWD, file.Name(), unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE); err != nil && (err != unix.EEXIST || verifyApplicationCipher(destination, p) != nil) {
		return lifecycle.ErrApplicationBackup
	}
	directory, err := os.Open(filepath.Dir(destination))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (b *Backend) captureApplicationBackup(ctx context.Context, state runtimeState, directory string) (*lifecycle.ApplicationBackupCheckpoint, error) {
	var catalog struct {
		Resources []controlpolicy.ApplicationResource `json:"resources"`
	}
	catalogPath := filepath.Join(b.root, "application-resources.json")
	if _, err := os.Lstat(catalogPath); errors.Is(err, os.ErrNotExist) {
		catalog.Resources = []controlpolicy.ApplicationResource{}
	} else if err != nil {
		return nil, err
	} else if err := readPrivateJSON(catalogPath, &catalog); err != nil {
		return nil, err
	}
	metadata, err := b.migrationMetadata(ctx, state, catalog.Resources, false)
	if err != nil {
		return nil, err
	}
	if metadata.Identity.Version != 2 {
		return nil, nil
	}
	catalog.Resources, err = controlpolicy.CanonicalApplications(catalog.Resources, metadata.BaseEnvironment)
	if err != nil {
		return nil, err
	}
	r, j, err := lifecycle.CurrentHostOperation(ctx)
	if err != nil || !j.HoldsOperationRoot(b.root) || r.Maintenance == nil || b.maintenance == nil || b.maintenance.OperationID != lifecycle.MigrationOperationUUID(r.ID) {
		return nil, lifecycle.ErrApplicationBackup
	}
	recipient, err := b.applicationBackupRecipient()
	if err != nil {
		return nil, err
	}
	intent := lifecycle.ApplicationBackupIntent{Version: 1, Operation: lifecycle.MigrationOperationUUID(r.ID), Identity: metadata.Identity, Policy: metadata.Policy, Resources: catalog.Resources, Recipient: recipient, Directory: directory}
	r, err = j.BeginApplicationBackup(r.ID, intent, time.Now())
	if err != nil {
		return nil, err
	}
	if _, err := b.applicationAdministrator(ctx, state, []string{"maintenance", "begin", "--operation-id", intent.Operation}, nil); err != nil {
		return nil, err
	}
	container, err := b.applicationBackupContainer(ctx, state)
	if err != nil {
		return nil, err
	}
	for _, resource := range intent.Resources {
		body, err := b.applicationAdministrator(ctx, state, []string{"revision", resource.Alias}, nil)
		var revision struct {
			Revision string `json:"revision"`
		}
		if err != nil || json.Unmarshal(body, &revision) != nil || !validSHA256(revision.Revision) {
			return nil, lifecycle.ErrApplicationBackup
		}
		body, err = b.applicationAdministrator(ctx, state, []string{"backup-export", resource.Alias, "--request-id", intent.Operation, "--revision", revision.Revision, "--recipient", recipient}, nil)
		var result struct {
			Export appvalues.ApplicationExportProof `json:"export"`
		}
		if err != nil || json.Unmarshal(body, &result) != nil || result.Export.Resource != resource.ID || result.Export.Revision != revision.Revision {
			return nil, lifecycle.ErrApplicationBackup
		}
		r, err = j.CheckpointApplicationExport(r.ID, result.Export, time.Now())
		if err != nil {
			return nil, err
		}
		if err := b.copyApplicationCipher(ctx, container, filepath.Join(directory, strings.ReplaceAll(resource.ID, "-", "")+".age"), result.Export); err != nil {
			return nil, err
		}
	}
	body, err := lifecycle.MarshalApplicationBackup(*r.ApplicationBackup)
	if err != nil {
		return nil, err
	}
	if err := safeio.PublishPrivate(filepath.Join(directory, "application-backup.json"), body, false); err != nil {
		return nil, err
	}
	return r.ApplicationBackup, nil
}
