//go:build linux

package migration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	hostapplication "loki/internal/host/application"
	"loki/internal/host/lifecycle"
	secret "loki/modules/secrets"
)

// PrepareNativeCandidate derives inactive service resources from the ORIGINAL
// locked operation. Current contains the actually loaded pre-cutover catalog;
// the target catalog comes only from that operation's immutable plan.
func (o SourceOwner) PrepareNativeCandidate(current hostapplication.PrepareOptions, brokerHost, providerHost string) (NativeCandidate, error) {
	var candidate NativeCandidate
	r, err := o.current()
	if err != nil || r.SecretMigration.Intent.Mode != "native" || r.Phase != lifecycle.PhaseAdmitted && r.Phase != lifecycle.PhaseSnapshot && r.Phase != lifecycle.PhaseMigrate {
		return candidate, ErrSourceSession
	}
	i := r.SecretMigration.Intent
	if os.Geteuid() != 0 || current.UID != i.Before.UID || current.GID != i.Before.GID || current.AgentUID != i.Before.AgentUID || current.MetadataGID != i.Before.MetadataGID || current.IdentityFile != i.IdentityFile || current.StateRoot != i.StateRoot || current.ServiceStateRoot != i.ServiceStateRoot || current.Binary != i.Binary || current.Principal != "agent" || current.MigrationOperation != "" || current.InitialMaintenanceOperation != "" || i.RuntimeRoot != filepath.Join(filepath.Dir(current.RuntimeRoot), "secret-migration-"+i.CandidateGeneration) {
		return candidate, ErrSourceSession
	}
	previous, err := hostapplication.PolicyDigest(current)
	if err != nil || previous != i.ExpectedPolicy {
		return candidate, ErrSourceSession
	}
	before, err := hostapplication.ReadIdentity(i.IdentityFile)
	if err != nil || before != i.Before {
		return candidate, ErrSourceSession
	}
	if err := hostapplication.RequireEmptyGeneration(i.StateRoot, before); err != nil {
		return candidate, err
	}
	for expected, supplied := range map[string]string{i.Binary: providerHost, filepath.Join(filepath.Dir(i.Binary), "loki-secret-broker"): brokerHost} {
		actual, err := filepath.EvalSymlinks(expected)
		resolved, otherErr := filepath.EvalSymlinks(supplied)
		if err != nil || otherErr != nil || actual != resolved {
			return candidate, ErrSourceSession
		}
	}
	current.Resources = i.Plan.Resources
	current.RuntimeRoot = i.RuntimeRoot
	current.MigrationOperation = lifecycle.MigrationOperationUUID(r.ID)
	next, err := hostapplication.PolicyDigest(current)
	if err != nil || next != i.Policy {
		return candidate, ErrSourceSession
	}
	// The ordinary preparation unit is deliberately boot-fenced. Recreate
	// only its public runtime ancestor here after a reboot, without starting
	// normal services or weakening the original private candidate projection.
	parent := filepath.Dir(current.RuntimeRoot)
	created := false
	if err := os.Mkdir(parent, 0755); err == nil {
		created = true
	} else if !errors.Is(err, os.ErrExist) {
		return candidate, err
	}
	if err := nativeOwnedDirectory(parent, false); err != nil {
		return candidate, err
	}
	if created {
		if err := syncNativeDirectory(parent); err != nil {
			return candidate, err
		}
		if err := syncNativeDirectory(filepath.Dir(parent)); err != nil {
			return candidate, err
		}
	}
	preparation, err := hostapplication.PrepareCandidate(current, before, i.CandidateGeneration)
	if err != nil {
		return candidate, err
	}
	jail := filepath.Join(i.StateRoot, ".migration-root-"+strings.ReplaceAll(current.MigrationOperation, "-", ""))
	if err := os.Mkdir(jail, 0755); err != nil && !errors.Is(err, os.ErrExist) {
		return candidate, err
	}
	info, err := os.Lstat(jail)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 && info.Mode().Perm() != 0755 {
		return candidate, ErrSourceSession
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 || owner.Gid != 0 {
		return candidate, ErrSourceSession
	}
	// The host store ancestor stays root-private. Inside the service's chroot,
	// this empty directory becomes / and must be searchable by the broker UID.
	// Repair the earlier private leaf on original-operation recovery only.
	if info.Mode().Perm() != 0755 {
		if err := os.Chmod(jail, 0755); err != nil {
			return candidate, err
		}
	}
	if err := syncNativeDirectory(jail); err != nil {
		return candidate, err
	}
	if err := syncNativeDirectory(i.StateRoot); err != nil {
		return candidate, err
	}
	candidate = NativeCandidate{Operation: current.MigrationOperation, Preparation: preparation, RootDirectory: jail, BrokerHost: brokerHost, ProviderHost: providerHost}
	if !candidate.valid() {
		return NativeCandidate{}, ErrSourceSession
	}
	return candidate, nil
}

// FinishNativeSource applies the original terminal disposition only after its
// actual selector agrees. The caller still owns serving health and app/Job
// gate ACKs; this method never acknowledges the host operation's release.
func (o SourceOwner) FinishNativeSource(ctx context.Context) error {
	if os.Geteuid() != 0 || o.Journal == nil || lifecycle.MigrationOperationUUID(o.ID) == "" {
		return ErrSourceSession
	}
	r, found, err := o.Journal.Active()
	if err != nil || !found || r.ID != o.ID || r.Kind != lifecycle.OperationSecretMigration || r.SecretMigration == nil || r.SecretMigration.Intent.Mode != "native" || r.State != lifecycle.OperationSucceeded && r.State != lifecycle.OperationRolledBack {
		return ErrSourceSession
	}
	i := r.SecretMigration.Intent
	actual, err := hostapplication.ReadIdentity(i.IdentityFile)
	if err != nil {
		return err
	}
	want := i.Before
	if r.State == lifecycle.OperationSucceeded {
		want.Version, want.ActiveGeneration, want.PolicySHA256 = 2, i.CandidateGeneration, i.Policy
		if actual != want {
			return ErrSourceSession
		}
	} else {
		bound := want
		bound.Version, bound.PolicySHA256 = 2, i.ExpectedPolicy
		if actual != want && actual != bound {
			return ErrSourceSession
		}
	}
	if err := hostapplication.RequireExistingGeneration(i.StateRoot, actual); err != nil {
		return err
	}
	return (secret.Controller{StateDirectory: i.SourceRoot}).FinishApplicationMigration(ctx, i.Plan.Revision, lifecycle.MigrationOperationUUID(r.ID), r.State == lifecycle.OperationSucceeded)
}

// StopNativeCandidate reconstructs only stop authority from the original
// journal. It works after a reboot or lost preparation ACK without recreating
// a broker root, protection session or private import endpoint.
func (o SourceOwner) StopNativeCandidate(ctx context.Context) error {
	if os.Geteuid() != 0 || o.Journal == nil || lifecycle.MigrationOperationUUID(o.ID) == "" {
		return ErrSourceSession
	}
	r, found, err := o.Journal.Active()
	if err != nil || !found || r.ID != o.ID || r.Kind != lifecycle.OperationSecretMigration || r.SecretMigration == nil || r.SecretMigration.Intent.Mode != "native" {
		return ErrSourceSession
	}
	i := r.SecretMigration.Intent
	identity := i.Before
	identity.Version, identity.ActiveGeneration, identity.PolicySHA256 = 2, i.CandidateGeneration, i.Policy
	candidate := NativeCandidate{Operation: lifecycle.MigrationOperationUUID(r.ID), Preparation: hostapplication.Preparation{Identity: identity}, RootDirectory: filepath.Join(i.StateRoot, ".migration-root-"+r.ID)}
	return candidate.stopOwned(ctx)
}

// SelectNativeCandidate changes selector/policy only. Its caller must stop
// normal service writers under the original Job/app gates first, then publish
// the frozen catalog before acknowledging PhaseRestart. A lost selector ACK
// reuses this same operation and target without another private import.
func (o SourceOwner) SelectNativeCandidate(ctx context.Context, current hostapplication.PrepareOptions) (hostapplication.Identity, error) {
	var selected hostapplication.Identity
	if os.Geteuid() != 0 {
		return selected, ErrSourceSession
	}
	r, err := o.current()
	if err != nil || r.SecretMigration.Intent.Mode != "native" || r.Phase != lifecycle.PhaseSwitch {
		return selected, ErrSourceSession
	}
	i := r.SecretMigration.Intent
	if current.UID != i.Before.UID || current.GID != i.Before.GID || current.AgentUID != i.Before.AgentUID || current.MetadataGID != i.Before.MetadataGID || current.IdentityFile != i.IdentityFile || current.StateRoot != i.StateRoot || current.ServiceStateRoot != i.ServiceStateRoot || current.Binary != i.Binary || current.Principal != "agent" || current.MigrationOperation != "" || current.InitialMaintenanceOperation != "" || i.RuntimeRoot != filepath.Join(filepath.Dir(current.RuntimeRoot), "secret-migration-"+i.CandidateGeneration) {
		return selected, ErrSourceSession
	}
	policy, err := hostapplication.PolicyDigest(current)
	if err != nil || policy != i.ExpectedPolicy && policy != i.Policy {
		return selected, ErrSourceSession
	}
	if r.SecretMigration.Archive == nil || !r.SecretMigration.Archive.Published {
		return selected, ErrSourceSession
	}
	if err := lifecycle.VerifyMigrationSourceArchive(i.BackupPath, r.SecretMigration.Archive); err != nil {
		return selected, err
	}
	fence, err := (secret.Controller{StateDirectory: i.SourceRoot}).ApplicationMigrationState(ctx)
	if err != nil || fence == nil || fence.State != "pending" || fence.Operation != lifecycle.MigrationOperationUUID(r.ID) || fence.Revision != i.Plan.Revision {
		return selected, ErrSourceSession
	}
	if err := o.StopNativeCandidate(ctx); err != nil {
		return selected, err
	}
	if err := hostapplication.RequireEmptyGeneration(i.StateRoot, i.Before); err != nil {
		return selected, err
	}
	current.Resources, current.RuntimeRoot, current.MigrationOperation = i.Plan.Resources, i.RuntimeRoot, lifecycle.MigrationOperationUUID(r.ID)
	policy, err = hostapplication.PolicyDigest(current)
	if err != nil || policy != i.Policy {
		return selected, ErrSourceSession
	}
	return hostapplication.CommitCandidate(current, i.Before, i.CandidateGeneration)
}
