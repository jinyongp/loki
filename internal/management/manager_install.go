package management

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"loki/internal/tools"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const maxManagerBytes = 256 << 20

type ManagerRecord struct {
	Schema     int    `json:"schema"`
	Root       string `json:"management_root"`
	Executable string `json:"executable"`
	Release    string `json:"release"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	SHA256     string `json:"sha256"`
	Bytes      int64  `json:"bytes"`
}

func (r ManagerRecord) validate() error {
	if r.Schema != 1 || r.Bytes <= 0 || r.Bytes > maxManagerBytes || r.OS != runtime.GOOS || r.Arch != runtime.GOARCH {
		return fmt.Errorf("invalid native manager ownership record")
	}
	if err := (tools.Config{Schema: 1, Release: r.Release, Host: tools.Host{Kind: "local"}, Mode: tools.ProjectHost}).Validate(); err != nil {
		return err
	}
	if err := (tools.Layout{Root: r.Root}).Validate(); err != nil {
		return err
	}
	if err := (tools.Layout{Root: filepath.Dir(r.Executable)}).Validate(); err != nil {
		return err
	}
	name := "loki"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if filepath.Base(r.Executable) != name || filepath.Clean(r.Executable) != r.Executable {
		return fmt.Errorf("invalid manager executable location")
	}
	return (tools.Operation{Schema: 1, ID: "op-manager", Module: "manager", Action: "install", Phase: tools.Prepared, Candidate: r.SHA256}).Validate()
}

type managerPublication struct {
	Schema    int            `json:"schema"`
	ID        string         `json:"id"`
	Phase     tools.Phase    `json:"phase"`
	Previous  *ManagerRecord `json:"previous,omitempty"`
	Candidate ManagerRecord  `json:"candidate"`
}

func (p managerPublication) validate(s Store) error {
	if p.Schema != 1 || p.Candidate.Root != s.Root {
		return fmt.Errorf("manager publication belongs to another management root")
	}
	if err := p.Candidate.validate(); err != nil {
		return err
	}
	if err := (tools.Config{Schema: 1, Release: p.Candidate.Release, Host: tools.Host{Kind: "local"}, Mode: tools.ProjectHost}).Validate(); err != nil {
		return err
	}
	if p.Previous != nil {
		if err := p.Previous.validate(); err != nil {
			return err
		}
		if p.Previous.Root != s.Root || p.Previous.Executable != p.Candidate.Executable {
			return fmt.Errorf("manager publication ownership changed")
		}
	}
	return (tools.Operation{Schema: 1, ID: p.ID, Module: "manager", Action: "install", Phase: p.Phase, Candidate: p.Candidate.SHA256}).Validate()
}

func managerDigest(path string) (string, int64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", 0, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxManagerBytes {
		return "", 0, fmt.Errorf("manager binary must be a bounded regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(f, maxManagerBytes+1))
	if err != nil {
		return "", 0, err
	}
	if n > maxManagerBytes {
		return "", 0, fmt.Errorf("manager binary exceeds size limit")
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), n, nil
}

func managerBinLock(directory string) (func(), error) {
	if err := realDirectories(directory); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(directory, 0755); err != nil {
		return nil, err
	}
	path := filepath.Join(directory, ".loki-manager-install.lock")
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("manager bin lock is not a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	unlock, err := lockFile(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("manager bin directory is busy: %w", err)
	}
	return func() { unlock(); _ = f.Close() }, nil
}

func (s Store) readManagerPublication() (*managerPublication, error) {
	var p managerPublication
	err := readOwnedJSON(filepath.Join(s.Root, "manager-install.json"), tools.MaxManifestBytes, &p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := p.validate(s); err != nil {
		return nil, err
	}
	return &p, nil
}

func (s Store) InstallManager(ctx context.Context, source, binDirectory string) (ManagerRecord, error) {
	if err := (tools.Layout{Root: binDirectory}).Validate(); err != nil {
		return ManagerRecord{}, err
	}
	binDirectory = filepath.Clean(binDirectory)
	unlock, err := s.Lock()
	if err != nil {
		return ManagerRecord{}, err
	}
	defer unlock()
	if err := s.mutable(); err != nil {
		return ManagerRecord{}, err
	}
	unlockBin, err := managerBinLock(binDirectory)
	if err != nil {
		return ManagerRecord{}, err
	}
	defer unlockBin()
	name := "loki"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	executable := filepath.Join(binDirectory, name)
	digest, size, err := managerDigest(source)
	if err != nil {
		return ManagerRecord{}, err
	}
	candidate := ManagerRecord{Schema: 1, Root: s.Root, Executable: executable, Release: Release, OS: runtime.GOOS, Arch: runtime.GOARCH, SHA256: digest, Bytes: size}
	var previous *ManagerRecord
	if currentDigest, currentSize, err := managerDigest(executable); err == nil {
		var owned ManagerRecord
		if err := readOwnedJSON(executable+".loki-owner.json", tools.MaxManifestBytes, &owned); err != nil {
			return ManagerRecord{}, fmt.Errorf("existing loki command is not owned by this installation; choose another bin directory: %w", err)
		}
		if owned.validate() != nil || owned.Root != s.Root || owned.Executable != executable || owned.SHA256 != currentDigest || owned.Bytes != currentSize {
			return ManagerRecord{}, fmt.Errorf("existing loki command ownership differs; choose another bin directory")
		}
		previous = &owned
		if currentDigest == digest && currentSize == size {
			state, err := s.Load()
			if err != nil {
				return ManagerRecord{}, err
			}
			if len(state.Installed) == 0 {
				state.Config.Release = Release
			}
			if err := s.Save(state); err != nil {
				return ManagerRecord{}, err
			}
			return owned, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return ManagerRecord{}, err
	}
	state, err := s.Load()
	if err != nil {
		return ManagerRecord{}, err
	}
	// An empty CLI installation follows the newly published manager version.
	// Populated installations retain their release until a tool update commits.
	if len(state.Installed) == 0 {
		state.Config.Release = Release
	}
	if err := s.Save(state); err != nil {
		return ManagerRecord{}, err
	}
	p := managerPublication{Schema: 1, ID: fmt.Sprintf("op-%d", time.Now().UnixNano()), Phase: tools.Prepared, Previous: previous, Candidate: candidate}
	if err := p.validate(s); err != nil {
		return ManagerRecord{}, err
	}
	journal := filepath.Join(s.Root, "manager-install.json")
	if err := atomicJSON(journal, p); err != nil {
		return ManagerRecord{}, err
	}
	stage := filepath.Join(binDirectory, ".loki-manager-"+p.ID+".tmp")
	input, err := os.Open(source)
	if err != nil {
		return ManagerRecord{}, err
	}
	defer input.Close()
	output, err := os.OpenFile(stage, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0755)
	if err != nil {
		return ManagerRecord{}, err
	}
	_, err = io.Copy(output, io.LimitReader(input, maxManagerBytes+1))
	if err == nil {
		err = output.Sync()
	}
	closeErr := output.Close()
	if err != nil {
		return ManagerRecord{}, err
	}
	if closeErr != nil {
		return ManagerRecord{}, closeErr
	}
	stagedDigest, stagedSize, err := managerDigest(stage)
	if err != nil || stagedDigest != digest || stagedSize != size {
		return ManagerRecord{}, fmt.Errorf("manager source changed while installing")
	}
	p.Phase = tools.Staged
	if err := atomicJSON(journal, p); err != nil {
		return ManagerRecord{}, err
	}
	if err := ctx.Err(); err != nil {
		return ManagerRecord{}, err
	}
	if err := os.Rename(stage, executable); err != nil {
		return ManagerRecord{}, fmt.Errorf("manager binary could not be published; close processes using the installed command and run bundled loki tools recover: %w", err)
	}
	if err := atomicJSON(executable+".loki-owner.json", candidate); err != nil {
		return ManagerRecord{}, err
	}
	p.Phase = tools.Committed
	if err := atomicJSON(journal, p); err != nil {
		return ManagerRecord{}, err
	}
	return candidate, nil
}

func (s Store) recoverManagerPublication() error {
	p, err := s.readManagerPublication()
	if err != nil {
		return err
	}
	if p == nil || p.Phase == tools.Committed || p.Phase == tools.Aborted {
		return nil
	}
	directory := filepath.Dir(p.Candidate.Executable)
	unlock, err := managerBinLock(directory)
	if err != nil {
		return err
	}
	defer unlock()
	digest, size, err := managerDigest(p.Candidate.Executable)
	if err == nil && digest == p.Candidate.SHA256 && size == p.Candidate.Bytes {
		if err := atomicJSON(p.Candidate.Executable+".loki-owner.json", p.Candidate); err != nil {
			return err
		}
		p.Phase = tools.Committed
	} else if (p.Previous == nil && errors.Is(err, os.ErrNotExist)) || (err == nil && p.Previous != nil && digest == p.Previous.SHA256 && size == p.Previous.Bytes) {
		p.Phase = tools.Aborted
	} else {
		return fmt.Errorf("manager publication recovery basis changed; inspect doctor")
	}
	stage := filepath.Join(directory, ".loki-manager-"+p.ID+".tmp")
	if info, err := os.Lstat(stage); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("manager staging file is not owned regular storage")
		}
		if err := os.Remove(stage); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return atomicJSON(filepath.Join(s.Root, "manager-install.json"), p)
}
