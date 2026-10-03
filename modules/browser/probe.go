package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	managedcommand "loki/internal/platform/command"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// The parent grants startup only after attaching process ownership. In
// particular, Windows Chrome must inherit the Node process's Job Object.
const runtimeProbe = `
process.stdin.once('data', async () => {
  process.stdin.destroy();
  let browser;
  let videoDirectory;
  try {
    const fs = require('node:fs');
    videoDirectory = fs.mkdtempSync(require('node:path').join(require('node:os').tmpdir(), 'loki-browser-probe-'));
    const { chromium } = require(process.argv[1]);
    browser = await chromium.launch({
      executablePath: process.argv[2], headless: true,
      chromiumSandbox: true, timeout: 15000
    });
    const version = browser.version();
    // Video is in the pinned default catalog. Exercise its bundled FFmpeg
    // dependency too, using an owned about:blank page and temporary files.
    const context = await browser.newContext({recordVideo: {dir: videoDirectory, size: {width: 320, height: 240}}});
    const page = await context.newPage();
    await page.setContent('<html><body>Browser readiness</body></html>');
    await page.screenshot();
    await page.waitForTimeout(300);
    const video = page.video();
    await context.close();
    if (!video || fs.statSync(await video.path()).size === 0) throw new Error('Bundled browser video dependency is unavailable');
    await browser.close();
    browser = undefined;
    process.stdout.write(JSON.stringify({version}) + '\n');
  } catch (error) {
    process.stderr.write(String(error) + '\n');
    process.exitCode = 1;
  } finally {
    if (browser) await browser.close().catch(() => {});
    if (videoDirectory) require('node:fs').rmSync(videoDirectory, {recursive: true, force: true});
  }
});
process.stdin.resume();
`

// CheckRuntime launches bundled Chrome with its sandbox enabled, observes the
// actual browser version and closes it. Playwright owns a temporary profile;
// project profiles, network pages and MCP sessions are not opened by doctor.
// A temporary local-page video also verifies the default FFmpeg dependency.
func CheckRuntime(ctx context.Context, bundle string) error {
	r, err := LoadRuntime(bundle)
	if err != nil {
		return err
	}
	node, err := asset(bundle, r.Node)
	if err != nil {
		return err
	}
	chrome, err := asset(bundle, r.Chrome)
	if err != nil {
		return err
	}
	browsers, err := asset(bundle, r.Browsers)
	if err != nil {
		return err
	}
	core, err := asset(bundle, "node_modules/playwright-core/index.js")
	if err != nil {
		return fmt.Errorf("bundled browser launch adapter unavailable: %w", err)
	}
	checkCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	if runtime.GOOS == "darwin" {
		app, err := chromeApp(bundle, chrome)
		if err != nil {
			return err
		}
		verification := managedcommand.New(checkCtx, "/usr/bin/codesign", "--verify", "--deep", "--strict", app)
		var diagnostics probeOutput
		verification.Stdout, verification.Stderr = &diagnostics, &diagnostics
		// Identical writers make os/exec serialize stdout and stderr writes.
		if err := verification.Run(); err != nil {
			return fmt.Errorf("installed Chrome vendor signature is invalid: %w: %s", err, strings.TrimSpace(diagnostics.String()))
		}
	}
	versionCmd := managedcommand.New(checkCtx, node, "--version")
	versionCmd.Env = runtimeEnvironment(os.Environ(), browsers)
	version, err := versionCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("managed Node cannot start: %w: %s", err, strings.TrimSpace(string(version)))
	}
	if strings.TrimSpace(string(version)) != "v"+NodeVersion {
		return fmt.Errorf("browser requires managed Node %s", NodeVersion)
	}
	command := managedcommand.New(checkCtx, node, "-e", runtimeProbe, core, chrome)
	command.Env = runtimeEnvironment(os.Environ(), browsers)
	command.WaitDelay = 2 * time.Second
	var stdout, stderr probeOutput
	command.Stdout, command.Stderr = &stdout, &stderr
	gate, err := command.StdinPipe()
	if err != nil {
		return err
	}
	if err := command.Start(); err != nil {
		_ = gate.Close()
		return fmt.Errorf("browser startup probe cannot start: %w", err)
	}
	cleanup, err := managedcommand.Own(command)
	if err != nil {
		_ = gate.Close()
		managedcommand.Cleanup(command)
		_ = command.Wait()
		return fmt.Errorf("browser startup probe ownership failed: %w", err)
	}
	defer cleanup()
	_, gateErr := io.WriteString(gate, "start\n")
	_ = gate.Close()
	waitErr := command.Wait()
	if gateErr != nil {
		return fmt.Errorf("browser startup probe handshake failed: %w", gateErr)
	}
	if waitErr != nil {
		return fmt.Errorf("managed Chrome sandbox startup failed; check native libraries and host sandbox support: %w: %s", waitErr, strings.TrimSpace(stderr.String()))
	}
	return checkProbeVersion(stdout.Bytes())
}

func chromeApp(bundle, chrome string) (string, error) {
	root, err := asset(bundle, "chrome")
	if err != nil {
		return "", err
	}
	var app string
	for parent := filepath.Dir(chrome); parent != root; parent = filepath.Dir(parent) {
		if parent == filepath.Dir(parent) {
			return "", fmt.Errorf("managed Chrome executable has no vendor app")
		}
		if strings.HasSuffix(parent, ".app") {
			app = parent
		}
	}
	if app == "" {
		return "", fmt.Errorf("managed Chrome executable has no signed vendor app")
	}
	return app, nil
}

func checkProbeVersion(output []byte) error {
	var result struct {
		Version string `json:"version"`
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return fmt.Errorf("invalid browser startup probe response: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("browser startup probe returned trailing output")
	}
	if result.Version != ChromeVersion {
		return fmt.Errorf("browser requires managed Chrome %s; observed %s", ChromeVersion, result.Version)
	}
	return nil
}

// Keep startup diagnostics bounded while continuing to drain process pipes.
type probeOutput struct{ buffer bytes.Buffer }

func (p *probeOutput) Len() int       { return p.buffer.Len() }
func (p *probeOutput) Bytes() []byte  { return p.buffer.Bytes() }
func (p *probeOutput) String() string { return p.buffer.String() }

func (p *probeOutput) Write(data []byte) (int, error) {
	count := len(data)
	if remaining := 64*1024 - p.Len(); remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = p.buffer.Write(data)
	}
	return count, nil
}

func runtimeEnvironment(environment []string, browsers string) []string {
	result := make([]string, 0, len(environment)+1)
	for _, variable := range environment {
		key, _, _ := strings.Cut(variable, "=")
		upper := strings.ToUpper(key)
		if upper == "NODE_OPTIONS" || upper == "NODE_PATH" || upper == "PLAYWRIGHT_BROWSERS_PATH" || strings.HasPrefix(upper, "PLAYWRIGHT_MCP_") {
			continue
		}
		result = append(result, variable)
	}
	return append(result, "PLAYWRIGHT_BROWSERS_PATH="+browsers)
}
