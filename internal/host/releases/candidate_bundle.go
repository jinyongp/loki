package releases

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	maxCandidateEvidenceBytes = int64(2 << 20)
	maxCandidateChecksumBytes = int64(1 << 20)
)

func VerifyCandidateBundle(root string) (CandidateEvidence, error) {
	root = strings.TrimSpace(root)
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root ||
		root == string(filepath.Separator) || strings.ContainsRune(root, 0) {
		return CandidateEvidence{}, errors.New("release candidate bundle root must be a clean absolute non-root path")
	}
	for _, directory := range []string{root, filepath.Join(root, "inputs")} {
		info, err := os.Lstat(directory)
		if err != nil {
			return CandidateEvidence{}, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return CandidateEvidence{}, errors.New("release candidate bundle directories must be real")
		}
	}

	evidenceRaw, _, err := readBundleRegular(filepath.Join(root, "evidence.json"), maxCandidateEvidenceBytes)
	if err != nil {
		return CandidateEvidence{}, err
	}
	evidence, err := LoadCandidateEvidence(evidenceRaw)
	if err != nil {
		return CandidateEvidence{}, err
	}
	files := []FileEvidence{
		evidence.ReleaseIndex, evidence.ReleaseManifest, evidence.HostBinary, evidence.Bootstrap,
		evidence.HostAssets, evidence.WSLAppliance, evidence.ToolchainCatalog, evidence.Provenance, evidence.Notices,
		evidence.ReleaseNotes, evidence.EffectivePolicy, evidence.EffectiveConfig,
	}
	for _, item := range files {
		if err = verifyBundleFile(root, item); err != nil {
			return CandidateEvidence{}, err
		}
	}
	for _, executable := range []FileEvidence{evidence.HostBinary, evidence.Bootstrap} {
		file, info, openErr := openBundleRegular(filepath.Join(root, filepath.FromSlash(executable.Path)))
		if openErr != nil {
			return CandidateEvidence{}, openErr
		}
		closeErr := file.Close()
		if closeErr != nil {
			return CandidateEvidence{}, closeErr
		}
		if info.Mode().Perm()&0111 == 0 {
			return CandidateEvidence{}, fmt.Errorf("release candidate executable %s is not executable", executable.Path)
		}
	}

	if err = verifyBundleShape(root, files); err != nil {
		return CandidateEvidence{}, err
	}
	checksumRaw, _, err := readBundleRegular(filepath.Join(root, "SHA256SUMS"), maxCandidateChecksumBytes)
	if err != nil {
		return CandidateEvidence{}, err
	}
	expected := make([]string, 0, len(files)+1)
	for _, item := range files {
		expected = append(expected, fmt.Sprintf("%s  %s", item.SHA256, item.Path))
	}
	evidenceSum := sha256.Sum256(evidenceRaw)
	expected = append(expected, fmt.Sprintf("%s  evidence.json", hex.EncodeToString(evidenceSum[:])))
	sort.Strings(expected)
	expectedRaw := []byte(strings.Join(expected, "\n") + "\n")
	if !bytes.Equal(checksumRaw, expectedRaw) {
		return CandidateEvidence{}, errors.New("release candidate SHA256SUMS does not match evidence")
	}
	return evidence, nil
}

func verifyBundleFile(root string, evidence FileEvidence) error {
	path := filepath.Join(root, filepath.FromSlash(evidence.Path))
	file, info, err := openBundleRegular(path)
	if err != nil {
		return err
	}
	defer file.Close()
	if info.Size() != evidence.Length {
		return fmt.Errorf("release candidate file %s length does not match evidence", evidence.Path)
	}
	digest := sha256.New()
	written, err := io.Copy(digest, file)
	if err != nil {
		return err
	}
	if written != evidence.Length || hex.EncodeToString(digest.Sum(nil)) != evidence.SHA256 {
		return fmt.Errorf("release candidate file %s digest does not match evidence", evidence.Path)
	}
	return nil
}

func verifyBundleShape(root string, files []FileEvidence) error {
	rootEntries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	if len(rootEntries) != 3 {
		return errors.New("release candidate bundle root contains unexpected entries")
	}
	rootExpected := map[string]bool{"SHA256SUMS": true, "evidence.json": true, "inputs": true}
	for _, entry := range rootEntries {
		if !rootExpected[entry.Name()] {
			return errors.New("release candidate bundle root contains an unexpected entry")
		}
	}
	inputExpected := make(map[string]bool, len(files))
	for _, file := range files {
		if filepath.Dir(filepath.FromSlash(file.Path)) != "inputs" {
			return errors.New("release candidate input path is outside the inputs directory")
		}
		inputExpected[filepath.Base(filepath.FromSlash(file.Path))] = true
	}
	inputEntries, err := os.ReadDir(filepath.Join(root, "inputs"))
	if err != nil {
		return err
	}
	if len(inputEntries) != len(inputExpected) {
		return errors.New("release candidate inputs contain unexpected entries")
	}
	for _, entry := range inputEntries {
		if entry.IsDir() || !inputExpected[entry.Name()] {
			return errors.New("release candidate inputs contain an unexpected entry")
		}
	}
	return nil
}

func readBundleRegular(path string, maximum int64) ([]byte, os.FileInfo, error) {
	file, info, err := openBundleRegular(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	if info.Size() <= 0 || info.Size() > maximum {
		return nil, nil, errors.New("release candidate metadata file exceeds its size policy")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(raw)) != info.Size() {
		return nil, nil, errors.New("release candidate metadata file changed while being read")
	}
	return raw, info, nil
}

func openBundleRegular(path string) (*os.File, os.FileInfo, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	file := os.NewFile(uintptr(fd), filepath.Base(path))
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, nil, errors.New("release candidate bundle file must be regular")
	}
	return file, info, nil
}
