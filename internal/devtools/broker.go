package devtools

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"sort"
	"strings"
)

var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type SecretEnvironmentResolver func(context.Context, string, []string) ([]string, []string, error)

type Broker struct {
	Client         *Client
	ResolveSecrets SecretEnvironmentResolver
}

func (b Broker) Call(ctx context.Context, name string, input json.RawMessage, profile string, secretNames []string) (json.RawMessage, error) {
	if b.Client == nil {
		return nil, errors.New("devtools client is unavailable")
	}
	if len(secretNames) == 0 {
		return nil, errors.New("devtools broker requires selected encrypted secrets")
	}
	if name != "process start" && name != "process restart" {
		return nil, errors.New("secret injection is unavailable for this devtools command")
	}
	if b.ResolveSecrets == nil {
		return nil, errors.New("devtools secret resolver is unavailable")
	}
	entries, private, err := b.ResolveSecrets(ctx, profile, secretNames)
	if err != nil {
		return nil, err
	}
	environment, err := mergeEnvironment(b.Client.Env, entries)
	if err != nil {
		return nil, err
	}
	result, err := b.Client.call(ctx, name, input, environment)
	if err != nil {
		message := err.Error()
		for _, value := range private {
			if strings.Contains(message, value) {
				return nil, errors.New("devtools command failed with private output")
			}
		}
		return nil, err
	}
	if containsPrivateJSON(result, private) {
		return nil, errors.New("devtools response contained private output")
	}
	return result, nil
}

func containsPrivateJSON(raw json.RawMessage, private []string) bool {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return true
	}
	var contains func(any) bool
	contains = func(current any) bool {
		switch typed := current.(type) {
		case string:
			for _, secretValue := range private {
				if strings.Contains(typed, secretValue) {
					return true
				}
			}
		case []any:
			for _, item := range typed {
				if contains(item) {
					return true
				}
			}
		case map[string]any:
			for key, item := range typed {
				if contains(key) || contains(item) {
					return true
				}
			}
		}
		return false
	}
	return contains(value)
}

func mergeEnvironment(base, extra []string) ([]string, error) {
	values := make(map[string]string, len(base)+len(extra))
	for _, entry := range append(slices.Clone(base), extra...) {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || !environmentName.MatchString(name) || strings.ContainsRune(value, 0) {
			return nil, errors.New("invalid process environment")
		}
		values[name] = value
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	environment := make([]string, 0, len(names))
	for _, name := range names {
		environment = append(environment, name+"="+values[name])
	}
	return environment, nil
}
