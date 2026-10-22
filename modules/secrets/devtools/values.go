package devtools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

var editRevision = regexp.MustCompile(`^[0-9a-f]{64}$`)
var environment = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`)

type ValueMetadata struct {
	Name      string `json:"key"`
	Kind      string `json:"kind"`
	Source    string `json:"source"`
	Overrides bool   `json:"overrides"`
}

type Metadata struct {
	Profile           string          `json:"profile"`
	Items             []ValueMetadata `json:"items"`
	IntegrityVerified bool            `json:"integrity_verified"`
}

type ChangeItem struct {
	Name   string `json:"key"`
	Kind   string `json:"kind"`
	Action string `json:"action"`
}

type ChangeResult struct {
	Profile    string       `json:"profile"`
	Changed    bool         `json:"changed"`
	Revision   string       `json:"revision"`
	Replayed   bool         `json:"replayed"`
	Applicable bool         `json:"applicable"`
	Items      []ChangeItem `json:"items"`
}

type SavedRequest struct {
	RequestID string        `json:"request_id"`
	State     string        `json:"state"`
	Result    *ChangeResult `json:"result,omitempty"`
}

type InputInspection struct {
	Values struct {
		State   string `json:"state"`
		Reason  string `json:"reason"`
		Current *struct {
			IntegrityVerified bool `json:"integrity_verified"`
		} `json:"current"`
	} `json:"values"`
}

type ProtectionStatus struct {
	State         string     `json:"state"`
	IdleExpiresAt *time.Time `json:"idle_expires_at,omitempty"`
}

func scopeArgs(app, env string) ([]string, error) {
	if !profile.MatchString(app) || env != "" && env != "common" && !environment.MatchString(env) {
		return nil, ErrUnavailable
	}
	args := []string{"--profile", app}
	if env != "" && env != "common" {
		args = append(args, "--env", env)
	}
	return args, nil
}

func guardedArgs(app, env, revision, request string) ([]string, error) {
	args, err := scopeArgs(app, env)
	if err != nil || !editRevision.MatchString(revision) || !uuid.MatchString(request) {
		return nil, ErrUnavailable
	}
	return append(args, "--revision", revision, "--request-id", request), nil
}

func (p *Provider) Revision(ctx context.Context, app, env string) (string, error) {
	args, err := scopeArgs(app, env)
	if err != nil {
		return "", err
	}
	var result struct {
		Profile  string `json:"profile"`
		Revision string `json:"revision"`
	}
	if err := p.invoke(ctx, append([]string{"values", "revision"}, args...), nil, &result); err != nil {
		return "", err
	}
	if result.Profile != app || !editRevision.MatchString(result.Revision) {
		return "", ErrUnavailable
	}
	return result.Revision, nil
}

func (p *Provider) Metadata(ctx context.Context, app, env, kind string) (Metadata, error) {
	args, err := scopeArgs(app, env)
	if err != nil {
		return Metadata{}, err
	}
	command := "secret"
	if kind == "variable" {
		command = "var"
	} else if kind != "secret" {
		return Metadata{}, ErrUnavailable
	}
	var result Metadata
	err = p.invoke(ctx, append([]string{command, "list"}, args...), nil, &result)
	if err == nil && result.Profile != app {
		return Metadata{}, ErrUnavailable
	}
	return result, err
}

func (p *Provider) InspectInput(ctx context.Context, app, env, bundle string) (InputInspection, error) {
	args, err := scopeArgs(app, env)
	if err != nil {
		return InputInspection{}, err
	}
	if bundle != "" {
		if !key.MatchString(bundle) && !profileBundle.MatchString(bundle) {
			return InputInspection{}, ErrUnavailable
		}
		args = append(args, "--bundle", bundle)
	}
	var result InputInspection
	err = p.invoke(ctx, append([]string{"values", "inspect"}, args...), nil, &result)
	return result, err
}

var profileBundle = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func (p *Provider) ProtectionStatus(ctx context.Context) (ProtectionStatus, error) {
	var result struct {
		Item ProtectionStatus `json:"item"`
	}
	err := p.invoke(ctx, []string{"vault", "status"}, nil, &result)
	if err == nil {
		switch result.Item.State {
		case "uninitialized", "locked", "unlocked", "locking", "locked_manual", "recovery_required":
		default:
			return ProtectionStatus{}, ErrUnavailable
		}
	}
	return result.Item, err
}

func (p *Provider) Request(ctx context.Context, app, request string) (SavedRequest, error) {
	if !profile.MatchString(app) || !uuid.MatchString(request) {
		return SavedRequest{}, ErrUnavailable
	}
	var result struct {
		Profile string       `json:"profile"`
		Item    SavedRequest `json:"item"`
	}
	err := p.invoke(ctx, []string{"values", "request", "show", request, "--profile", app}, nil, &result)
	if err == nil && (result.Profile != app || result.Item.RequestID != request || result.Item.Result != nil && result.Item.Result.Profile != app) {
		return SavedRequest{}, ErrUnavailable
	}
	return result.Item, err
}

func (p *Provider) SetPublic(ctx context.Context, app, env, name, value, revision, request string) (ChangeResult, error) {
	args, err := guardedArgs(app, env, revision, request)
	if err != nil || !key.MatchString(name) {
		return ChangeResult{}, ErrUnavailable
	}
	var result ChangeResult
	err = p.valueMutation(ctx, app, append([]string{"var", "set", name, "--value", value}, args...), nil, &result)
	return result, err
}

func (p *Provider) Generate(ctx context.Context, app, env, name, revision, request string) (ChangeResult, error) {
	args, err := guardedArgs(app, env, revision, request)
	if err != nil || !key.MatchString(name) {
		return ChangeResult{}, ErrUnavailable
	}
	var result ChangeResult
	err = p.valueMutation(ctx, app, append([]string{"secret", "generate", name}, args...), nil, &result)
	return result, err
}

// SetSecret is reachable only through private administrator/migration input.
func (p *Provider) SetSecret(ctx context.Context, app, env, name string, body []byte, revision, request string, replace bool) (ChangeResult, error) {
	args, err := guardedArgs(app, env, revision, request)
	if err != nil || !key.MatchString(name) || len(body) > 65536 {
		return ChangeResult{}, ErrUnavailable
	}
	var result ChangeResult
	if !replace {
		args = append(args, "--create-only")
	}
	err = p.valueMutation(ctx, app, append([]string{"secret", "set", name, "--stdin"}, args...), body, &result)
	return result, err
}

func (p *Provider) Unset(ctx context.Context, app, env, kind, name, revision, request string) (ChangeResult, error) {
	args, err := guardedArgs(app, env, revision, request)
	if err != nil || !key.MatchString(name) {
		return ChangeResult{}, ErrUnavailable
	}
	command := "secret"
	if kind == "variable" {
		command = "var"
	} else if kind != "secret" {
		return ChangeResult{}, ErrUnavailable
	}
	var result ChangeResult
	err = p.valueMutation(ctx, app, append([]string{command, "unset", name}, args...), nil, &result)
	return result, err
}

func (p *Provider) Import(ctx context.Context, app, env, path, revision, request, bundle string, variables []string, preview bool) (ChangeResult, error) {
	args, err := guardedArgs(app, env, revision, request)
	if err != nil || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ChangeResult{}, ErrUnavailable
	}
	args = append([]string{"import", "--file", path}, args...)
	if bundle != "" {
		if !profileBundle.MatchString(bundle) {
			return ChangeResult{}, ErrUnavailable
		}
		args = append(args, "--bundle", bundle)
	}
	names := append([]string(nil), variables...)
	sort.Strings(names)
	for i, name := range names {
		if !key.MatchString(name) || i > 0 && names[i-1] == name {
			return ChangeResult{}, ErrUnavailable
		}
		args = append(args, "--var", name)
	}
	if preview {
		args = append(args, "--dry-run")
	}
	var result ChangeResult
	err = p.valueMutation(ctx, app, args, nil, &result)
	return result, err
}

func (p *Provider) Environment(ctx context.Context, app, name, operation, revision, request string) (ChangeResult, error) {
	if !environment.MatchString(name) || name == "common" || operation != "create" && operation != "remove" {
		return ChangeResult{}, ErrUnavailable
	}
	args, err := guardedArgs(app, "", revision, request)
	if err != nil {
		return ChangeResult{}, err
	}
	var result ChangeResult
	err = p.valueMutation(ctx, app, append([]string{"env", operation, name}, args...), nil, &result)
	return result, err
}

func (p *Provider) Bundle(ctx context.Context, app, name, revision, request string, members []string) (ChangeResult, error) {
	if !profileBundle.MatchString(name) {
		return ChangeResult{}, ErrUnavailable
	}
	args, err := guardedArgs(app, "", revision, request)
	if err != nil {
		return ChangeResult{}, err
	}
	args = append([]string{"bundle", "set", name}, args...)
	names := append([]string(nil), members...)
	sort.Strings(names)
	for i, name := range names {
		if !key.MatchString(name) || i > 0 && names[i-1] == name {
			return ChangeResult{}, ErrUnavailable
		}
		args = append(args, "--key", name)
	}
	var result ChangeResult
	err = p.valueMutation(ctx, app, args, nil, &result)
	return result, err
}

func (p *Provider) valueMutation(ctx context.Context, app string, args []string, body []byte, target *ChangeResult) error {
	var raw json.RawMessage
	if err := p.invoke(ctx, args, body, &raw); err != nil {
		return err
	}
	defer clear(raw)
	if json.Unmarshal(raw, target) != nil || target.Profile != app || !editRevision.MatchString(target.Revision) {
		return ErrUnavailable
	}
	return nil
}
