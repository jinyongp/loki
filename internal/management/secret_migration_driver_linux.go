//go:build linux

package management

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"golang.org/x/sys/unix"
	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/host/lifecycle"
	hostpolicy "loki/internal/host/policy"
)

type SecretMigrationStatus struct {
	Operation  string                   `json:"operation_id"`
	State      lifecycle.OperationState `json:"state"`
	Phase      lifecycle.OperationPhase `json:"phase"`
	Generation string                   `json:"candidate_generation"`
	Policy     string                   `json:"policy"`
	Revision   uint64                   `json:"source_revision"`
	Protection string                   `json:"protection,omitempty"`
	Released   bool                     `json:"released"`
}

func migrationStatus(record lifecycle.OperationRecord) SecretMigrationStatus {
	return SecretMigrationStatus{Operation: lifecycle.MigrationOperationUUID(record.ID), State: record.State, Phase: record.Phase, Generation: record.SecretMigration.Intent.CandidateGeneration, Policy: record.SecretMigration.Intent.Policy, Revision: record.SecretMigration.Intent.Plan.Revision, Released: record.Maintenance.Released}
}

func (s Store) SecretMigrationStatus(ctx context.Context) (SecretMigrationStatus, error) {
	var status SecretMigrationStatus
	err := s.WithSecretMigrationJournal(ctx, lifecycle.OperationJournalOptions{}, func(j *lifecycle.OperationJournal) error {
		records, err := j.List()
		if err != nil {
			return err
		}
		for _, record := range records {
			if record.SecretMigration != nil {
				status = migrationStatus(record)
			}
		}
		if status.Operation == "" {
			return fmt.Errorf("no recorded secret migration exists")
		}
		return nil
	})
	return status, err
}

// PrepareSecretMigration freezes public intent only. Source decryption stays in
// its private helper; no host operation, key or provider generation is created.
func (b *DockerFullBackend) PrepareSecretMigration(ctx context.Context, mappings []appvalues.MigrationProfileMapping, resources []controlpolicy.ApplicationResource, recipient, backup string, interrupt bool) (lifecycle.SecretMigrationIntent, error) {
	var intent lifecycle.SecretMigrationIntent
	unlock, err := b.Store.Lock()
	if err != nil {
		return intent, err
	}
	defer unlock()
	if err := b.Store.RequireMutable(); err != nil {
		return intent, err
	}
	if !filepath.IsAbs(backup) || filepath.Clean(backup) != backup || backup == "/" {
		return intent, fmt.Errorf("choose a private absolute operator backup path")
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, filepath.Dir(backup), &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return intent, fmt.Errorf("create the private operator backup directory before preparing migration")
	}
	var info unix.Stat_t
	err = unix.Fstat(fd, &info)
	unix.Close(fd)
	if err != nil || info.Uid != uint32(os.Geteuid()) || info.Mode&07777 != 0700 {
		return intent, fmt.Errorf("operator backup directory must be private and owned")
	}
	if _, err := os.Lstat(backup); !errors.Is(err, os.ErrNotExist) {
		return intent, fmt.Errorf("choose an unused operator backup filename")
	}
	plan, err := b.PrepareSecretMigrationSource(ctx, mappings, resources)
	if err != nil {
		return intent, err
	}
	before, err := b.InspectSecretMigrationIdentity(ctx)
	if err != nil {
		return intent, err
	}
	state, err := b.Store.Load()
	if err != nil {
		return intent, err
	}
	c, contract, err := b.Store.fullApplicationInputs()
	if err != nil {
		return intent, err
	}
	previous, err := hostpolicy.CompileApplications(c, contract, state.ApplicationResources)
	if err != nil {
		return intent, err
	}
	next, err := hostpolicy.CompileApplications(c, contract, plan.Resources)
	if err != nil {
		return intent, err
	}
	inputs, err := b.Store.FullResources()
	if err != nil {
		return intent, err
	}
	provider, err := inputs.ServiceProgram("runtime-core", "devtools")
	if err != nil {
		return intent, err
	}
	var generation [16]byte
	if _, err := rand.Read(generation[:]); err != nil {
		return intent, err
	}
	intent = lifecycle.SecretMigrationIntent{Plan: plan, Before: before, CandidateGeneration: hex.EncodeToString(generation[:]), ExpectedPolicy: previous.Digest(), Policy: next.Digest(), Mode: "managed-full", SourceRoot: "/var/lib/loki/runtime", IdentityFile: "/var/lib/loki/secrets/deployment.json", StateRoot: "/var/lib/loki/secrets", ServiceStateRoot: "/var/lib/loki/secrets", RuntimeRoot: "/run/loki/secrets", Binary: provider, Recipient: recipient, BackupPath: backup, Interrupt: interrupt}
	if !intent.Valid() {
		return lifecycle.SecretMigrationIntent{}, fmt.Errorf("migration intent conflicts with current identity, policy or recipient")
	}
	return intent, nil
}

// ApplySecretMigration runs only under the existing transaction/owner lock.
// Omit intent to recover the active operation. Protection is explicit private
// input for this invocation; a cold target returns pending without auto-init.
func (b *DockerFullBackend) ApplySecretMigration(ctx context.Context, intent *lifecycle.SecretMigrationIntent, protection string, passphrase []byte) (SecretMigrationStatus, error) {
	var status SecretMigrationStatus
	if protection != "" && protection != "init" && protection != "unlock" || (protection != "") != (len(passphrase) > 0) || len(passphrase) > 65536 {
		return status, fmt.Errorf("choose explicit init/unlock with private input, or recover without protection input")
	}
	err := b.Store.WithSecretMigrationJournal(ctx, lifecycle.OperationJournalOptions{}, func(j *lifecycle.OperationJournal) error {
		var operation lifecycle.OperationRecord
		if intent != nil {
			// A completed exact plan is a saved result, never another migration.
			records, err := j.List()
			if err != nil {
				return err
			}
			for _, record := range records {
				if record.SecretMigration != nil && reflect.DeepEqual(record.SecretMigration.Intent, *intent) {
					operation = record
					break
				}
			}
			if operation.ID == "" {
				before, err := b.InspectSecretMigrationIdentity(ctx)
				if err != nil || before != intent.Before {
					return fmt.Errorf("prepared migration identity changed before admission")
				}
				operation, err = j.BeginSecretMigration(*intent, time.Now())
				if err != nil {
					return err
				}
			}
		} else {
			var found bool
			var err error
			operation, found, err = j.Active()
			if err != nil || !found || operation.SecretMigration == nil {
				return fmt.Errorf("no original secret migration requires recovery")
			}
		}
		status = migrationStatus(operation)
		if operation.Maintenance.Released {
			return nil
		}
		return b.WithSecretMigrationMaintenance(ctx, j, func(scoped context.Context) error {
			transaction, err := b.Store.readTransaction()
			if err != nil || transaction == nil {
				return fmt.Errorf("original migration transaction is unavailable")
			}
			candidate, err := fullMigrationReservation(operation, transaction.Previous)
			if err != nil {
				return err
			}
			for step := 0; step < 8; step++ {
				current, found, err := j.Active()
				if err != nil || !found || current.ID != operation.ID {
					return fmt.Errorf("original migration owner changed during recovery")
				}
				status = migrationStatus(current)
				if current.State.Terminal() {
					if current.State != lifecycle.OperationSucceeded {
						return fmt.Errorf("rolled-back migration requires its original restored serving identity")
					}
					digest := sha256.Sum256([]byte("loki-secret-migration-serving\x00" + current.PlanID))
					reservations, err := b.Store.Deployments()
					if err != nil {
						return err
					}
					for _, serving := range reservations {
						if serving.ID == fmt.Sprintf("deployment-%x", digest[:16]) {
							if err := b.FinalizeSecretMigration(scoped, candidate, serving); err != nil {
								return err
							}
							status.Released = true
							return nil
						}
					}
					return fmt.Errorf("original serving reservation is unavailable")
				}
				switch current.Phase {
				case lifecycle.PhaseAdmitted, lifecycle.PhaseSnapshot:
					reservations, err := b.Store.Deployments()
					if err != nil {
						return err
					}
					for _, previous := range reservations {
						if previous.ID == candidate.ID || b.ConfirmAbsent(scoped, previous) == nil {
							continue
						}
						ids, err := b.containerIDs(scoped, previous)
						if err != nil {
							return err
						}
						for _, id := range ids {
							container, err := b.inspectContainer(scoped, id)
							if err != nil {
								return err
							}
							if err := b.requireParent(previous, container); err != nil {
								return err
							}
							if container.State.Running && container.Config.Labels[fullServiceLabel] == "secrets" {
								if err := b.FenceSecretMigrationApplication(scoped, previous, true); err != nil {
									return err
								}
							}
						}
						if err := b.Stop(scoped, previous); err != nil {
							return err
						}
					}
					if _, err := b.PrepareSecretMigrationCandidate(scoped); err != nil {
						return err
					}
					if err := b.BackupSecretMigrationSource(scoped, candidate); err != nil {
						return err
					}
				case lifecycle.PhaseMigrate:
					if _, err := b.PrepareSecretMigrationCandidate(scoped); err != nil {
						return err
					}
					if err := b.StartSecretMigrationCandidate(scoped, candidate); err != nil {
						return err
					}
					if protection != "" {
						if _, err := b.MigrationCandidateProtection(scoped, candidate, protection, passphrase); err != nil {
							return err
						}
						protection = ""
					}
					metadata, err := b.MigrationCandidateProtection(scoped, candidate, "status", nil)
					if err != nil {
						return err
					}
					var state struct {
						State string `json:"state"`
					}
					if json.Unmarshal(metadata, &state) != nil {
						return fmt.Errorf("invalid private candidate protection metadata")
					}
					status.Protection = state.State
					if state.State != "unlocked" {
						return nil
					}
					if err := b.TransferSecretMigrationSource(scoped, candidate); err != nil {
						return err
					}
				case lifecycle.PhaseSwitch:
					if err := b.Stop(scoped, candidate); err != nil {
						return err
					}
					if err := b.CommitSecretMigrationCandidate(scoped, candidate); err != nil {
						return err
					}
				case lifecycle.PhaseRestart, lifecycle.PhaseHealth:
					if _, err := b.RestartSecretMigration(scoped); err != nil {
						return err
					}
				default:
					return fmt.Errorf("unsupported original migration phase")
				}
			}
			return fmt.Errorf("original migration did not reach its next operator state")
		})
	})
	return status, err
}
