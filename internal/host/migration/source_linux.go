//go:build linux

// Package migration owns the offline source-owner side of host value migration.
// In managed deployments it runs in the owned root administrator helper. Source
// bytes go directly to the broker's private channel; the host journal receives
// only immutable write/delivery metadata and match counts.
package migration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"loki/internal/admin"
	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/host/lifecycle"
	"loki/internal/platform/privateio"
	secret "loki/modules/secrets"
)

var ErrSourceSession = errors.New("original migration source session did not complete")

// SourceEvent never contains value bytes or plaintext hashes. Its synchronous
// checkpoint is acknowledged before the corresponding provider effect/approval.
type SourceEvent struct {
	Operation string                                `json:"operation"`
	Type      string                                `json:"type"`
	Write     *lifecycle.MigrationWriteCheckpoint   `json:"write,omitempty"`
	Binding   *appvalues.MigrationComparisonBinding `json:"binding,omitempty"`
	Attempt   string                                `json:"attempt,omitempty"`
	Count     int                                   `json:"count,omitempty"`
}

type SourceSession struct {
	Source          secret.Controller
	Target          admin.ApplicationClient
	Operation       string
	Policy          string
	Plan            appvalues.MigrationPlan
	BaseEnvironment map[string]string
	Writes          []lifecycle.MigrationWriteCheckpoint
	Checkpoint      func(SourceEvent) error
}

func validDigest(value string) bool {
	b, err := hex.DecodeString(value)
	return err == nil && len(b) == 32 && value == hex.EncodeToString(b)
}

func newAttempt() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	nonce[6] = nonce[6]&15 | 64
	nonce[8] = nonce[8]&63 | 128
	return lifecycle.MigrationOperationUUID(hex.EncodeToString(nonce[:])), nil
}

func (s SourceSession) emit(event SourceEvent) error {
	event.Operation = s.Operation
	return s.Checkpoint(event)
}

func (s SourceSession) revision(ctx context.Context, resource controlpolicy.ApplicationResource, environment string) (string, error) {
	raw, err := s.Target.Call(ctx, appvalues.AdminRequest{Action: "revision", Resource: resource.Alias, Environment: environment}, nil)
	if err != nil {
		return "", err
	}
	var result struct {
		Revision string `json:"revision"`
	}
	if json.Unmarshal(raw, &result) != nil || !validDigest(result.Revision) {
		return "", ErrSourceSession
	}
	return result.Revision, nil
}

func (s SourceSession) write(ctx context.Context, resource controlpolicy.ApplicationResource, environment, action, name string, body []byte, members []string) error {
	w := lifecycle.MigrationWriteCheckpoint{Resource: resource.ID, Environment: environment, Action: action, Name: name}
	w.Request = lifecycle.MigrationWriteRequest(strings.ReplaceAll(s.Operation, "-", ""), w)
	found := false
	for _, old := range s.Writes {
		if old.Request != w.Request {
			continue
		}
		if old.Resource != w.Resource || old.Environment != w.Environment || old.Action != w.Action || old.Name != w.Name || !validDigest(old.Revision) {
			return ErrSourceSession
		}
		if old.Complete {
			return nil
		}
		w.Revision, found = old.Revision, true
		break
	}
	if !found {
		revisionEnvironment := environment
		if action == "env_create" || action == "bundle_set" {
			revisionEnvironment = "common"
		}
		var err error
		w.Revision, err = s.revision(ctx, resource, revisionEnvironment)
		if err != nil {
			return err
		}
	}
	checkpoint := w
	if err := s.emit(SourceEvent{Type: "write_prepared", Write: &checkpoint}); err != nil {
		return err
	}
	q := appvalues.AdminRequest{Action: action, Resource: resource.Alias, Environment: environment, Name: name, Revision: w.Revision, RequestID: w.Request, Members: members}
	if action == "bundle_set" {
		q.Name = ""
		q.Environment = ""
	}
	if (action == "set_secret" || action == "set_public") && len(body) == 0 {
		q.Empty = true
	}
	raw, err := s.Target.Call(ctx, q, body)
	if err != nil {
		return err
	}
	var result struct {
		Action   string `json:"action"`
		Resource string `json:"resource"`
		Revision string `json:"revision"`
		Request  string `json:"request_id"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Action != action || result.Resource != resource.Alias || result.Request != w.Request || !validDigest(result.Revision) {
		return ErrSourceSession
	}
	checkpoint.Complete = true
	return s.emit(SourceEvent{Type: "write_completed", Write: &checkpoint})
}

func (s SourceSession) compare(ctx context.Context, resource controlpolicy.ApplicationResource, environment string, values []privateio.MigrationExpectedValue) error {
	attempt, err := newAttempt()
	if err != nil {
		return err
	}
	q := appvalues.AdminRequest{Action: "migration_compare", Resource: resource.Alias, Environment: environment, Revision: s.Policy, RequestID: attempt}
	result, err := s.Target.CompareMigration(ctx, s.Operation, q, values, func(binding appvalues.MigrationComparisonBinding) error {
		return s.emit(SourceEvent{Type: "comparison_prepared", Binding: &binding})
	})
	if err != nil {
		return err
	}
	if !result.Matched || result.Count != len(values) {
		return ErrSourceSession
	}
	return s.emit(SourceEvent{Type: "comparison_completed", Attempt: attempt, Count: result.Count})
}

// Transfer rereads the exact fenced source revision and revalidates every target
// value through approved private delivery on every recovery invocation. Saved
// completed writes are not repeated, and pending writes reuse their ORIGINAL
// revision/UUID. A missing/replaced target therefore cannot reuse old matches.
func (s SourceSession) Transfer(ctx context.Context) error {
	if !controlpolicy.ApplicationUUID.MatchString(s.Operation) || !validDigest(s.Policy) || s.Checkpoint == nil {
		return ErrSourceSession
	}
	return s.Source.WithApplicationMigrationValues(ctx, s.Operation, s.Plan, s.BaseEnvironment, func(values []secret.MigrationValue) error {
		resources := make(map[string]controlpolicy.ApplicationResource, len(s.Plan.Resources))
		for _, resource := range s.Plan.Resources {
			resources[resource.ID] = resource
			for _, env := range resource.Environments {
				if env != "common" {
					if err := s.write(ctx, resource, env, "env_create", "", nil, nil); err != nil {
						return err
					}
				}
			}
		}
		for _, value := range values {
			resource, exists := resources[value.Resource]
			if !exists {
				return ErrSourceSession
			}
			action := "set_secret"
			if value.Kind == "variable" {
				action = "set_public"
			}
			if err := s.write(ctx, resource, value.Environment, action, value.Name, value.Bytes(), nil); err != nil {
				return err
			}
		}
		for _, resource := range s.Plan.Resources {
			if resource.Bundle == "" {
				continue
			}
			members := make([]string, 0, len(resource.Keys))
			for key := range resource.Keys {
				members = append(members, key)
			}
			sort.Strings(members)
			if err := s.write(ctx, resource, "common", "bundle_set", resource.Bundle, nil, members); err != nil {
				return err
			}
		}
		for start := 0; start < len(values); {
			resource := resources[values[start].Resource]
			env := values[start].Environment
			batch := []privateio.MigrationExpectedValue{}
			end := start
			for end < len(values) && values[end].Resource == resource.ID && values[end].Environment == env {
				v := values[end]
				candidate := append(batch, privateio.MigrationExpectedValue{Name: v.Name, Value: v.Bytes()})
				encoded, err := privateio.EncodeMigrationExpected(candidate)
				clear(encoded)
				if err != nil {
					if !errors.Is(err, privateio.ErrLimit) || len(batch) == 0 {
						return err
					}
					break
				}
				batch = candidate
				end++
			}
			if err := s.compare(ctx, resource, env, batch); err != nil {
				return err
			}
			start = end
		}
		return nil
	})
}
