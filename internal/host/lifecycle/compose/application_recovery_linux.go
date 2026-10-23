//go:build linux

package compose

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"golang.org/x/sys/unix"
	"loki/internal/control/appvalues"
	"loki/internal/host/lifecycle"
)

// This is an immutable encrypted input artifact, not a recovery journal. Only
// the new host's original locked operation can import or select its contents.
// Historical volumes, task/session roots, authentication and journals are absent.
type newDeploymentApplicationArchive struct {
	Version     int                    `json:"version"`
	Source      lifecycle.BackupRecord `json:"source_backup"`
	SourceImage string                 `json:"source_image"`
	Runtime     runtimeState           `json:"new_runtime"`
	directory   string
}

func (a newDeploymentApplicationArchive) ID() string {
	body, _ := json.Marshal(a)
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (b *Backend) recoveryArchiveDirectory(sourceID string) (string, error) {
	if !strings.HasPrefix(sourceID, "sha256:") || !validSHA256(strings.TrimPrefix(sourceID, "sha256:")) {
		return "", lifecycle.ErrApplicationBackup
	}
	return filepath.Join(b.root, "application-recovery", strings.TrimPrefix(sourceID, "sha256:")), nil
}

func privateRecoveryDirectory(path string) error {
	fd, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return lifecycle.ErrApplicationBackup
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&07777 != 0700 || st.Uid != uint32(os.Geteuid()) {
		return lifecycle.ErrApplicationBackup
	}
	return nil
}

func readRecoveryMetadata(path string, target any) error {
	fd, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return lifecycle.ErrApplicationBackup
	}
	file := os.NewFile(uintptr(fd), "public-recovery-metadata")
	defer file.Close()
	var st unix.Stat_t
	const bound = 2 << 20
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&07777 != 0600 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || st.Size < 1 || st.Size > bound {
		return lifecycle.ErrApplicationBackup
	}
	body, err := io.ReadAll(io.LimitReader(file, bound+1))
	if err != nil || len(body) > bound {
		return lifecycle.ErrApplicationBackup
	}
	// Host metadata contains application key maps and JSON schemas whose
	// names legitimately contain uppercase letters. The private wire's
	// lowercase-only field grammar is not this document's format contract.
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		return lifecycle.ErrApplicationBackup
	}
	return nil
}

func (b *Backend) newDeploymentRestoreArchive(ctx context.Context, i lifecycle.ApplicationRestoreIntent) (lifecycle.BackupRecord, runtimeState, error) {
	var a newDeploymentApplicationArchive
	fail := func() (lifecycle.BackupRecord, runtimeState, error) {
		return lifecycle.BackupRecord{}, runtimeState{}, lifecycle.ErrApplicationBackup
	}
	if ctx.Err() != nil || !i.NewDeployment || privateRecoveryDirectory(i.RuntimeRef) != nil || readRecoveryMetadata(filepath.Join(i.RuntimeRef, "archive.json"), &a) != nil || a.Version != 1 || !a.Source.Valid() || a.Source.Installed == nil || a.Source.ApplicationBackup == nil || !a.Source.ApplicationBackup.Complete() || a.ID() != i.BackupID || a.Runtime.validate() != nil || a.Runtime.CoreImage != a.SourceImage || a.SourceImage != i.TargetImage || a.Runtime.GenerationID != i.TargetProgram || a.Source.Installed.ID != i.TargetProgram {
		return fail()
	}
	directory, err := b.recoveryArchiveDirectory(a.Source.ID)
	if err != nil || directory != i.RuntimeRef {
		return fail()
	}
	checkpoint := *a.Source.ApplicationBackup
	checkpoint.Intent.Directory = filepath.Join(directory, "applications")
	if !reflect.DeepEqual(checkpoint, i.Backup) {
		return fail()
	}
	for _, proof := range checkpoint.Exports {
		if verifyApplicationCipher(filepath.Join(checkpoint.Intent.Directory, restoreCipherName(proof.Resource)), proof) != nil {
			return fail()
		}
	}
	return a.Source, a.Runtime, nil
}

// PrepareNewDeploymentRestore requires a separately installed, empty owner
// with a different deployment/runner and exactly the archived program already
// selected. It copies encrypted app artifacts only; the ordinary restore driver
// then uses this host's fresh authentication and request namespace.
func (b *Backend) PrepareNewDeploymentRestore(ctx context.Context, sourceRoot, backupID string, interrupt bool) (lifecycle.ApplicationRestoreIntent, error) {
	a, err := b.importNewDeploymentArchive(ctx, sourceRoot, backupID)
	if err != nil {
		return lifecycle.ApplicationRestoreIntent{}, fmt.Errorf("import encrypted recovery artifact: %w", err)
	}
	intent, err := b.prepareApplicationRestore(ctx, a.ID(), interrupt, &a)
	if err != nil {
		return intent, fmt.Errorf("bind fresh deployment recovery: %w", err)
	}
	return intent, nil
}

func (b *Backend) importNewDeploymentArchive(ctx context.Context, sourceRoot, backupID string) (newDeploymentApplicationArchive, error) {
	var a newDeploymentApplicationArchive
	if sourceRoot == b.root || !filepath.IsAbs(sourceRoot) || filepath.Clean(sourceRoot) != sourceRoot || privateRecoveryDirectory(sourceRoot) != nil {
		return a, lifecycle.ErrApplicationBackup
	}
	lock, err := lifecycle.AcquireOperationLock(b.root)
	if err != nil {
		return a, err
	}
	defer lock.Close()
	j, err := lifecycle.OpenOperationJournal(b.root, lock, lifecycle.OperationJournalOptions{})
	if err != nil {
		return a, err
	}
	if _, found, err := j.Active(); err != nil || found {
		return a, lifecycle.ErrApplicationBackup
	}
	source, err := lifecycle.OpenFileStore(sourceRoot)
	if err != nil {
		return a, err
	}
	backup, err := source.LoadBackup(ctx, backupID)
	if err != nil || backup.Installed == nil || backup.ApplicationBackup == nil || !backup.ApplicationBackup.Complete() {
		return a, lifecycle.ErrApplicationBackup
	}
	sourceBackend, err := Open(Config{StateRoot: sourceRoot, Runner: b.runner})
	if err != nil {
		return a, err
	}
	snapshot, err := sourceBackend.snapshotDirectory(backup.RuntimeRef)
	var manifest snapshotManifest
	if err != nil || privateRecoveryDirectory(snapshot) != nil || readRecoveryMetadata(filepath.Join(snapshot, "manifest.json"), &manifest) != nil || manifest.Version != snapshotManifestVersion || manifest.Runtime == nil || manifest.Runtime.validate() != nil || manifest.Runtime.GenerationID != backup.Installed.ID || manifest.ApplicationBackup == nil || !reflect.DeepEqual(*manifest.ApplicationBackup, *backup.ApplicationBackup) || backup.ApplicationBackup.Intent.Directory != filepath.Join(snapshot, "applications") {
		return a, lifecycle.ErrApplicationBackup
	}
	store, err := lifecycle.OpenFileStore(b.root)
	if err != nil {
		return a, err
	}
	installed, err := store.Snapshot(ctx)
	if err != nil || installed.Installed == nil || installed.Installed.ID != backup.Installed.ID {
		return a, lifecycle.ErrApplicationBackup
	}
	current, found, err := b.loadRuntime()
	if err != nil || !found || current.CoreImage != manifest.Runtime.CoreImage || current.GenerationID != backup.Installed.ID {
		return a, lifecycle.ErrApplicationBackup
	}
	_, imageDigest, pinned := strings.Cut(current.CoreImage, "@")
	if !pinned || imageDigest != installed.Installed.Spec.CoreImageDigest {
		return a, lifecycle.ErrApplicationBackup
	}
	resources, err := b.currentApplicationCatalog()
	if err != nil || len(resources) != 0 {
		return a, lifecycle.ErrApplicationBackup
	}
	before, err := b.migrationMetadata(ctx, current, resources, true)
	old := backup.ApplicationBackup.Intent.Identity
	if err != nil || before.Identity.Version != 1 && before.Identity.Version != 2 || before.Identity.DeploymentID == old.DeploymentID || before.Identity.RunnerID == old.RunnerID {
		return a, lifecycle.ErrApplicationBackup
	}
	directory, err := b.recoveryArchiveDirectory(backup.ID)
	if err != nil {
		return a, err
	}
	a = newDeploymentApplicationArchive{Version: 1, Source: backup, SourceImage: manifest.Runtime.CoreImage, Runtime: current, directory: directory}
	parent := filepath.Dir(directory)
	if err := os.Mkdir(parent, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return a, err
	}
	if privateRecoveryDirectory(parent) != nil {
		return a, lifecycle.ErrApplicationBackup
	}
	if _, err := os.Lstat(directory); err == nil {
		var previous newDeploymentApplicationArchive
		if privateRecoveryDirectory(directory) != nil || readRecoveryMetadata(filepath.Join(directory, "archive.json"), &previous) != nil || previous.ID() != a.ID() {
			return a, lifecycle.ErrApplicationBackup
		}
		if err := b.publishApplicationBackupRecipient(backup.ApplicationBackup.Intent.Recipient, false); err != nil {
			return a, err
		}
		return a, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return a, err
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		return a, err
	}
	prefix := ".import-" + strings.TrimPrefix(backup.ID, "sha256:") + "-"
	var retainedBytes int64
	count := 0
	for _, entry := range entries {
		path := filepath.Join(parent, entry.Name())
		if strings.HasPrefix(entry.Name(), prefix) {
			if err := removeRecoveryStage(path, backup.ApplicationBackup.Exports); err != nil {
				return a, err
			}
			continue
		}
		count++
		var previous newDeploymentApplicationArchive
		if !validSHA256(entry.Name()) || privateRecoveryDirectory(path) != nil || readRecoveryMetadata(filepath.Join(path, "archive.json"), &previous) != nil || !previous.Source.Valid() || previous.Source.ApplicationBackup == nil {
			return a, lifecycle.ErrApplicationBackup
		}
		for _, proof := range previous.Source.ApplicationBackup.Exports {
			retainedBytes += int64(proof.Bytes)
		}
	}
	if count >= lifecycle.DefaultLifecycleStorageLimits().MaxBackups {
		return a, lifecycle.ErrLifecycleStorageQuotaExceeded
	}
	for _, proof := range backup.ApplicationBackup.Exports {
		retainedBytes += int64(proof.Bytes)
	}
	if retainedBytes > lifecycle.DefaultLifecycleStorageLimits().MaxBytes {
		return a, lifecycle.ErrLifecycleStorageQuotaExceeded
	}
	stage, err := os.MkdirTemp(parent, prefix)
	if err != nil {
		return a, err
	}
	defer os.RemoveAll(stage)
	apps := filepath.Join(stage, "applications")
	if err := os.Mkdir(apps, 0700); err != nil {
		return a, err
	}
	var total int64
	for _, proof := range backup.ApplicationBackup.Exports {
		total += int64(proof.Bytes)
		if total > lifecycle.DefaultLifecycleStorageLimits().MaxBytes {
			return a, lifecycle.ErrLifecycleStorageQuotaExceeded
		}
		name := restoreCipherName(proof.Resource)
		if err := copyRecoveryCipher(ctx, filepath.Join(backup.ApplicationBackup.Intent.Directory, name), filepath.Join(apps, name), proof); err != nil {
			return a, err
		}
	}
	if err := writePrivateJSON(filepath.Join(stage, "archive.json"), a); err != nil {
		return a, err
	}
	for _, path := range []string{apps, stage} {
		dir, err := os.Open(path)
		if err != nil {
			return a, err
		}
		err = dir.Sync()
		dir.Close()
		if err != nil {
			return a, err
		}
	}
	if err := os.Rename(stage, directory); err != nil {
		return a, err
	}
	dir, err := os.Open(parent)
	if err != nil {
		return a, err
	}
	err = dir.Sync()
	dir.Close()
	if err != nil {
		return a, err
	}
	if err := b.publishApplicationBackupRecipient(backup.ApplicationBackup.Intent.Recipient, false); err != nil {
		return a, err
	}
	return a, nil
}

func removeRecoveryStage(directory string, proofs []appvalues.ApplicationExportProof) error {
	allowed := map[string]int64{"archive.json": 2 << 20}
	for _, p := range proofs {
		allowed[filepath.Join("applications", restoreCipherName(p.Resource))] = int64(p.Bytes)
	}
	err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		if relative == "." || relative == "applications" {
			return privateRecoveryDirectory(path)
		}
		limit, ok := allowed[relative]
		var st unix.Stat_t
		if !ok || entry.IsDir() || unix.Lstat(path, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&07777 != 0600 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || st.Size > limit {
			return lifecycle.ErrApplicationBackup
		}
		return nil
	})
	if err != nil {
		return err
	}
	return os.RemoveAll(directory)
}

func copyRecoveryCipher(ctx context.Context, source, target string, proof appvalues.ApplicationExportProof) error {
	fd, err := unix.Openat2(unix.AT_FDCWD, source, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NONBLOCK, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return err
	}
	input := os.NewFile(uintptr(fd), "encrypted-recovery-input")
	defer input.Close()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&07777 != 0600 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || st.Size != int64(proof.Bytes) || proof.Bytes < 1 || proof.Bytes > appvalues.MaxApplicationArchive {
		return lifecycle.ErrApplicationBackup
	}
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer output.Close()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(output, hash), io.LimitReader(input, int64(proof.Bytes)+1))
	if err != nil || n != int64(proof.Bytes) || hex.EncodeToString(hash.Sum(nil)) != proof.CipherSHA256 || ctx.Err() != nil {
		return lifecycle.ErrApplicationBackup
	}
	return output.Sync()
}
