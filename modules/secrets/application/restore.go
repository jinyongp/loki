//go:build linux

package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
)

// importApplication is unavailable in ordinary serving layouts. The original
// host candidate alone may import an app-only logical archive. Upstream owns
// archive decoding, key re-encryption and idempotency; no task root, session,
// execution, approval or pattern is copied from a historical provider root.
func (s *Service) importApplication(ctx context.Context, q AdminRequest, body []byte) (map[string]any, error) {
	previewOnly := q.Action == "backup_import_preview"
	if s.options.RestoreOperation == "" || q.Operation != s.options.RestoreOperation || !adminUUID.MatchString(q.RequestID) || !revisionPattern.MatchString(q.ArchiveSHA256) || previewOnly && q.Revision != "" || !previewOnly && !revisionPattern.MatchString(q.Revision) || q.Environment != "" || q.Principal != "" || q.Name != "" || q.Variables != nil || q.Members != nil || q.Empty || q.Replace || q.Recipient != "" {
		return nil, invalid()
	}
	identity, archive, err := appvalues.DecodeApplicationRestore(body)
	if err != nil {
		return nil, invalid()
	}
	sum := sha256.Sum256(archive)
	if hex.EncodeToString(sum[:]) != q.ArchiveSHA256 {
		return nil, conflict()
	}
	var profile string
	var resource controlpolicy.ApplicationResource
	for _, r := range s.options.Resources {
		if r.Alias == q.Resource {
			profile, resource = r.Profile(), r
			break
		}
	}
	if profile == "" {
		return nil, denied()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	gate, err := s.maintenanceState()
	if err != nil || gate == nil || !gate.Active || gate.Operation != q.Operation {
		return nil, conflict()
	}
	// Identity and ciphertext exist only under the candidate's private tmpfs.
	// Startup removes orphan staging before admitting a fresh candidate.
	// The operator must supply private input again after process loss.
	directory, err := os.MkdirTemp(s.options.Inbox, ".restore-"+strings.ReplaceAll(q.Operation, "-", "")+"-")
	if err != nil {
		return nil, unavailable()
	}
	defer os.RemoveAll(directory)
	identityPath, archivePath := filepath.Join(directory, "identity"), filepath.Join(directory, "application.age")
	if os.WriteFile(identityPath, identity, 0600) != nil || os.WriteFile(archivePath, archive, 0600) != nil {
		return nil, unavailable()
	}
	preview, err := s.options.Provider.PreviewApplicationImport(ctx, profile, archivePath, identityPath)
	if err != nil {
		return nil, safeError(err)
	}
	if preview.SourceProfile != profile {
		return nil, denied()
	}
	if previewOnly {
		return map[string]any{"action": q.Action, "resource_id": resource.ID, "digest": preview.Digest, "target_exists": preview.TargetExists}, nil
	}
	result, err := s.options.Provider.ImportApplication(ctx, profile, archivePath, identityPath, q.Revision, q.RequestID)
	if err != nil {
		return nil, safeError(err)
	}
	if err := s.record("host-admin", resource, "backup_import", q.RequestID, "success"); err != nil {
		return nil, err
	}
	return map[string]any{"action": q.Action, "resource_id": resource.ID, "digest": result.Digest, "changed": result.Changed, "replayed": result.Replayed}, nil
}

func cleanupRestoreStaging(options Options) error {
	if options.RestoreOperation == "" {
		return nil
	}
	entries, err := os.ReadDir(options.Inbox)
	if err != nil {
		return err
	}
	prefix := ".restore-" + strings.ReplaceAll(options.RestoreOperation, "-", "") + "-"
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".restore-") {
			continue
		}
		directory := filepath.Join(options.Inbox, entry.Name())
		if !strings.HasPrefix(entry.Name(), prefix) || privateDirectory(directory, true) != nil {
			return unavailable()
		}
		files, err := os.ReadDir(directory)
		if err != nil || len(files) > 2 {
			return unavailable()
		}
		for _, file := range files {
			if file.Name() != "identity" && file.Name() != "application.age" {
				return unavailable()
			}
			path := filepath.Join(directory, file.Name())
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
				return unavailable()
			}
			owner, ok := info.Sys().(*syscall.Stat_t)
			if !ok || owner.Uid != uint32(os.Geteuid()) || owner.Nlink != 1 {
				return unavailable()
			}
			if err := os.Remove(path); err != nil {
				return err
			}
		}
		if err := os.Remove(directory); err != nil {
			return err
		}
	}
	return nil
}
