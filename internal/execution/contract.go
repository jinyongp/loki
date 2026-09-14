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
		"runtime-state": {Path: "/var/lib/loki-go/runtime", Owner: "root", Group: "root", Mode: "0700"},
		"signing-state": {Path: "/var/lib/loki-go/signing", Owner: "root", Group: "root", Mode: "0700"},
		"runner-state":  {Path: "/var/lib/loki-go/runner", Owner: "runner", Group: "runner", Mode: "0700"},
		"runner-cache":  {Path: "/var/cache/loki-go/runner", Owner: "runner", Group: "runner", Mode: "0700"},
		"runner-temp":   {Path: "/var/tmp/loki-go/runner", Owner: "runner", Group: "runner", Mode: "0700"},
		"workspace":     {Path: "/workspace", Owner: "runner", Group: "workspace", Mode: "2770"},
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
		if got != want {
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
		"XDG_CONFIG_HOME":          filepath.Join(state, "config"),
		"XDG_DATA_HOME":            filepath.Join(state, "data"),
		"XDG_STATE_HOME":           filepath.Join(state, "state"),
		"XDG_CACHE_HOME":           cache,
		"NPM_CONFIG_CACHE":         filepath.Join(cache, "npm"),
		"PLAYWRIGHT_BROWSERS_PATH": filepath.Join(cache, "playwright"),
		"GOCACHE":                  filepath.Join(cache, "go-build"),
		"PIP_CACHE_DIR":            filepath.Join(cache, "pip"),
		"TMPDIR":                   temp,
		"PATH":                     "/opt/loki/bin:/usr/local/bin:/usr/bin:/bin",
		"GIT_CONFIG_GLOBAL":        "/home/runner/.gitconfig",
		"GIT_CONFIG_NOSYSTEM":      "1",
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
