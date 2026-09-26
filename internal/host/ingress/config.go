// Package ingress owns operator-managed MCP ingress Host allowlisting.
package ingress

import (
	"errors"

	"github.com/pelletier/go-toml/v2"

	"loki/internal/host/lifecycle"
)

const (
	FileName       = "ingress.toml"
	MaxPublicHosts = 64
)

type Config struct {
	PublicHosts []string `toml:"public_hosts"`
}

func Normalize(hosts []string) ([]string, error) {
	return lifecycle.NormalizeIngressHosts(hosts)
}

func Render(hosts []string) ([]byte, error) {
	normalized, err := Normalize(hosts)
	if err != nil {
		return nil, err
	}
	raw, err := toml.Marshal(Config{PublicHosts: normalized})
	if err != nil {
		return nil, errors.New("cannot encode MCP ingress configuration")
	}
	return raw, nil
}
