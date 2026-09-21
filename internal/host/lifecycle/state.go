package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"loki/internal/platform/safeio"
)

const maxLifecycleStateBytes = 1 << 20

type FileStore struct {
	Root string
}

func OpenFileStore(root string) (*FileStore, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(filepath.Separator) || strings.ContainsRune(root, 0) {
		return nil, errors.New("host lifecycle state root must be a clean absolute non-root path")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("host lifecycle state root must be a private real directory")
	}
	return &FileStore{Root: root}, nil
}

func (s *FileStore) Snapshot(ctx context.Context) (Snapshot, error) {
	if s == nil {
		return Snapshot{}, errors.New("host lifecycle store is not configured")
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	var snapshot Snapshot
	if err := readPrivateJSON(s.path("host.json"), &snapshot.Host, true); err != nil {
		return Snapshot{}, err
	}
	installed, err := readOptionalGeneration(s.path("installed.json"))
	if err != nil {
		return Snapshot{}, err
	}
	available, err := readOptionalGeneration(s.path("available.json"))
	if err != nil {
		return Snapshot{}, err
	}
	prepared, err := readOptionalPlan(s.path("prepared.json"))
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.Installed = installed
	snapshot.Available = available
	snapshot.Prepared = prepared
	if _, err = snapshot.Host.normalized(); err != nil {
		return Snapshot{}, err
	}
	if installed != nil && !installed.Valid() {
		return Snapshot{}, errors.New("installed release generation is invalid")
	}
	if available != nil && !available.Valid() {
		return Snapshot{}, errors.New("available release generation is invalid")
	}
	if prepared != nil && !prepared.Valid() {
		return Snapshot{}, errors.New("prepared host update plan is invalid")
	}
	return snapshot, nil
}

func (s *FileStore) SavePrepared(ctx context.Context, plan PreparedPlan) error {
	if s == nil {
		return errors.New("host lifecycle store is not configured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !plan.Valid() {
		return errors.New("prepared host update plan is invalid")
	}
	snapshot, err := s.Snapshot(ctx)
	if err != nil {
		return err
	}
	if snapshot.Host.Revision != plan.ObservedHostRevision ||
		snapshot.Host.ActiveGenerationID != plan.ActiveGenerationID ||
		snapshot.Available == nil || snapshot.Available.ID != plan.CandidateGenerationID {
		return errors.New("host lifecycle state changed before prepared plan publication")
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if len(raw) > maxLifecycleStateBytes {
		return errors.New("prepared host update plan exceeds size limit")
	}
	if err = safeio.PublishPrivate(s.path("prepared.json"), raw, true); err != nil {
		return err
	}
	return ctx.Err()
}

func (s *FileStore) path(name string) string {
	return filepath.Join(s.Root, name)
}

func readOptionalGeneration(path string) (*Generation, error) {
	var generation Generation
	if err := readPrivateJSON(path, &generation, false); err != nil {
		return nil, err
	}
	if generation.ID == "" {
		return nil, nil
	}
	return &generation, nil
}

func readOptionalPlan(path string) (*PreparedPlan, error) {
	var plan PreparedPlan
	if err := readPrivateJSON(path, &plan, false); err != nil {
		return nil, err
	}
	if plan.ID == "" {
		return nil, nil
	}
	return &plan, nil
}

func readPrivateJSON(path string, target any, required bool) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) && !required {
		return nil
	}
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), filepath.Base(path))
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return errors.New("host lifecycle state file must be a private regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxLifecycleStateBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > maxLifecycleStateBytes {
		return errors.New("host lifecycle state file exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		return errors.New("host lifecycle state file is invalid")
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("host lifecycle state file contains trailing data")
	}
	return nil
}
