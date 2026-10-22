//go:build linux

package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"
	"golang.org/x/sys/unix"
	"loki/internal/control/appvalues"
	"loki/internal/platform/privateio"
	"loki/internal/platform/safeio"
)

const applicationArchiveLimit = 64 << 20

// applicationExport records public scope and ciphertext integrity only. The
// provider performs logical export/encryption; no vault/key/session file enters
// this path. Artifacts stay in the broker-owned generation until the host owner
// copies them to its private archive and acknowledges the canonical operation.
type applicationExport = appvalues.ApplicationExportProof

type applicationExportIntent struct {
	Version   int    `json:"version"`
	Operation string `json:"operation_id"`
	Resource  string `json:"resource_id"`
	Policy    string `json:"policy"`
	Revision  string `json:"revision"`
	Recipient string `json:"recipient"`
}

func (s *Service) exportApplication(ctx context.Context, q AdminRequest, body []byte) (map[string]any, error) {
	if len(body) != 0 || q.Environment != "" || q.Principal != "" || q.Variables != nil || q.Members != nil || q.Name != "" || q.Empty || q.Replace || !adminUUID.MatchString(q.RequestID) || !revisionPattern.MatchString(q.Revision) {
		return nil, invalid()
	}
	recipient, err := age.ParseX25519Recipient(q.Recipient)
	if err != nil || recipient.String() != q.Recipient {
		return nil, invalid()
	}
	var resourceID, profile string
	for _, r := range s.options.Resources {
		if r.Alias == q.Resource {
			resourceID, profile = r.ID, r.Profile()
			break
		}
	}
	if resourceID == "" {
		return nil, denied()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	gate, err := s.maintenanceState()
	if err != nil {
		return nil, err
	}
	if gate == nil || !gate.Active || gate.Operation != q.RequestID {
		return nil, conflict()
	}
	current, err := s.options.Provider.Revision(ctx, profile, "common")
	if err != nil {
		return nil, safeError(err)
	}
	if current != q.Revision {
		return nil, conflict()
	}
	intent := applicationExportIntent{Version: 1, Operation: q.RequestID, Resource: resourceID, Policy: s.options.PolicySHA256, Revision: q.Revision, Recipient: q.Recipient}
	directory := s.options.Root
	for _, name := range []string{"exports", strings.ReplaceAll(q.RequestID, "-", ""), strings.ReplaceAll(resourceID, "-", "")} {
		directory = filepath.Join(directory, name)
		if err := os.Mkdir(directory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, unavailable()
		}
		if privateDirectory(directory, false) != nil {
			return nil, unavailable()
		}
		parent, err := os.Open(filepath.Dir(directory))
		if err != nil {
			return nil, unavailable()
		}
		syncErr := parent.Sync()
		closeErr := parent.Close()
		if syncErr != nil || closeErr != nil {
			return nil, unavailable()
		}
	}
	intentPath := filepath.Join(directory, "intent.json")
	previous, err := readPrivate(intentPath, 4096)
	if err == nil {
		var old applicationExportIntent
		if privateio.Decode(previous, &old) != nil || old != intent {
			return nil, conflict()
		}
	} else if errors.Is(err, os.ErrNotExist) {
		encoded, _ := json.Marshal(intent)
		if safeio.PublishPrivate(intentPath, encoded, false) != nil {
			return nil, unavailable()
		}
	} else {
		return nil, unavailable()
	}
	proofPath, archive, pending := filepath.Join(directory, "proof.json"), filepath.Join(directory, "application.age"), filepath.Join(directory, "pending.age")
	var proof applicationExport
	previous, err = readPrivate(proofPath, 4096)
	if err == nil {
		if privateio.Decode(previous, &proof) != nil || proof.Version != intent.Version || proof.Operation != intent.Operation || proof.Resource != intent.Resource || proof.Policy != intent.Policy || proof.Revision != intent.Revision || proof.Recipient != intent.Recipient || !revisionPattern.MatchString(proof.CipherSHA256) || proof.Bytes < 1 || proof.Bytes > applicationArchiveLimit {
			return nil, unavailable()
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if _, err := os.Lstat(archive); !errors.Is(err, os.ErrNotExist) {
			return nil, unavailable()
		}
		// An unacknowledged export has no external mutation effect. Discard
		// only our exact owned encrypted pending file and retry the read.
		if leftover, err := readPrivate(pending, applicationArchiveLimit); err == nil {
			clear(leftover)
			if os.Remove(pending) != nil {
				return nil, unavailable()
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, unavailable()
		}
		if err := s.options.Provider.ExportApplication(ctx, profile, q.Recipient, pending); err != nil {
			return nil, safeError(err)
		}
		ciphertext, err := readPrivate(pending, applicationArchiveLimit)
		if err != nil {
			return nil, unavailable()
		}
		sum := sha256.Sum256(ciphertext)
		proof = applicationExport{Version: intent.Version, Operation: intent.Operation, Resource: intent.Resource, Policy: intent.Policy, Revision: intent.Revision, Recipient: intent.Recipient, CipherSHA256: hex.EncodeToString(sum[:]), Bytes: len(ciphertext)}
		clear(ciphertext)
		encoded, _ := json.Marshal(proof)
		if safeio.PublishPrivate(proofPath, encoded, false) != nil {
			return nil, unavailable()
		}
	} else {
		return nil, unavailable()
	}
	verify := func(path string) error {
		ciphertext, err := readPrivate(path, applicationArchiveLimit)
		if err != nil {
			return err
		}
		defer clear(ciphertext)
		sum := sha256.Sum256(ciphertext)
		if len(ciphertext) != proof.Bytes || hex.EncodeToString(sum[:]) != proof.CipherSHA256 {
			return unavailable()
		}
		return nil
	}
	if err := verify(archive); errors.Is(err, os.ErrNotExist) {
		if verify(pending) != nil {
			return nil, unavailable()
		}
		if unix.Renameat2(unix.AT_FDCWD, pending, unix.AT_FDCWD, archive, unix.RENAME_NOREPLACE) != nil {
			return nil, unavailable()
		}
	} else if err != nil {
		return nil, unavailable()
	}
	dir, err := os.Open(directory)
	if err != nil {
		return nil, unavailable()
	}
	err = dir.Sync()
	closeErr := dir.Close()
	if err != nil || closeErr != nil {
		return nil, unavailable()
	}
	for _, resource := range s.options.Resources {
		if resource.ID == resourceID {
			if err := s.record("host-admin", resource, "backup_export", q.RequestID, "success"); err != nil {
				return nil, err
			}
			break
		}
	}
	return map[string]any{"export": proof}, nil
}
