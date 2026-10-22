//go:build linux

package migration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"

	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	hostapplication "loki/internal/host/application"
	"loki/internal/host/lifecycle"
	secret "loki/modules/secrets"
)

type NativePrepareOptions struct {
	Application     hostapplication.PrepareOptions
	BeforeResources []controlpolicy.ApplicationResource
	Mappings        []appvalues.MigrationProfileMapping
	SourceRoot      string
	Recipient       string
	BackupPath      string
	Interrupt       bool
}

// PrepareNative observes the administrator-owned source without freezing it.
// The returned document contains complete mappings/revision and immutable role
// metadata, never values, source hashes, initialized keys or a host operation.
func PrepareNative(ctx context.Context, options NativePrepareOptions) (lifecycle.SecretMigrationIntent, error) {
	var intent lifecycle.SecretMigrationIntent
	if os.Geteuid() != 0 || options.Application.MigrationOperation != "" || options.Application.InitialMaintenanceOperation != "" {
		return intent, ErrSourceSession
	}
	recipient, err := age.ParseX25519Recipient(options.Recipient)
	if err != nil || recipient.String() != options.Recipient || !filepath.IsAbs(options.SourceRoot) || filepath.Clean(options.SourceRoot) != options.SourceRoot || options.SourceRoot == "/" || strings.ContainsAny(options.SourceRoot, "\x00\r\n") {
		return intent, ErrSourceSession
	}
	if err := lifecycle.ValidateMigrationBackupDestination(options.BackupPath); err != nil {
		return intent, err
	}
	application := options.Application
	if err := hostapplication.ValidateMetadata(application); err != nil {
		return intent, err
	}
	before, err := hostapplication.ReadIdentity(application.IdentityFile)
	if err != nil || before.UID != application.UID || before.GID != application.GID || before.AgentUID != application.AgentUID || before.MetadataGID != application.MetadataGID {
		return intent, ErrSourceSession
	}
	if err := hostapplication.RequireEmptyGeneration(application.StateRoot, before); err != nil {
		return intent, err
	}
	previousOptions := application
	previousOptions.Resources = options.BeforeResources
	previous, err := hostapplication.PolicyDigest(previousOptions)
	if err != nil || before.Version == 2 && before.PolicySHA256 != previous {
		return intent, ErrSourceSession
	}
	next, err := hostapplication.PolicyDigest(application)
	if err != nil {
		return intent, err
	}
	plan, err := (secret.Controller{StateDirectory: options.SourceRoot}).PrepareApplicationMigration(ctx, options.Mappings, application.Resources, application.Contract.Environment)
	if err != nil {
		return intent, err
	}
	var generation [16]byte
	if _, err := rand.Read(generation[:]); err != nil {
		return intent, err
	}
	id := hex.EncodeToString(generation[:])
	intent = lifecycle.SecretMigrationIntent{Plan: plan, Before: before, CandidateGeneration: id, ExpectedPolicy: previous, Policy: next, Mode: "native", SourceRoot: options.SourceRoot, IdentityFile: application.IdentityFile, StateRoot: application.StateRoot, ServiceStateRoot: application.ServiceStateRoot, RuntimeRoot: filepath.Join(filepath.Dir(application.RuntimeRoot), "secret-migration-"+id), Binary: application.Binary, Recipient: options.Recipient, BackupPath: options.BackupPath, Interrupt: options.Interrupt}
	application.RuntimeRoot, application.MigrationOperation = intent.RuntimeRoot, lifecycle.MigrationOperationUUID(id)
	if err := hostapplication.ValidateMetadata(application); err != nil {
		return lifecycle.SecretMigrationIntent{}, err
	}
	if !intent.Valid() {
		return lifecycle.SecretMigrationIntent{}, ErrSourceSession
	}
	return intent, nil
}
