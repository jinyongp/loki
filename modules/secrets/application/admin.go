//go:build linux

package application

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/fault"
	provider "loki/modules/secrets/devtools"
)

// AdminRequest is private control metadata. Secret bytes use the following
// bounded binary frame, never this JSON object or a command-line argument.
type AdminRequest = appvalues.AdminRequest

var adminUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func (s *Service) Admin(ctx context.Context, q AdminRequest, body []byte) (map[string]any, error) {
	if strings.HasPrefix(q.Action, "protection_") {
		return s.protection(ctx, q, body)
	}
	if q.Action == "revision" {
		if len(body) != 0 || q.Revision != "" || q.RequestID != "" || q.Name != "" || q.Principal != "" || q.Members != nil || q.Variables != nil || q.Empty || q.Replace {
			return nil, invalid()
		}
		for _, r := range s.options.Resources {
			if r.Alias == q.Resource {
				env := q.Environment
				if env == "" {
					env = "common"
				}
				allowed := false
				for _, e := range r.Environments {
					if env == e {
						allowed = true
					}
				}
				if !allowed {
					return nil, denied()
				}
				revision, err := s.options.Provider.Revision(ctx, r.Profile(), env)
				if err != nil {
					return nil, safeError(err)
				}
				return map[string]any{"action": "revision", "resource": r.Alias, "env": env, "revision": revision}, nil
			}
		}
		return nil, denied()
	}
	if q.Empty && (q.Action != "set_secret" || len(body) != 0) {
		return nil, invalid()
	}
	if q.Replace && q.Action != "set_secret" {
		return nil, invalid()
	}
	if q.Action == "stage" {
		if q.Revision != "" || q.RequestID != "" || q.Name != "" || q.Members != nil {
			return nil, invalid()
		}
		return s.Stage(ctx, StageRequest{Resource: q.Resource, Environment: q.Environment, Principal: q.Principal, Variables: q.Variables}, body)
	}
	if q.Principal != "" || q.Variables != nil || !revisionPattern.MatchString(q.Revision) || !adminUUID.MatchString(q.RequestID) {
		return nil, invalid()
	}
	var resource *controlpolicy.ApplicationResource
	for _, r := range s.options.Resources {
		if r.Alias == q.Resource {
			resource = &r
			break
		}
	}
	if resource == nil {
		return nil, denied()
	}
	r := *resource
	s.mu.Lock()
	defer s.mu.Unlock()
	action := q.Action
	if q.Replace {
		action += "_replace"
	}
	request := appvalues.Request{Action: action, Environment: &q.Environment, ExpectedRevision: &q.Revision, RequestID: &q.RequestID}
	if q.Environment == "" {
		request.Environment = nil
	}
	if q.Name != "" {
		request.Name = &q.Name
	}
	created, err := s.ensureLink("host-admin", r, request)
	if err != nil {
		return nil, err
	}
	if created {
		if _, e := s.options.Provider.Request(ctx, r.Profile(), q.RequestID); e == nil {
			return nil, conflict()
		} else {
			var failure *provider.Failure
			if !errors.As(e, &failure) || failure.Code != "request_not_found" {
				return nil, safeError(e)
			}
		}
	}
	var result provider.ChangeResult
	switch q.Action {
	case "env_create", "env_remove":
		if len(body) != 0 || q.Name != "" || q.Members != nil || q.Environment == "common" {
			return nil, invalid()
		}
		allowed := false
		for _, env := range r.Environments {
			if env == q.Environment {
				allowed = true
			}
		}
		if !allowed {
			return nil, denied()
		}
		op := "create"
		if q.Action == "env_remove" {
			op = "remove"
		}
		result, err = s.options.Provider.Environment(ctx, r.Profile(), q.Environment, op, q.Revision, q.RequestID)
	case "bundle_set":
		if len(body) != 0 || q.Name != "" || q.Environment != "" || r.Bundle == "" || len(q.Members) == 0 || len(q.Members) > controlpolicy.MaxApplicationKeys {
			return nil, invalid()
		}
		for _, name := range q.Members {
			if _, ok := r.Keys[name]; !ok {
				return nil, denied()
			}
		}
		result, err = s.options.Provider.Bundle(ctx, r.Profile(), r.Bundle, q.Revision, q.RequestID, q.Members)
	case "set_secret":
		if q.Members != nil || len(body) > 65536 || r.Keys[q.Name] != "secret" || controlpolicy.ReservedApplicationKey(q.Name, s.options.BaseEnvironment) {
			return nil, denied()
		}
		env := q.Environment
		if env == "" {
			env = "common"
		}
		allowed := false
		for _, e := range r.Environments {
			if env == e {
				allowed = true
			}
		}
		if !allowed {
			return nil, denied()
		}
		result, err = s.options.Provider.SetSecret(ctx, r.Profile(), env, q.Name, body, q.Revision, q.RequestID, q.Replace)
	default:
		return nil, invalid()
	}
	if err != nil {
		return nil, safeError(err)
	}
	if !revisionPattern.MatchString(result.Revision) {
		return nil, unavailable()
	}
	code := "success"
	if result.Replayed {
		code = "replayed"
	}
	if err := s.record("host-admin", r, q.Action, q.RequestID, code); err != nil {
		return nil, err
	}
	return map[string]any{"action": q.Action, "resource": r.Alias, "revision": result.Revision, "request_id": q.RequestID, "changed": result.Changed, "replayed": result.Replayed}, nil
}

func (s *Service) protection(ctx context.Context, q AdminRequest, body []byte) (map[string]any, error) {
	if q.Resource != "" || q.Environment != "" || q.Principal != "" || q.Variables != nil || q.Members != nil || q.Revision != "" || q.RequestID != "" || q.Name != "" || q.Empty || q.Replace {
		return nil, invalid()
	}
	action := strings.TrimPrefix(q.Action, "protection_")
	switch action {
	case "status", "init", "unlock", "lock", "recover":
	default:
		return nil, invalid()
	}
	if action == "status" {
		if len(body) != 0 {
			return nil, invalid()
		}
		result, err := s.options.Provider.ProtectionStatus(ctx)
		if err != nil {
			return nil, safeError(err)
		}
		return map[string]any{"action": "status", "state": result.State}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.options.Provider.Protection(ctx, action, body)
	code := "success"
	if err != nil {
		code = string(fault.Describe(safeError(err)).Code)
	}
	if s.audit.Append(map[string]any{"at": time.Now().UTC().Format(time.RFC3339Nano), "principal": "host-admin", "scope": "protection", "action": q.Action, "result": code}) != nil {
		return nil, unavailable()
	}
	if err != nil {
		return nil, safeError(err)
	}
	return map[string]any{"action": action, "state": result.Item.State, "changed": result.Changed}, nil
}
