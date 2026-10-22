//go:build linux

package migration

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	hostapplication "loki/internal/host/application"
	"loki/internal/host/lifecycle"
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
	preparation, err := hostapplication.PrepareCandidate(current, before, i.CandidateGeneration)
	if err != nil {
		return candidate, err
	}
	jail := filepath.Join(i.StateRoot, ".migration-root-"+strings.ReplaceAll(current.MigrationOperation, "-", ""))
	if err := os.Mkdir(jail, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return candidate, err
	}
	info, err := os.Lstat(jail)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return candidate, ErrSourceSession
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 || owner.Gid != 0 {
		return candidate, ErrSourceSession
	}
	candidate = NativeCandidate{Operation: current.MigrationOperation, Preparation: preparation, RootDirectory: jail, BrokerHost: brokerHost, ProviderHost: providerHost}
	if !candidate.valid() {
		return NativeCandidate{}, ErrSourceSession
	}
	return candidate, nil
}
