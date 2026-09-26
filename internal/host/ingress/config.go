// Package ingress owns operator-managed MCP ingress Host allowlisting.
package ingress

import (
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	FileName       = "ingress.toml"
	MaxPublicHosts = 64
)

var hostLabelPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

func Normalize(hosts []string) ([]string, error) {
	if len(hosts) > MaxPublicHosts {
		return nil, errors.New("ingress host allowlist exceeds 64 entries")
	}
	normalized := make([]string, 0, len(hosts))
	for _, host := range hosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if len(host) < 1 || len(host) > 253 {
			return nil, errors.New("ingress hostname is invalid")
		}
		for _, label := range strings.Split(host, ".") {
			if !hostLabelPattern.MatchString(label) {
				return nil, errors.New("ingress hostname is invalid")
			}
		}
		normalized = append(normalized, host)
	}
	slices.Sort(normalized)
	return slices.Compact(normalized), nil
}

func Render(hosts []string) ([]byte, error) {
	normalized, err := Normalize(hosts)
	if err != nil {
		return nil, err
	}
	quoted := make([]string, 0, len(normalized))
	for _, host := range normalized {
		quoted = append(quoted, strconv.Quote(host))
	}
	return []byte("public_hosts = [" + strings.Join(quoted, ", ") + "]\n"), nil
}
