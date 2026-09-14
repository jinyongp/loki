// Package execution defines the administrator-owned execution environment
// contract shared by Loki packaging and runtime launch paths.
package execution

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
)

const Version = 1

type Contract struct {
	Version         int                       `json:"version"`
	Directories     map[string]Directory      `json:"directories"`
	Environment     map[string]string         `json:"environment"`
	NetworkProfiles map[string]NetworkProfile `json:"network_profiles"`
}

type Directory struct {
	Path  string `json:"path"`
	Owner string `json:"owner"`
	Group string `json:"group"`
	Mode  string `json:"mode"`
}

type NetworkProfile struct {
	Mode                   string `json:"mode"`
	Proxy                  string `json:"proxy,omitempty"`
	AllowSecrets           bool   `json:"allow_secrets"`
	AdministratorAllowlist bool   `json:"administrator_allowlist"`
}

func Load(raw []byte) (Contract, error) {
	var contract Contract
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&contract); err != nil {
		return Contract{}, fmt.Errorf("decode execution contract: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Contract{}, errors.New("execution contract contains trailing data")
	}
	if err := contract.Validate(); err != nil {
		return Contract{}, err
	}
	return contract, nil
}

func (c Contract) Validate() error {
	if c.Version != Version {
		return fmt.Errorf("unsupported execution contract version %d", c.Version)
	}
	requiredDirectories := map[string]Directory{
		"runtime-state": {Owner: "root", Group: "root", Mode: "0700"},
		"signing-state": {Owner: "root", Group: "root", Mode: "0700"},
		"runner-state":  {Owner: "runner", Group: "runner", Mode: "0700"},
		"runner-cache":  {Owner: "runner", Group: "runner", Mode: "0700"},
		"runner-temp":   {Owner: "runner", Group: "runner", Mode: "0700"},
		"workspace":     {Owner: "runner", Group: "workspace", Mode: "2770"},
	}
	if len(c.Directories) != len(requiredDirectories) {
		return errors.New("execution contract directory set is incomplete")
	}
	seen := map[string]string{}
	for name, want := range requiredDirectories {
		got, ok := c.Directories[name]
		if !ok {
			return fmt.Errorf("execution contract directory %q is missing", name)
		}
		if got.Owner != want.Owner || got.Group != want.Group || got.Mode != want.Mode {
			return fmt.Errorf("execution contract directory %q = %#v, want %#v", name, got, want)
		}
		if !filepath.IsAbs(got.Path) || filepath.Clean(got.Path) != got.Path {
			return fmt.Errorf("execution contract directory %q is not a clean absolute path", name)
		}
		if other := seen[got.Path]; other != "" {
			return fmt.Errorf("execution contract directories %q and %q share a path", other, name)
		}
		seen[got.Path] = name
	}

	state := c.Directories["runner-state"].Path
	cache := c.Directories["runner-cache"].Path
	temp := c.Directories["runner-temp"].Path
	requiredEnvironment := map[string]string{
		"HOME":                     "/home/runner",
		"GH_CONFIG_DIR":            filepath.Join(state, "config", "gh"),
		"XDG_CONFIG_HOME":          filepath.Join(state, "config"),
		"XDG_DATA_HOME":            filepath.Join(state, "data"),
		"XDG_STATE_HOME":           filepath.Join(state, "state"),
		"XDG_CACHE_HOME":           cache,
		"NPM_CONFIG_CACHE":         filepath.Join(cache, "npm"),
		"npm_config_store_dir":     filepath.Join(cache, "pnpm"),
		"PLAYWRIGHT_BROWSERS_PATH": filepath.Join(cache, "playwright"),
		"GOCACHE":                  filepath.Join(cache, "go-build"),
		"GOMODCACHE":               filepath.Join(cache, "go-mod"),
		"PIP_CACHE_DIR":            filepath.Join(cache, "pip"),
		"TMPDIR":                   temp,
		"PATH":                     "/opt/loki/bin:/usr/local/bin:/usr/bin:/bin",
		"GIT_CONFIG_GLOBAL":        "/home/runner/.gitconfig",
		"GIT_CONFIG_NOSYSTEM":      "1",
		"GIT_OPTIONAL_LOCKS":       "0",
		"LANG":                     "C.UTF-8",
		"LC_ALL":                   "C.UTF-8",
	}
	if len(c.Environment) != len(requiredEnvironment) {
		return errors.New("execution contract environment set is incomplete")
	}
	for name, want := range requiredEnvironment {
		got, ok := c.Environment[name]
		if !ok || got != want || strings.ContainsAny(got, "\r\n\x00") {
			return fmt.Errorf("execution contract environment %q = %q, want %q", name, got, want)
		}
	}

	requiredNetworks := map[string]NetworkProfile{
		"dependency-install": {Mode: "proxy", Proxy: "http://127.0.0.1:18766", AllowSecrets: false, AdministratorAllowlist: true},
		"runtime-default":    {Mode: "loopback", AllowSecrets: true},
		"runtime-profile":    {Mode: "proxy", Proxy: "http://127.0.0.1:18766", AllowSecrets: true, AdministratorAllowlist: true},
		"browser":            {Mode: "proxy", Proxy: "http://127.0.0.1:18767", AllowSecrets: false, AdministratorAllowlist: true},
	}
	if len(c.NetworkProfiles) != len(requiredNetworks) {
		return errors.New("execution contract network profile set is incomplete")
	}
	for name, want := range requiredNetworks {
		if got, ok := c.NetworkProfiles[name]; !ok || got != want {
			return fmt.Errorf("execution contract network profile %q = %#v, want %#v", name, got, want)
		}
	}
	return nil
}

// EnvironmentList returns the complete runner environment in stable order.
// Callers must use it as the child environment rather than merging the parent
// process environment.
func (c Contract) EnvironmentList() ([]string, error) {
	return c.EnvironmentForNetwork("runtime-default")
}

// EnvironmentForNetwork returns the closed runner environment for one
// administrator-defined network profile.
func (c Contract) EnvironmentForNetwork(profile string) ([]string, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	network, ok := c.NetworkProfiles[profile]
	if !ok {
		return nil, fmt.Errorf("unknown execution network profile %q", profile)
	}
	values := make(map[string]string, len(c.Environment)+6)
	for name, value := range c.Environment {
		values[name] = value
	}
	if network.Mode == "proxy" {
		for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
			values[name] = network.Proxy
		}
		values["NO_PROXY"] = "127.0.0.1,localhost"
		values["no_proxy"] = "127.0.0.1,localhost"
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
