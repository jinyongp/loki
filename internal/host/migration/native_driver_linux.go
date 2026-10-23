//go:build linux

package migration

import (
	"context"
	"time"

	"loki/internal/control/appvalues"
	hostapplication "loki/internal/host/application"
	"loki/internal/host/lifecycle"
)

// NativeServices is the installed suite's finite service boundary. The driver
// retains source/import/selector authority; this owner handles the original Job
// gate, ordinary services, public catalog/roles, readiness and gate release.
type NativeServices interface {
	Freeze(context.Context, lifecycle.OperationRecord) error
	PrepareServing(context.Context, lifecycle.OperationRecord, bool) error
	StartServing(context.Context, lifecycle.OperationRecord) error
	Health(context.Context, lifecycle.OperationRecord) error
	Release(context.Context, lifecycle.OperationRecord) error
}

type NativeProgress struct {
	Operation  string                   `json:"operation_id"`
	State      lifecycle.OperationState `json:"state"`
	Phase      lifecycle.OperationPhase `json:"phase"`
	Generation string                   `json:"candidate_generation"`
	Protection string                   `json:"protection,omitempty"`
	Released   bool                     `json:"released"`
	Rollback   bool                     `json:"rollback_requested"`
}

func nativeProgress(r lifecycle.OperationRecord) NativeProgress {
	return NativeProgress{Operation: lifecycle.MigrationOperationUUID(r.ID), State: r.State, Phase: r.Phase, Generation: r.SecretMigration.Intent.CandidateGeneration, Released: r.Maintenance.Released, Rollback: r.SecretMigration.RollbackRequested}
}

// RequestNativeRollback persists direction only after checking the actual
// selector. A selector rename whose checkpoint was lost is already cut over.
func (o SourceOwner) RequestNativeRollback() error {
	r, err := o.nativeBootRecord()
	if err != nil {
		return err
	}
	actual, err := hostapplication.ReadIdentity(r.SecretMigration.Intent.IdentityFile)
	if err != nil || actual != r.SecretMigration.Intent.Before {
		return ErrSourceSession
	}
	_, err = o.Journal.RequestSecretMigrationRollback(r.ID, time.Now())
	return err
}

// RunNative resumes the original journal through its next operator boundary.
// Locked/uninitialized is a successful pending result, never an auto-init or
// a substitute generation. An ordinary serving restart always loses sessions.
func (o SourceOwner) RunNative(ctx context.Context, current hostapplication.PrepareOptions, services NativeServices, broker, provider, protection string, passphrase []byte) (NativeProgress, error) {
	var result NativeProgress
	if services == nil || protection != "" && protection != "init" && protection != "unlock" || (protection != "") != (len(passphrase) > 0) || len(passphrase) > 65536 {
		return result, ErrSourceSession
	}
	for step := 0; step < 12; step++ {
		r, err := o.nativeBootRecord()
		if err != nil {
			return result, err
		}
		result = nativeProgress(r)
		i := r.SecretMigration.Intent
		policy, policyErr := hostapplication.PolicyDigest(current)
		if policyErr != nil || policy != i.ExpectedPolicy && policy != i.Policy || current.IdentityFile != i.IdentityFile || current.StateRoot != i.StateRoot || current.ServiceStateRoot != i.ServiceStateRoot || current.Binary != i.Binary || current.UID != i.Before.UID || current.GID != i.Before.GID || current.AgentUID != i.Before.AgentUID || current.MetadataGID != i.Before.MetadataGID {
			return result, ErrSourceSession
		}
		if r.Maintenance.Released {
			return result, nil
		}
		if r.State.Terminal() {
			if err := o.FenceNativeBoot(ctx); err != nil {
				return result, err
			}
			if err := o.StopNativeCandidate(ctx); err != nil {
				return result, err
			}
			if err := services.PrepareServing(ctx, r, r.State == lifecycle.OperationRolledBack); err != nil {
				return result, err
			}
			if err := o.PermitNativeStart(); err != nil {
				return result, err
			}
			if err := services.StartServing(ctx, r); err != nil {
				return result, err
			}
			if err := services.Health(ctx, r); err != nil {
				return result, err
			}
			if err := o.FinishNativeSource(ctx); err != nil {
				return result, err
			}
			if err := services.Release(ctx, r); err != nil {
				return result, err
			}
			if err := o.ReleaseNativeBoot(ctx); err != nil {
				return result, err
			}
			if _, err := o.Journal.AcknowledgeMigrationRelease(r.ID, time.Now()); err != nil {
				return result, err
			}
			result.Released = true
			return result, nil
		}
		if r.SecretMigration.RollbackRequested {
			if err := o.FenceNativeBoot(ctx); err != nil {
				return result, err
			}
			if err := services.Freeze(ctx, r); err != nil {
				return result, err
			}
			if err := o.RequestNativeRollback(); err != nil {
				return result, err
			}
			if err := o.StopNativeCandidate(ctx); err != nil {
				return result, err
			}
			if err := services.PrepareServing(ctx, r, true); err != nil {
				return result, err
			}
			if err := o.PermitNativeStart(); err != nil {
				return result, err
			}
			if err := services.StartServing(ctx, r); err != nil {
				return result, err
			}
			if err := services.Health(ctx, r); err != nil {
				return result, err
			}
			if _, err := o.Journal.CompleteSecretMigration(r.ID, true, time.Now()); err != nil {
				return result, err
			}
			continue
		}
		switch r.Phase {
		case lifecycle.PhaseAdmitted, lifecycle.PhaseSnapshot:
			if err := o.FenceNativeBoot(ctx); err != nil {
				return result, err
			}
			if err := services.Freeze(ctx, r); err != nil {
				return result, err
			}
			if err := o.Backup(ctx); err != nil {
				return result, err
			}
		case lifecycle.PhaseMigrate:
			candidate, err := o.PrepareNativeCandidate(current, broker, provider)
			if err != nil {
				return result, err
			}
			if err := candidate.Start(ctx); err != nil {
				return result, err
			}
			if protection != "" {
				if _, err := candidate.Client().Call(ctx, appvalues.AdminRequest{Action: "protection_" + protection}, passphrase); err != nil {
					return result, err
				}
				protection = ""
			}
			result.Protection, err = candidate.Status(ctx)
			if err != nil || result.Protection != "unlocked" {
				return result, err
			}
			if err := o.Transfer(ctx, candidate.Client(), current.Contract.Environment); err != nil {
				return result, err
			}
		case lifecycle.PhaseSwitch:
			if _, err := o.SelectNativeCandidate(ctx, current); err != nil {
				return result, err
			}
			if err := services.PrepareServing(ctx, r, false); err != nil {
				return result, err
			}
			if _, err := o.Journal.AdvanceSecretMigration(r.ID, lifecycle.PhaseRestart, time.Now()); err != nil {
				return result, err
			}
		case lifecycle.PhaseRestart, lifecycle.PhaseHealth:
			if err := services.PrepareServing(ctx, r, false); err != nil {
				return result, err
			}
			if err := o.PermitNativeStart(); err != nil {
				return result, err
			}
			if err := services.StartServing(ctx, r); err != nil {
				return result, err
			}
			if err := services.Health(ctx, r); err != nil {
				return result, err
			}
			if r.Phase == lifecycle.PhaseRestart {
				if _, err := o.Journal.AdvanceSecretMigration(r.ID, lifecycle.PhaseHealth, time.Now()); err != nil {
					return result, err
				}
			}
			if _, err := o.Journal.CompleteSecretMigration(r.ID, false, time.Now()); err != nil {
				return result, err
			}
		default:
			return result, ErrSourceSession
		}
	}
	return result, ErrSourceSession
}
