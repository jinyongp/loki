//go:build linux

package compose

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"loki/internal/control/appvalues"
	"loki/internal/host/lifecycle"
)

func (b *Backend) applicationRestoreAdministrator(ctx context.Context, arguments []string, input []byte) ([]byte, error) {
	r, _, err := b.applicationRestoreRecord(ctx)
	if err != nil || r.State != lifecycle.OperationApplying || r.Phase != lifecycle.PhaseMigrate || r.ApplicationRestore.RollbackRequested || len(arguments) < 2 {
		return nil, lifecycle.ErrApplicationBackup
	}
	if arguments[0] == "protection" {
		if arguments[1] != "status" && arguments[1] != "init" && arguments[1] != "unlock" {
			return nil, lifecycle.ErrApplicationBackup
		}
	} else if arguments[0] != "app" || arguments[1] != "restore-preview" && arguments[1] != "restore-import" && arguments[1] != "restore-seal" {
		return nil, lifecycle.ErrApplicationBackup
	}
	private, ok := b.runner.(PrivateRunner)
	if !ok {
		return nil, lifecycle.ErrApplicationBackup
	}
	binding := applicationRestoreBinding(r)
	projection := filepath.Join("/dev/shm", "loki-tools-"+applicationOwnerID(b.root)+"-restore-"+r.ID, "secrets")
	generation, err := b.applicationGenerationSource(ctx, binding.generation)
	if err != nil {
		return nil, err
	}
	candidate, found, err := b.inspectApplicationCandidate(ctx, binding, projection, generation)
	if err != nil || !found || !candidate.State.Running {
		return nil, lifecycle.ErrApplicationBackup
	}
	memory := "134217728"
	if arguments[0] == "app" && arguments[1] != "restore-seal" {
		memory = "536870912"
	}
	args := []string{"run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--security-opt", "no-new-privileges:true", "--user", "0:0", "--memory", memory, "--pids-limit", "32", "--log-driver", "none", "--entrypoint", "/opt/loki/bin/loki", "--mount", applicationBind(filepath.Join(projection, "admin"), "/run/loki/secrets/admin", true), "--mount", applicationBind(filepath.Join(projection, "admin-layout.json"), "/etc/loki/secret-admin.json", true)}
	args = append(args, b.applicationCandidateLabels(binding, "admin")...)
	if len(input) > 0 {
		args = append(args, "--interactive")
	}
	args = append(args, binding.image, "secret")
	args = append(args, arguments...)
	args = append(args, "--layout", "/etc/loki/secret-admin.json")
	var source io.Reader
	if len(input) > 0 {
		source = bytes.NewReader(input)
	}
	return private.RunPrivate(ctx, nil, source, args...)
}

func (b *Backend) applicationRestoreProtection(ctx context.Context, action string, input []byte) (string, error) {
	if action != "status" && action != "init" && action != "unlock" || (action != "status") != (len(input) > 0) || len(input) > 65536 {
		return "", lifecycle.ErrApplicationBackup
	}
	args := []string{"protection", action}
	if len(input) > 0 {
		args = append(args, "--stdin")
	}
	limit := 30 * time.Second
	if action == "status" {
		limit = 15 * time.Second
	}
	ready, stop := context.WithTimeout(ctx, limit)
	defer stop()
	for {
		body, err := b.applicationRestoreAdministrator(ready, args, input)
		defer clear(body)
		if err == nil {
			var result struct {
				Action string `json:"action"`
				State  string `json:"state"`
			}
			if json.Unmarshal(body, &result) != nil || result.Action != action {
				return "", lifecycle.ErrApplicationBackup
			}
			switch result.State {
			case "uninitialized", "locked", "locked_manual", "unlocked", "locking", "recovery_required":
				return result.State, nil
			default:
				return "", lifecycle.ErrApplicationBackup
			}
		}
		if action != "status" {
			return "", err
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ready.Done():
			timer.Stop()
			return "", lifecycle.ErrApplicationBackup
		case <-timer.C:
		}
	}
}

func (b *Backend) importApplicationRestore(ctx context.Context, identity []byte) error {
	r, j, err := b.applicationRestoreRecord(ctx)
	if err != nil || len(r.ApplicationRestore.Imports) > 0 && len(identity) < 1 || len(identity) > 65536 {
		return lifecycle.ErrApplicationBackup
	}
	i := r.ApplicationRestore.Intent
	if _, _, err := b.applicationRestoreBackup(ctx, i); err != nil {
		return err
	}
	for _, slot := range r.ApplicationRestore.Imports {
		var resource string
		for _, entry := range i.Backup.Intent.Resources {
			if entry.ID == slot.Resource {
				resource = entry.Alias
				break
			}
		}
		if resource == "" {
			return lifecycle.ErrApplicationBackup
		}
		ciphertext, err := readApplicationRestoreCipher(filepath.Join(i.Backup.Intent.Directory, restoreCipherName(slot.Resource)), slot.CipherSHA256)
		if err != nil {
			return err
		}
		body, err := appvalues.EncodeApplicationRestore(identity, ciphertext)
		clear(ciphertext)
		if err != nil {
			return err
		}
		args := []string{"app", "restore-preview", resource, "--operation-id", lifecycle.MigrationOperationUUID(r.ID), "--archive-sha256", slot.CipherSHA256, "--request-id", slot.Request, "--stdin"}
		if slot.Digest == "" {
			response, err := b.applicationRestoreAdministrator(ctx, args, body)
			var preview struct {
				Resource string `json:"resource_id"`
				Digest   string `json:"digest"`
				Exists   bool   `json:"target_exists"`
			}
			if err != nil || json.Unmarshal(response, &preview) != nil || preview.Resource != slot.Resource || preview.Exists || !validSHA256(preview.Digest) {
				clear(body)
				return lifecycle.ErrApplicationBackup
			}
			slot.Digest = preview.Digest
			if _, err := j.CheckpointApplicationRestoreImport(r.ID, slot.Resource, slot.Digest, false, time.Now()); err != nil {
				clear(body)
				return err
			}
		}
		args[1] = "restore-import"
		args = append(args, "--revision", slot.Digest)
		response, err := b.applicationRestoreAdministrator(ctx, args, body)
		clear(body)
		var applied struct {
			Resource string `json:"resource_id"`
			Digest   string `json:"digest"`
			Changed  bool   `json:"changed"`
			Replayed bool   `json:"replayed"`
		}
		if err != nil || json.Unmarshal(response, &applied) != nil || applied.Resource != slot.Resource || applied.Digest != slot.Digest || !applied.Changed && !applied.Replayed {
			return lifecycle.ErrApplicationBackup
		}
		if _, err := j.CheckpointApplicationRestoreImport(r.ID, slot.Resource, slot.Digest, true, time.Now()); err != nil {
			return err
		}
	}
	response, err := b.applicationRestoreAdministrator(ctx, []string{"app", "restore-seal", "--operation-id", lifecycle.MigrationOperationUUID(r.ID), "--policy", i.Policy}, nil)
	var seal struct {
		Operation string `json:"operation_id"`
		Policy    string `json:"policy"`
		Sealed    bool   `json:"sealed"`
		Resources int    `json:"resources"`
	}
	if err != nil || json.Unmarshal(response, &seal) != nil || seal.Operation != lifecycle.MigrationOperationUUID(r.ID) || seal.Policy != i.Policy || !seal.Sealed || seal.Resources != len(i.Backup.Intent.Resources) {
		return lifecycle.ErrApplicationBackup
	}
	_, err = j.SealApplicationRestore(r.ID, time.Now())
	return err
}

func readApplicationRestoreCipher(path, digest string) ([]byte, error) {
	fd, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NONBLOCK, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "logical-restore-ciphertext")
	defer f.Close()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&07777 != 0600 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || st.Size < 1 || st.Size > appvalues.MaxApplicationArchive {
		return nil, lifecycle.ErrApplicationBackup
	}
	body, err := io.ReadAll(io.LimitReader(f, appvalues.MaxApplicationArchive+1))
	sum := sha256.Sum256(body)
	if err != nil || int64(len(body)) != st.Size || hex.EncodeToString(sum[:]) != digest {
		clear(body)
		return nil, lifecycle.ErrApplicationBackup
	}
	return body, nil
}

func (b *Backend) cleanupApplicationRestoreAdministrators(ctx context.Context) error {
	r, _, err := b.applicationRestoreRecord(ctx)
	if err != nil {
		return err
	}
	binding := applicationRestoreBinding(r)
	ids, err := b.runner.Run(ctx, nil, "container", "ls", "--all", "--filter", "label=io.loki.restore.owner="+applicationOwnerID(b.root), "--filter", "label=io.loki.restore.operation="+r.ID, "--filter", "label=io.loki.restore.purpose=admin", "--format", "{{.ID}}")
	if err != nil {
		return err
	}
	projection := filepath.Join("/dev/shm", "loki-tools-"+applicationOwnerID(b.root)+"-restore-"+r.ID, "secrets")
	for _, id := range strings.Fields(string(ids)) {
		body, err := b.runner.Run(ctx, nil, "container", "inspect", id)
		var items []migrationContainer
		if err != nil || json.Unmarshal(body, &items) != nil || len(items) != 1 {
			return lifecycle.ErrApplicationBackup
		}
		c := items[0]
		labels := c.Config.Labels
		if c.Config.Image != binding.image || c.Config.User != "0:0" || labels["io.loki.restore.owner"] != applicationOwnerID(b.root) || labels["io.loki.restore.operation"] != r.ID || labels["io.loki.restore.generation"] != binding.generation || labels["io.loki.restore.purpose"] != "admin" || c.HostConfig.NetworkMode != "none" || c.HostConfig.Privileged || !c.HostConfig.ReadonlyRootfs || c.HostConfig.LogConfig.Type != "none" || len(c.Config.Entrypoint) != 1 || c.Config.Entrypoint[0] != "/opt/loki/bin/loki" || len(c.Config.Cmd) < 3 || c.Config.Cmd[0] != "secret" || c.Config.Cmd[1] != "protection" && c.Config.Cmd[1] != "app" || len(c.Mounts) != 2 {
			return lifecycle.ErrApplicationBackup
		}
		want := map[string]string{"/run/loki/secrets/admin": filepath.Join(projection, "admin"), "/etc/loki/secret-admin.json": filepath.Join(projection, "admin-layout.json")}
		for _, m := range c.Mounts {
			if source, ok := want[m.Destination]; !ok || m.Type != "bind" || m.Source != source || m.RW {
				return lifecycle.ErrApplicationBackup
			}
			delete(want, m.Destination)
		}
		if _, err := b.runner.Run(ctx, nil, "container", "rm", "--force", c.ID); err != nil {
			return err
		}
	}
	return nil
}
