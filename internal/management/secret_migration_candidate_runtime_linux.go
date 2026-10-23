//go:build linux

package management

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"loki/internal/host/lifecycle"
	"loki/internal/platform/privateio"
)

// Candidate administration runs in a separate root helper. The broker keeps
// ALL capabilities dropped; granting its container DAC_OVERRIDE for root exec
// would also grant that capability to its ordinary value owner.
func (b *DockerFullBackend) candidateProtection(ctx context.Context, r DeploymentReservation, action string, input []byte) (json.RawMessage, error) {
	operation, err := b.migrationRecord(ctx)
	if err != nil {
		return nil, err
	}
	if operation.SecretMigration.RollbackRequested || !candidatePreparationPhase(operation) && !(action == "status" && operation.State == lifecycle.OperationApplying && operation.Phase == lifecycle.PhaseSwitch) {
		return nil, fmt.Errorf("selected or terminal migrations cannot reopen candidate protection")
	}
	record, err := b.record(r)
	if err != nil {
		return nil, err
	}
	if record.MigrationCandidate == nil || record.MigrationCandidate.Operation != lifecycle.MigrationOperationUUID(operation.ID) || record.Preparing {
		return nil, fmt.Errorf("original private candidate is not prepared")
	}
	needsInput := action == "init" || action == "unlock"
	if action != "status" && action != "init" && action != "unlock" && action != "lock" && action != "recover" || needsInput && (len(input) == 0 || len(input) > 65536) || !needsInput && len(input) != 0 {
		return nil, fmt.Errorf("invalid explicit candidate protection request")
	}
	spec := record.Specs[0]
	projection := filepath.Join("/dev/shm", b.Store.FullOwner()+"-migration-"+operation.ID, "secrets")
	mounts := []FullMount{{Kind: "bind", Source: filepath.Join(projection, "admin"), Target: "/run/loki/secrets/admin", ReadOnly: true}, {Kind: "bind", Source: filepath.Join(projection, "admin-layout.json"), Target: "/etc/loki/secret-admin.json", ReadOnly: true}}
	workerBound := false
	for _, mount := range spec.Mounts {
		if mount.Kind == "bind" && mount.ReadOnly && strings.HasPrefix(record.Worker, mount.Target+"/") {
			mounts = append(mounts, mount)
			workerBound = true
		}
	}
	if !workerBound {
		return nil, fmt.Errorf("candidate administrator lacks its immutable worker")
	}
	ids, err := b.containerIDs(ctx, r)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		container, err := b.inspectContainer(ctx, id)
		if err != nil {
			return nil, err
		}
		if err := b.requireParent(r, container); err != nil {
			return nil, err
		}
		if container.Config.Labels[fullServiceLabel] == "prepare" {
			if _, err := b.command(ctx, "removing interrupted owned candidate administrator", nil, "container", "rm", "--force", id); err != nil {
				return nil, err
			}
		}
	}
	args := []string{"run", "--rm", "--name", b.containerName(r, "prepare"), "--network", "none", "--read-only", "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--security-opt", "no-new-privileges:true", "--user", "0:0", "--memory", "134217728", "--pids-limit", "32", "--log-driver", "none", "--entrypoint", record.Worker}
	if needsInput {
		args = append(args, "--interactive")
	}
	for _, label := range b.labels(r, "prepare") {
		args = append(args, "--label", label)
	}
	for _, mount := range mounts {
		argument, err := dockerMountArgument(mount)
		if err != nil {
			return nil, err
		}
		args = append(args, "--mount", argument)
	}
	args = append(args, spec.Image, "secret", "protection", action, "--layout", "/etc/loki/secret-admin.json")
	if needsInput {
		args = append(args, "--stdin")
	}
	result, err := b.command(ctx, "administering private candidate protection", bytes.NewReader(input), args...)
	if err != nil {
		return nil, err
	}
	if err := validateCandidateProtectionMetadata(result, action); err != nil {
		return nil, err
	}
	return json.RawMessage(result), nil
}

// Candidate migration consumes only reviewed protection metadata. Pending
// rewrap belongs to its original administrator operation and blocks migration.
func validateCandidateProtectionMetadata(result []byte, action string) error {
	var metadata struct {
		Action        string `json:"action"`
		State         string `json:"state"`
		Changed       bool   `json:"changed"`
		Backend       string `json:"backend,omitempty"`
		IdleExpiresAt string `json:"idle_expires_at,omitempty"`
		Rewrap        *struct {
			RequestID         string `json:"request_id"`
			FromBackend       string `json:"from_backend"`
			Backend           string `json:"backend"`
			SlotPublished     bool   `json:"slot_published"`
			Completed         bool   `json:"completed"`
			IntegrityVerified bool   `json:"integrity_verified"`
		} `json:"rewrap,omitempty"`
	}
	if privateio.Decode(result, &metadata) != nil || metadata.Action != action || metadata.Backend != "" && metadata.Backend != "passphrase" {
		return fmt.Errorf("invalid candidate protection metadata")
	}
	if metadata.IdleExpiresAt != "" {
		if _, err := time.Parse(time.RFC3339Nano, metadata.IdleExpiresAt); err != nil {
			return fmt.Errorf("invalid candidate protection expiry")
		}
	}
	if metadata.Rewrap != nil {
		return fmt.Errorf("candidate protection change requires original administrator rewrap recovery")
	}
	switch metadata.State {
	case "uninitialized", "unlocked", "locked", "locked_manual", "locking", "recovery_required":
	default:
		return fmt.Errorf("unknown candidate protection state")
	}
	return nil
}

// MigrationCandidateProtection never discovers, saves or reuses a passphrase.
// The caller supplies explicit private input only for init/unlock.
func (b *DockerFullBackend) MigrationCandidateProtection(ctx context.Context, r DeploymentReservation, action string, input []byte) (json.RawMessage, error) {
	return b.candidateProtection(ctx, r, action, input)
}

func (b *DockerFullBackend) StartSecretMigrationCandidate(ctx context.Context, r DeploymentReservation) error {
	operation, err := b.migrationRecord(ctx)
	if err != nil {
		return err
	}
	if !candidatePreparationPhase(operation) {
		return fmt.Errorf("selected or terminal migrations cannot restart a candidate")
	}
	record, err := b.record(r)
	if err != nil {
		return err
	}
	if record.MigrationCandidate == nil || record.Preparing {
		return fmt.Errorf("original private migration candidate is unavailable")
	}
	spec := record.Specs[0]
	inspection, err := b.inspectContainer(ctx, spec.Name)
	if err != nil {
		return err
	}
	if err := b.verifySpec(r, spec, inspection); err != nil {
		return err
	}
	if !inspection.State.Running {
		if _, err := b.command(ctx, "starting the owned private migration broker", nil, "container", "start", spec.Name); err != nil {
			return err
		}
	}
	ready, stop := context.WithTimeout(ctx, 30*time.Second)
	defer stop()
	var firstFailure error
	for {
		if _, err := b.candidateProtection(ready, r, "status", nil); err == nil {
			return nil
		} else if firstFailure == nil {
			firstFailure = err
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ready.Done():
			timer.Stop()
			return fmt.Errorf("private candidate readiness was not established: %w", firstFailure)
		case <-timer.C:
		}
	}
}
