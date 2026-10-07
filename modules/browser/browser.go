// Package browser launches pinned official engines on the selected project host.
// Profiles and results belong to this tool; no appliance services are required.
package browser

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	managedcommand "loki/internal/platform/command"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

const PlaywrightVersion = "0.0.83"
const DevToolsVersion = "1.10.1"
const NodeVersion = "26.10.0"
const ChromeVersion = "155.0.8059.39"
const PlaywrightCoreVersion = "1.64.0-alpha-1790635538000"

var Capabilities = []string{"unsafe-code", "vision", "pdf", "devtools", "network", "storage", "testing", "tracing", "config", "extensions", "pwa", "webmcp", "third-party", "memory"}

// Runtime paths are relative to the immutable artifact generation, not PATH.
type Runtime struct {
	Schema   int    `json:"schema"`
	Node     string `json:"node"`
	Chrome   string `json:"chrome"`
	Browsers string `json:"browsers"`
}

type Options struct {
	Bundle    string
	Data      string
	Workspace string
	Engine    string
	// Proxy is administrator-owned full-mode routing, never a tool argument.
	Proxy string
	// RuntimeVerified follows a startup check of the same leased/read-only
	// bundle, letting a combined launcher reuse one check for both engines.
	RuntimeVerified bool
	Capabilities    []string
	Stderr          io.Writer
}

type Launch struct {
	Command *exec.Cmd
	RootURI string
	Output  string
	Cleanup func()
}

func asset(bundle, relative string) (string, error) {
	if relative == "" || !filepath.IsLocal(relative) {
		return "", fmt.Errorf("runtime asset must be relative to its artifact")
	}
	full := filepath.Join(bundle, relative)
	real, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(bundle)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, real)
	if err != nil || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("runtime asset escapes its artifact")
	}
	return real, nil
}

func LoadRuntime(bundle string) (Runtime, error) {
	var runtime Runtime
	path, err := asset(bundle, "runtime.json")
	if err != nil {
		return runtime, err
	}
	file, err := os.Open(path)
	if err != nil {
		return runtime, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 64*1024+1))
	if err != nil {
		return runtime, err
	}
	if len(data) > 64*1024 {
		return runtime, fmt.Errorf("browser runtime metadata exceeds 64 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&runtime); err != nil {
		return runtime, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return runtime, fmt.Errorf("browser runtime metadata has trailing data")
	}
	if runtime.Schema != 1 {
		return runtime, fmt.Errorf("unsupported browser runtime schema")
	}
	for _, relative := range []string{runtime.Node, runtime.Chrome, runtime.Browsers} {
		if _, err := asset(bundle, relative); err != nil {
			return runtime, err
		}
	}
	for pkg, want := range map[string]string{"@playwright/mcp": PlaywrightVersion, "chrome-devtools-mcp": DevToolsVersion, "playwright": PlaywrightCoreVersion, "playwright-core": PlaywrightCoreVersion} {
		path, err := asset(bundle, filepath.Join("node_modules", filepath.FromSlash(pkg), "package.json"))
		if err != nil {
			return runtime, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return runtime, err
		}
		var metadata struct {
			Version string `json:"version"`
		}
		if err := json.Unmarshal(data, &metadata); err != nil {
			return runtime, err
		}
		if metadata.Version != want {
			return runtime, fmt.Errorf("browser package %s version differs from pinned %s", pkg, want)
		}
	}
	return runtime, nil
}

func ValidateCapabilitiesAndProxy(capabilities []string, proxy string) error {
	seen := map[string]bool{}
	for _, capability := range capabilities {
		if !slices.Contains(Capabilities, capability) {
			return fmt.Errorf("unsupported browser capability %q", capability)
		}
		if seen[capability] {
			return fmt.Errorf("duplicate browser capability %q", capability)
		}
		seen[capability] = true
	}
	if proxy != "" {
		u, err := url.Parse(proxy)
		if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("browser proxy must be an administrator-owned HTTP endpoint")
		}
	}
	return nil
}

func Prepare(ctx context.Context, o Options) (Launch, error) {
	if o.Engine != "playwright" && o.Engine != "devtools" {
		return Launch{}, fmt.Errorf("browser engine must be playwright or devtools")
	}
	if err := ValidateCapabilitiesAndProxy(o.Capabilities, o.Proxy); err != nil {
		return Launch{}, err
	}
	r, err := LoadRuntime(o.Bundle)
	if err != nil {
		return Launch{}, err
	}
	node, err := asset(o.Bundle, r.Node)
	if err != nil {
		return Launch{}, err
	}
	chrome, err := asset(o.Bundle, r.Chrome)
	if err != nil {
		return Launch{}, err
	}
	browsers, err := asset(o.Bundle, r.Browsers)
	if err != nil {
		return Launch{}, err
	}
	if !o.RuntimeVerified {
		if err := CheckRuntime(ctx, o.Bundle); err != nil {
			return Launch{}, err
		}
	}
	workspace, err := filepath.Abs(o.Workspace)
	if err != nil {
		return Launch{}, err
	}
	workspace, err = filepath.EvalSymlinks(workspace)
	if err != nil {
		return Launch{}, err
	}
	info, err := os.Stat(workspace)
	if err != nil {
		return Launch{}, err
	}
	if !info.IsDir() {
		return Launch{}, fmt.Errorf("browser workspace must be a directory")
	}
	digest := sha256.Sum256([]byte(workspace))
	project := hex.EncodeToString(digest[:16])
	profile, output, cleanup, err := ownedSession(ctx, o.Data, project, o.Engine)
	if err != nil {
		return Launch{}, err
	}
	if err := markEnginePrepared(profile); err != nil {
		cleanup()
		return Launch{}, err
	}
	var args []string
	if o.Engine == "playwright" {
		args = []string{filepath.Join(o.Bundle, "node_modules", "@playwright", "mcp", "cli.js"), "--headless", "--sandbox", "--executable-path", chrome, "--user-data-dir", profile, "--output-dir", output}
		var caps []string
		for _, cap := range o.Capabilities {
			if slices.Contains([]string{"vision", "pdf", "devtools", "network", "storage", "testing", "tracing", "config"}, cap) {
				caps = append(caps, cap)
			}
		}
		if len(caps) > 0 {
			args = append(args, "--caps", strings.Join(caps, ","))
		}
		if o.Proxy != "" {
			args = append(args, "--proxy-server", o.Proxy)
		}
	} else {
		args = []string{filepath.Join(o.Bundle, "node_modules", "chrome-devtools-mcp", "build", "src", "bin", "chrome-devtools-mcp.js"), "--headless", "--executablePath", chrome, "--userDataDir", profile, "--filesystemRoot", output, "--no-usage-statistics", "--no-performance-crux"}
		if o.Proxy != "" {
			args = append(args, "--chromeArg=--proxy-server="+o.Proxy)
		}
		for _, cap := range o.Capabilities {
			switch cap {
			case "extensions":
				args = append(args, "--category-extensions")
			case "pwa":
				args = append(args, "--category-pwa")
			case "webmcp":
				args = append(args, "--categoryExperimentalWebmcp", "--chromeArg=--enable-features=WebMCP")
			case "third-party":
				args = append(args, "--categoryExperimentalThirdParty")
			case "memory":
				args = append(args, "--memoryDebugging")
			case "vision":
				args = append(args, "--experimentalVision")
			case "devtools":
				args = append(args, "--experimentalDevtools")
			}
		}
	}
	command := managedcommand.New(ctx, node, args...)
	command.Dir = workspace
	command.Stderr = o.Stderr
	command.Env = runtimeEnvironment(os.Environ(), browsers)
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(workspace)}
	if !strings.HasPrefix(u.Path, "/") {
		u.Path = "/" + u.Path
	}
	return Launch{Command: command, RootURI: u.String(), Output: output, Cleanup: cleanup}, nil
}

func Allowed(name string, capabilities []string) bool {
	// These tools execute code in the upstream server process. Browser page
	// scripts retain their upstream semantics and project-host authority.
	if name == "browser_run_code" || name == "browser_run_code_unsafe" || name == "execute" {
		return slices.Contains(capabilities, "unsafe-code")
	}
	return true
}
