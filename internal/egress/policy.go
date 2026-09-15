package egress

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
)

const PolicyVersion = 1

type Policy struct {
	Version  int                `json:"version"`
	Profiles map[string]Profile `json:"profiles"`
}

type Profile struct {
	AllowedHosts []string `json:"allowed_hosts"`
	AllowedPorts []int    `json:"allowed_ports"`
}

func LoadPolicy(raw []byte) (Policy, error) {
	var policy Policy
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return Policy{}, fmt.Errorf("decode egress policy: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Policy{}, errors.New("egress policy contains trailing data")
	}
	if err := policy.Validate(); err != nil {
		return Policy{}, err
	}
	return policy, nil
}

func (p Policy) Validate() error {
	if p.Version != PolicyVersion {
		return fmt.Errorf("unsupported egress policy version %d", p.Version)
	}
	if len(p.Profiles) != 2 {
		return errors.New("egress policy must define dependency-install and github-api")
	}
	dependency, ok := p.Profiles["dependency-install"]
	if !ok || len(dependency.AllowedHosts) == 0 || !slices.Equal(dependency.AllowedPorts, []int{443}) {
		return errors.New("dependency-install egress profile is incomplete")
	}
	github, ok := p.Profiles["github-api"]
	if !ok || !slices.Equal(github.AllowedHosts, []string{"api.github.com"}) || !slices.Equal(github.AllowedPorts, []int{443}) {
		return errors.New("github-api egress profile must allow only api.github.com:443")
	}
	for name, profile := range p.Profiles {
		if !slices.IsSorted(profile.AllowedHosts) {
			return fmt.Errorf("%s hosts must be sorted", name)
		}
		previous := ""
		for _, host := range profile.AllowedHosts {
			if host == previous || host != strings.ToLower(host) || strings.TrimRight(host, ".") != host || net.ParseIP(host) != nil || host == "localhost" || strings.HasSuffix(host, ".localhost") || !validHostname(host) {
				return fmt.Errorf("invalid %s host %q", name, host)
			}
			previous = host
		}
	}
	return nil
}

func validHostname(host string) bool {
	if len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if character != '-' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
				return false
			}
		}
	}
	return true
}

func (p Policy) Allows(profile, host string, port int) bool {
	configured, ok := p.Profiles[profile]
	return ok && slices.Contains(configured.AllowedHosts, host) && slices.Contains(configured.AllowedPorts, port)
}
