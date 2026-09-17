// Package execution defines the administrator-owned execution environment
// contract shared by Loki packaging and runtime launch paths.
package execution

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
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
		"runtime-state":           {Owner: "root", Group: "root", Mode: "0700"},
		"signing-state":           {Owner: "root", Group: "root", Mode: "0700"},
		"runner-state":            {Owner: "runner", Group: "runner", Mode: "0700"},
		"runner-config":           {Owner: "runner", Group: "runner", Mode: "0700"},
		"runner-gh-config":        {Owner: "runner", Group: "runner", Mode: "0700"},
		"runner-data":             {Owner: "runner", Group: "runner", Mode: "0700"},
		"runner-xdg-state":        {Owner: "runner", Group: "runner", Mode: "0700"},
		"snapshots":               {Owner: "runner", Group: "runner", Mode: "0700"},
		"runner-cache":            {Owner: "runner", Group: "runner", Mode: "0700"},
		"runner-npm-cache":        {Owner: "runner", Group: "runner", Mode: "0700"},
		"runner-pnpm-store":       {Owner: "runner", Group: "runner", Mode: "0700"},
		"runner-playwright-cache": {Owner: "runner", Group: "runner", Mode: "0700"},
		"runner-go-build-cache":   {Owner: "runner", Group: "runner", Mode: "0700"},
		"runner-go-mod-cache":     {Owner: "runner", Group: "runner", Mode: "0700"},
		"runner-pip-cache":        {Owner: "runner", Group: "runner", Mode: "0700"},
		"runner-temp":             {Owner: "runner", Group: "runner", Mode: "0700"},
		"workspace":               {Owner: "runner", Group: "workspace", Mode: "2770"},
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

	cache := c.Directories["runner-cache"].Path
	temp := c.Directories["runner-temp"].Path
	if !slices.Contains([]string{"/opt/loki/toolchain/bin:/opt/loki/bin:/usr/local/bin:/usr/bin:/bin", "/opt/loki/bin:/usr/local/bin:/usr/bin:/bin"}, c.Environment["PATH"]) {
		return errors.New("execution contract PATH is not a supported service layout")
	}
	if !slices.Contains([]string{"/etc/loki-go/gitconfig", "/usr/share/doc/loki/loki-gitconfig"}, c.Environment["GIT_CONFIG_GLOBAL"]) {
		return errors.New("execution contract Git config is not a supported service layout")
	}
	requiredEnvironment := map[string]string{
		"HOME":                     "/home/runner",
		"GH_CONFIG_DIR":            c.Directories["runner-gh-config"].Path,
		"XDG_CONFIG_HOME":          c.Directories["runner-config"].Path,
		"XDG_DATA_HOME":            c.Directories["runner-data"].Path,
		"XDG_STATE_HOME":           c.Directories["runner-xdg-state"].Path,
		"XDG_CACHE_HOME":           cache,
		"NPM_CONFIG_CACHE":         c.Directories["runner-npm-cache"].Path,
		"npm_config_store_dir":     c.Directories["runner-pnpm-store"].Path,
		"PLAYWRIGHT_BROWSERS_PATH": c.Directories["runner-playwright-cache"].Path,
		"GOCACHE":                  c.Directories["runner-go-build-cache"].Path,
		"GOMODCACHE":               c.Directories["runner-go-mod-cache"].Path,
		"PIP_CACHE_DIR":            c.Directories["runner-pip-cache"].Path,
		"TMPDIR":                   temp,
		"PATH":                     c.Environment["PATH"],
		"GIT_CONFIG_GLOBAL":        c.Environment["GIT_CONFIG_GLOBAL"],
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
		"dependency-install": {Mode: "proxy", AllowSecrets: false, AdministratorAllowlist: true},
		"runtime-default":    {Mode: "loopback", AllowSecrets: true},
		"runtime-profile":    {Mode: "proxy", AllowSecrets: true, AdministratorAllowlist: true},
		"browser":            {Mode: "proxy", AllowSecrets: false, AdministratorAllowlist: true},
	}
	if len(c.NetworkProfiles) != len(requiredNetworks) {
		return errors.New("execution contract network profile set is incomplete")
	}
	for name, want := range requiredNetworks {
		got, ok := c.NetworkProfiles[name]
		proxy := got.Proxy
		got.Proxy = ""
		if !ok || got != want {
			return fmt.Errorf("execution contract network profile %q = %#v, want %#v", name, got, want)
		}
		allowed := []string{""}
		switch name {
		case "dependency-install", "runtime-profile":
			allowed = []string{"http://127.0.0.1:18766", "http://egress:18766"}
		case "browser":
			allowed = []string{"http://127.0.0.1:18767", "http://browser-proxy:18767"}
		}
		if !slices.Contains(allowed, proxy) {
			return fmt.Errorf("execution contract network profile %q has unsupported proxy", name)
		}
	}
	return nil
}

func (c Contract) ProxyPorts() ([]int, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	seen := map[int]struct{}{}
	for _, profile := range c.NetworkProfiles {
		if profile.Proxy == "" {
			continue
		}
		proxy, err := url.Parse(profile.Proxy)
		if err != nil || proxy.Port() == "" {
			return nil, errors.New("execution contract proxy port is invalid")
		}
		port, err := strconv.Atoi(proxy.Port())
		if err != nil || port < 1 || port > 65535 {
			return nil, errors.New("execution contract proxy port is invalid")
		}
		seen[port] = struct{}{}
	}
	ports := make([]int, 0, len(seen))
	for port := range seen {
		ports = append(ports, port)
	}
	sort.Ints(ports)
	return ports, nil
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
