package management

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"loki/internal/tools"
)

var backupIDPattern = regexp.MustCompile(`^backup-[0-9]{19}$`)
var backupComponents = []string{"tools", "data", "providers", "integrations", "auth"}

type DataBackupBackend interface {
	// Return only persistent data with verified host ownership and no attached
	// containers. Socket volumes and daemon-global state are never included.
	OwnedDataPaths(context.Context) (map[string]string, error)
}

type BackupRecord struct {
	Schema     int               `json:"schema"`
	ID         string            `json:"id"`
	Root       string            `json:"root"`
	CreatedAt  time.Time         `json:"created_at"`
	Snapshot   Snapshot          `json:"snapshot"`
	Components map[string]string `json:"components"`
	Volumes    map[string]string `json:"volumes"`
}

func (r BackupRecord) validate() error {
	if r.Schema != 1 || !backupIDPattern.MatchString(r.ID) || !filepath.IsAbs(r.Root) || r.CreatedAt.IsZero() || len(r.Components) > len(backupComponents) || len(r.Volumes) > 64 {
		return fmt.Errorf("invalid backup record")
	}
	if err := r.Snapshot.Validate(); err != nil {
		return err
	}
	for component, digest := range r.Components {
		if !slices.Contains(backupComponents, component) || !deploymentDigestPattern.MatchString(digest) {
			return fmt.Errorf("invalid backup component")
		}
	}
	for name, digest := range r.Volumes {
		if !strings.HasPrefix(name, (Store{Root: r.Root}).FullOwner()+"-data-") || !regexp.MustCompile(`^[a-z0-9-]{1,128}$`).MatchString(name) || !deploymentDigestPattern.MatchString(digest) {
			return fmt.Errorf("invalid backup volume")
		}
	}
	return nil
}

func (s Store) backupPath(id string) (string, error) {
	if !backupIDPattern.MatchString(id) {
		return "", fmt.Errorf("invalid backup ID")
	}
	parent := filepath.Join(s.Root, "backups")
	if err := realDirectories(s.Root, parent, filepath.Join(parent, id)); err != nil {
		return "", err
	}
	return filepath.Join(parent, id), nil
}

func (s Store) ReadBackup(id string) (BackupRecord, error) {
	var record BackupRecord
	directory, err := s.backupPath(id)
	if err != nil {
		return record, err
	}
	if err := readOwnedJSON(filepath.Join(directory, "backup.json"), 2*tools.MaxManifestBytes, &record); err != nil {
		return record, err
	}
	if err := record.validate(); err != nil {
		return record, err
	}
	if record.ID != id || record.Root != filepath.Clean(s.Root) {
		return record, fmt.Errorf("backup belongs to another execution host or management root")
	}
	return record, nil
}

func (s Store) Backups() ([]BackupRecord, error) {
	parent := filepath.Join(s.Root, "backups")
	if err := realDirectories(s.Root, parent); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(parent)
	if errors.Is(err, os.ErrNotExist) {
		return []BackupRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	var records []BackupRecord
	for _, entry := range entries {
		if !entry.IsDir() || !backupIDPattern.MatchString(entry.Name()) {
			continue
		}
		record, err := s.ReadBackup(entry.Name())
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	slices.SortFunc(records, func(a, b BackupRecord) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return records, nil
}

// copyBackupTree never follows links. Modes and link targets are part of its
// digest; a restore validates the complete copy before replacing owned data.
func copyBackupTree(ctx context.Context, source, destination string) (string, error) {
	if err := realDirectories(source); err != nil {
		return "", err
	}
	if destination != "" {
		if err := os.Mkdir(destination, 0700); err != nil {
			return "", err
		}
	}
	hash := sha256.New()
	var total int64
	entries := 0
	var directories []struct {
		path string
		info os.FileInfo
	}
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		entries++
		if entries > 100000 || info.Size() > (64<<30)-total {
			return fmt.Errorf("backup exceeds 100000 entries or 64 GiB; keep large external assets outside managed data")
		}
		fmt.Fprintf(hash, "%d:%s:%d:%s:", len(relative), relative, info.Mode(), backupOwnership(info))
		target := ""
		if destination != "" {
			target = filepath.Join(destination, relative)
		}
		if info.IsDir() {
			if target != "" {
				if relative != "." {
					if err := os.Mkdir(target, 0700); err != nil {
						return err
					}
				}
				directories = append(directories, struct {
					path string
					info os.FileInfo
				}{target, info})
			}
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(hash, "%d:%s", len(link), link)
			if target != "" {
				if err := os.Symlink(link, target); err != nil {
					return err
				}
				return copyBackupMetadata(target, info)
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("backup contains a live socket or unsupported file; stop active sessions before backup")
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		defer input.Close()
		opened, err := input.Stat()
		if err != nil || !os.SameFile(info, opened) {
			return fmt.Errorf("backup source changed while being opened")
		}
		fmt.Fprintf(hash, "%d:", info.Size())
		var writer io.Writer = hash
		var output *os.File
		if target != "" {
			output, err = os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
			if err != nil {
				return err
			}
			writer = io.MultiWriter(hash, output)
		}
		n, copyErr := io.CopyN(writer, input, info.Size())
		total += n
		if output != nil {
			syncErr := output.Sync()
			closeErr := output.Close()
			if copyErr == nil {
				copyErr = errors.Join(syncErr, closeErr)
			}
		}
		if copyErr != nil {
			return copyErr
		}
		if target != "" {
			if err := copyBackupMetadata(target, info); err != nil {
				return err
			}
		}
		current, err := os.Lstat(path)
		if err != nil || !os.SameFile(info, current) || current.Size() != info.Size() || !current.ModTime().Equal(info.ModTime()) {
			return fmt.Errorf("backup source changed during copying; retry after stopping writers")
		}
		return nil
	})
	if err == nil {
		for i := len(directories) - 1; i >= 0; i-- {
			if metadataErr := copyBackupMetadata(directories[i].path, directories[i].info); metadataErr != nil {
				return "", metadataErr
			}
		}
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if err == nil && destination != "" {
		copied, verifyErr := copyBackupTree(ctx, destination, "")
		if verifyErr != nil {
			return "", verifyErr
		}
		if copied != digest {
			return "", fmt.Errorf("backup copy metadata or content differs from its source")
		}
	}
	return digest, err
}

func (s Store) Backup(ctx context.Context, backend DataBackupBackend) (BackupRecord, error) {
	unlock, err := s.Lock()
	if err != nil {
		return BackupRecord{}, err
	}
	defer unlock()
	if err := s.mutable(); err != nil {
		return BackupRecord{}, err
	}
	state, err := s.Load()
	if err != nil {
		return BackupRecord{}, err
	}
	deployments, err := s.Deployments()
	if err != nil {
		return BackupRecord{}, err
	}
	if len(deployments) != 0 {
		return BackupRecord{}, fmt.Errorf("stop selected services before creating a consistent backup")
	}
	var releases []func()
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	for _, installation := range state.Installed {
		release, err := s.generationLock(installation.Artifact, false)
		if err != nil {
			return BackupRecord{}, err
		}
		releases = append(releases, release)
	}
	records, err := s.Backups()
	if err != nil {
		return BackupRecord{}, err
	}
	if len(records) >= 16 {
		return BackupRecord{}, fmt.Errorf("16 backups are retained; remove an unneeded backup with loki backups remove ID")
	}
	parent := filepath.Join(s.Root, "backups")
	if err := os.MkdirAll(parent, 0700); err != nil {
		return BackupRecord{}, err
	}
	id := fmt.Sprintf("backup-%019d", time.Now().UnixNano())
	stage, err := os.MkdirTemp(parent, ".preparing-")
	if err != nil {
		return BackupRecord{}, err
	}
	defer os.RemoveAll(stage)
	record := BackupRecord{Schema: 1, ID: id, Root: filepath.Clean(s.Root), CreatedAt: time.Now().UTC(), Snapshot: state, Components: map[string]string{}, Volumes: map[string]string{}}
	for _, name := range backupComponents {
		source := filepath.Join(s.Root, name)
		if _, err := os.Lstat(source); errors.Is(err, os.ErrNotExist) {
			source = filepath.Join(stage, ".empty-"+name)
			if err := os.Mkdir(source, 0700); err != nil {
				return record, err
			}
		} else if err != nil {
			return record, err
		}
		digest, err := copyBackupTree(ctx, source, filepath.Join(stage, name))
		if err != nil {
			return record, err
		}
		record.Components[name] = digest
		if filepath.Dir(source) == stage {
			if err := os.Remove(source); err != nil {
				return record, err
			}
		}
	}
	if state.Config.Mode == tools.Full {
		if backend == nil {
			return record, fmt.Errorf("full backup requires its owned data backend")
		}
		paths, err := backend.OwnedDataPaths(ctx)
		if err != nil {
			return record, err
		}
		if err := os.Mkdir(filepath.Join(stage, "volumes"), 0700); err != nil {
			return record, err
		}
		for name, path := range paths {
			digest, err := copyBackupTree(ctx, path, filepath.Join(stage, "volumes", name))
			if err != nil {
				return record, err
			}
			record.Volumes[name] = digest
		}
	}
	if err := record.validate(); err != nil {
		return record, err
	}
	if err := atomicJSON(filepath.Join(stage, "backup.json"), record); err != nil {
		return record, err
	}
	target, err := s.backupPath(id)
	if err != nil {
		return record, err
	}
	return record, os.Rename(stage, target)
}
