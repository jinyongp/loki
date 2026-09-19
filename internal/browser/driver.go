package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"loki/internal/cdp"
	"loki/internal/daemon"
	"loki/internal/platform/netguard"
)

// Options are supplied by the administrator. The browser service runs inside
// the installed filesystem/network sandbox; Chrome has no user-supplied flags.
type Options struct {
	Binary, Profile, Downloads, Proxy, LibraryPath string
}
type Driver struct {
	options         Options
	gate            chan struct{}
	client          *cdp.Client
	command         *exec.Cmd
	wait            chan error
	target          string
	generation      uint64
	stateGeneration uint64
	sessions        map[string]string
	closed          map[string]struct{}
	debug           Debug
	downloads       *downloads
}

func NewDriver(options Options) (*Driver, error) {
	for _, path := range []string{options.Binary, options.Profile, options.Downloads} {
		if !filepath.IsAbs(path) {
			return nil, errors.New("browser paths must be absolute")
		}
	}
	proxy, err := url.Parse(options.Proxy)
	if err != nil {
		return nil, errors.New("browser requires the managed HTTP proxy")
	}
	proxyHost := proxy.Hostname()
	if proxy.Scheme != "http" || proxyHost != "127.0.0.1" && proxyHost != "browser-proxy" ||
		proxy.Port() == "" || proxy.User != nil || proxy.Opaque != "" || proxy.Path != "" ||
		proxy.RawQuery != "" || proxy.ForceQuery || strings.Contains(options.Proxy, "#") {
		return nil, errors.New("browser requires the managed HTTP proxy")
	}
	port, err := strconv.Atoi(proxy.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("invalid browser proxy port")
	}
	return &Driver{options: options, gate: make(chan struct{}, 1)}, nil
}
func (d *Driver) lock(ctx context.Context) error {
	select {
	case d.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (d *Driver) Close() {
	d.gate <- struct{}{}
	defer func() { <-d.gate }()
	d.stop()
}
func (d *Driver) stop() {
	if d.client != nil {
		d.client.Close()
		d.client = nil
	}
	if d.command != nil {
		_ = syscall.Kill(-d.command.Process.Pid, syscall.SIGKILL)
		<-d.wait
		d.command = nil
	}
	if d.downloads != nil {
		d.downloads.Close()
		d.downloads = nil
	}
	d.target = ""
	d.sessions = nil
	d.closed = nil
	d.debug.Reset()
}
func (d *Driver) start(ctx context.Context) (err error) {
	if d.client != nil {
		if err = d.client.Call(ctx, "", "Browser.getVersion", nil, nil); err == nil {
			before := d.target
			if err = d.focus(ctx); err == nil && d.target != before {
				d.generation++
			}
			return err
		}
		d.stop()
	}
	if err = daemon.PrivateDirectory(d.options.Profile); err != nil {
		return err
	}
	temporary := filepath.Join(filepath.Dir(d.options.Profile), "tmp")
	if err = daemon.PrivateDirectory(temporary); err != nil {
		return err
	}
	if err = prepareDownloads(d.options.Downloads); err != nil {
		return err
	}
	inRead, inWrite, err := os.Pipe()
	if err != nil {
		return err
	}
	defer inRead.Close()
	defer func() {
		if err != nil {
			inWrite.Close()
		}
	}()
	outRead, outWrite, err := os.Pipe()
	if err != nil {
		return err
	}
	defer outWrite.Close()
	defer func() {
		if err != nil {
			outRead.Close()
		}
	}()
	command := exec.Command(d.options.Binary,
		"--headless", "--no-sandbox", "--disable-gpu", "--disable-background-networking", "--disable-sync",
		"--no-first-run", "--no-default-browser-check", "--disable-component-update", "--disable-quic",
		"--force-webrtc-ip-handling-policy=disable_non_proxied_udp", "--proxy-bypass-list=<-loopback>",
		"--proxy-server="+d.options.Proxy, "--remote-debugging-pipe", "--window-size=1280,800",
		"--user-data-dir="+d.options.Profile, "about:blank")
	command.ExtraFiles = []*os.File{inRead, outWrite}
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + d.options.Profile, "TMPDIR=" + temporary, "LANG=C.UTF-8"}
	command.Stderr = os.Stderr
	if d.options.LibraryPath != "" {
		command.Env = append(command.Env, "LD_LIBRARY_PATH="+d.options.LibraryPath)
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err = command.Start(); err != nil {
		return err
	}
	d.command = command
	d.wait = make(chan error, 1)
	go func() { d.wait <- command.Wait() }()
	inRead.Close()
	outWrite.Close()
	d.debug.Reset()
	d.sessions = map[string]string{}
	d.closed = map[string]struct{}{}
	defer func() {
		if err != nil {
			d.stop()
		}
	}()
	d.downloads, err = newDownloads(d.options.Downloads)
	if err != nil {
		return err
	}
	d.client = cdp.New(outRead, inWrite, func(e cdp.Event) { d.debug.Event(e); d.downloads.Event(e) })
	if err = d.client.Call(ctx, "", "Browser.getVersion", nil, nil); err != nil {
		return err
	}
	if err = d.client.Call(ctx, "", "Browser.setDownloadBehavior", map[string]any{"behavior": "allowAndName", "downloadPath": d.options.Downloads, "eventsEnabled": true}, nil); err != nil {
		return err
	}
	if err = d.focus(ctx); err != nil {
		return err
	}
	d.generation++
	return nil
}

type targetInfo struct{ TargetID, Type, URL, Title string }

func (d *Driver) targets(ctx context.Context) ([]targetInfo, error) {
	var result struct{ TargetInfos []targetInfo }
	if err := d.client.Call(ctx, "", "Target.getTargets", nil, &result); err != nil {
		return nil, err
	}
	targets := []targetInfo{}
	for _, t := range result.TargetInfos {
		if _, closed := d.closed[t.TargetID]; t.Type == "page" && !closed {
			targets = append(targets, t)
		}
	}
	return targets, nil
}
func (d *Driver) focus(ctx context.Context) error {
	targets, err := d.targets(ctx)
	if err != nil {
		return err
	}
	found := false
	for _, t := range targets {
		if t.TargetID == d.target {
			found = true
		}
	}
	if !found {
		d.target = ""
		if len(targets) > 0 {
			d.target = targets[0].TargetID
		} else {
			var result struct{ TargetID string }
			if err = d.client.Call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &result); err != nil {
				return err
			}
			d.target = result.TargetID
		}
	}
	if d.sessions[d.target] == "" {
		var result struct{ SessionID string }
		if err = d.client.Call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": d.target, "flatten": true}, &result); err != nil {
			return err
		}
		for _, method := range []string{"Page.enable", "Runtime.enable", "Log.enable", "Network.enable"} {
			var params any
			if method == "Network.enable" {
				params = map[string]any{"maxTotalBufferSize": 10485760, "maxResourceBufferSize": 1048576, "maxPostDataSize": 0}
			}
			if err = d.client.Call(ctx, result.SessionID, method, params, nil); err != nil {
				return err
			}
		}
		d.sessions[d.target] = result.SessionID
	}
	return d.client.Call(ctx, "", "Target.activateTarget", map[string]any{"targetId": d.target}, nil)
}
func shortID(id string) any {
	if len(id) < 4 {
		return nil
	}
	return id[len(id)-4:]
}
func (d *Driver) tabList(ctx context.Context) (map[string]any, error) {
	targets, err := d.targets(ctx)
	if err != nil {
		return nil, err
	}
	tabs := []map[string]any{}
	for _, t := range targets {
		tabs = append(tabs, map[string]any{"tab_id": shortID(t.TargetID), "url": t.URL, "title": t.Title, "active": t.TargetID == d.target})
	}
	return map[string]any{"active_tab_id": shortID(d.target), "tabs": tabs, "browser_generation": d.generation}, nil
}
func (d *Driver) resolveTab(ctx context.Context, id string) (string, error) {
	if len(id) != 4 {
		return "", errors.New("tab_id must be the four-character id from browser_list_tabs")
	}
	targets, err := d.targets(ctx)
	if err != nil {
		return "", err
	}
	match := ""
	for _, t := range targets {
		if shortID(t.TargetID) == id {
			if match != "" {
				return "", errors.New("ambiguous tab_id")
			}
			match = t.TargetID
		}
	}
	if match == "" {
		return "", errors.New("tab_id was not found")
	}
	return match, nil
}
func (d *Driver) evaluate(ctx context.Context, expression string, out any) error {
	var tree struct {
		FrameTree struct{ Frame struct{ ID string } }
	}
	if err := d.client.Call(ctx, d.sessions[d.target], "Page.getFrameTree", nil, &tree); err != nil {
		return err
	}
	var world struct{ ExecutionContextID int }
	if err := d.client.Call(ctx, d.sessions[d.target], "Page.createIsolatedWorld", map[string]any{"frameId": tree.FrameTree.Frame.ID, "worldName": "loki-private"}, &world); err != nil {
		return err
	}
	var result struct {
		Result           struct{ Value json.RawMessage }
		ExceptionDetails json.RawMessage
	}
	if err := d.client.Call(ctx, d.sessions[d.target], "Runtime.evaluate", map[string]any{"expression": expression, "contextId": world.ExecutionContextID, "returnByValue": true, "awaitPromise": true, "timeout": 10000}, &result); err != nil {
		return err
	}
	if len(result.ExceptionDetails) > 0 {
		return errors.New("browser page operation failed; refresh browser_state")
	}
	if out == nil {
		return nil
	}
	if len(result.Result.Value) == 0 {
		return errors.New("browser page result is missing")
	}
	return json.Unmarshal(result.Result.Value, out)
}
func (d *Driver) page(ctx context.Context) (map[string]any, error) {
	var result map[string]any
	err := d.evaluate(ctx, `({url:location.href,title:document.title})`, &result)
	return result, err
}
func (d *Driver) waitPage(ctx context.Context, loader string) error {
	timer := time.NewTicker(25 * time.Millisecond)
	defer timer.Stop()
	for {
		var tree struct {
			FrameTree struct{ Frame struct{ LoaderID string } }
		}
		err := d.client.Call(ctx, d.sessions[d.target], "Page.getFrameTree", nil, &tree)
		if err != nil {
			return err
		}
		if loader == "" || tree.FrameTree.Frame.LoaderID == loader {
			var ready string
			if err = d.evaluate(ctx, `document.readyState`, &ready); err == nil && (ready == "interactive" || ready == "complete") {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
}
func (d *Driver) navigate(ctx context.Context, address string, newTab bool) (map[string]any, error) {
	address, err := netguard.ValidateURL(address)
	if err != nil {
		return nil, err
	}
	d.generation++
	if newTab {
		var created struct{ TargetID string }
		if err = d.client.Call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &created); err != nil {
			return nil, err
		}
		d.target = created.TargetID
		if err = d.focus(ctx); err != nil {
			return nil, err
		}
	}
	var result struct{ LoaderID, ErrorText string }
	if err = d.client.Call(ctx, d.sessions[d.target], "Page.navigate", map[string]any{"url": address}, &result); err != nil {
		return nil, err
	}
	if result.ErrorText != "" {
		return nil, fmt.Errorf("navigation failed: %s", result.ErrorText)
	}
	if err = d.waitPage(ctx, result.LoaderID); err != nil {
		return nil, err
	}
	page, err := d.page(ctx)
	if err != nil {
		return nil, err
	}
	page["new_tab"] = newTab
	page["active_tab_id"] = shortID(d.target)
	page["browser_generation"] = d.generation
	return page, nil
}

// Call serializes browser actions and bounds both queueing and execution time.
func (d *Driver) Call(ctx context.Context, operation string, args map[string]any) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if err := d.lock(ctx); err != nil {
		return nil, err
	}
	defer func() { <-d.gate }()
	if operation == "stop" {
		changed := d.client != nil || d.command != nil
		d.stop()
		if changed {
			d.generation++
		}
		return map[string]any{"status": "stopped", "browser_generation": d.generation}, nil
	}
	if operation == "start" {
		if err := d.start(ctx); err != nil {
			return nil, err
		}
		return map[string]any{
			"status": "running", "active_tab_id": shortID(d.target),
			"browser_generation": d.generation,
		}, nil
	}
	if d.client == nil {
		return nil, errors.New("browser is not running; call browser_start first")
	}
	if err := d.focus(ctx); err != nil {
		return nil, err
	}
	interactionGeneration := d.generation
	switch operation {
	case "click", "hover", "drag", "wheel", "fill", "type", "key", "shortcut", "select_option", "set_checked", "focus", "back", "switch_tab", "close_tab":
		requireState := operation == "fill" || operation == "type" || operation == "select_option" || operation == "set_checked" || operation == "focus" ||
			(operation == "click" || operation == "hover" || operation == "wheel") && args["index"] != nil ||
			operation == "drag" && (args["source_index"] != nil || args["target_index"] != nil)
		if err := d.requireInteractionGeneration(args, requireState); err != nil {
			return nil, err
		}
	}
	switch operation {
	case "console", "network", "request", "websockets", "page_errors", "debug_diagnostics":
		result, err := d.observe(ctx, operation, args)
		if err == nil {
			result["browser_generation"] = d.generation
		}
		return result, err
	case "state":
		return d.state(ctx)
	case "click":
		result, err := d.click(ctx, args)
		return d.finishInteraction(interactionGeneration, result, err)
	case "hover":
		result, err := d.hover(ctx, args)
		return d.finishInteraction(interactionGeneration, result, err)
	case "drag":
		result, err := d.drag(ctx, args)
		return d.finishInteraction(interactionGeneration, result, err)
	case "wheel":
		result, err := d.wheel(ctx, args)
		return d.finishInteraction(interactionGeneration, result, err)
	case "fill":
		result, err := d.fillText(ctx, args)
		return d.finishInteraction(interactionGeneration, result, err)
	case "type":
		result, err := d.typeText(ctx, args)
		return d.finishInteraction(interactionGeneration, result, err)
	case "key":
		result, err := d.keyInput(ctx, args, false)
		return d.finishInteraction(interactionGeneration, result, err)
	case "shortcut":
		result, err := d.keyInput(ctx, args, true)
		return d.finishInteraction(interactionGeneration, result, err)
	case "select_option":
		result, err := d.selectOptions(ctx, args)
		return d.finishInteraction(interactionGeneration, result, err)
	case "set_checked":
		result, err := d.setChecked(ctx, args)
		return d.finishInteraction(interactionGeneration, result, err)
	case "focus":
		result, err := d.focusElement(ctx, args)
		return d.finishInteraction(interactionGeneration, result, err)
	case "screenshot":
		return d.screenshot(ctx, args["full_page"] == true)
	case "navigate":
		address, _ := args["url"].(string)
		return d.navigate(ctx, address, args["new_tab"] == true)
	case "list_tabs":
		return d.tabList(ctx)
	case "switch_tab", "close_tab":
		id, _ := args["tab_id"].(string)
		target, err := d.resolveTab(ctx, id)
		if err != nil {
			return nil, err
		}
		if operation == "switch_tab" {
			d.target = target
			d.generation++
			if err = d.focus(ctx); err != nil {
				return nil, err
			}
			result, err := d.page(ctx)
			if err == nil {
				result["tab_id"] = id
			}
			return d.finishInteraction(interactionGeneration, result, err)
		}
		if err = d.client.Call(ctx, "", "Target.closeTarget", map[string]any{"targetId": target}, nil); err != nil {
			return nil, err
		}
		d.generation++
		d.closed[target] = struct{}{}
		delete(d.sessions, target)
		if target == d.target {
			d.target = ""
			targets, err := d.targets(ctx)
			if err != nil {
				return nil, err
			}
			if len(targets) > 0 {
				d.target = targets[0].TargetID
				if err = d.focus(ctx); err != nil {
					return nil, err
				}
			}
		}
		return d.finishInteraction(interactionGeneration, map[string]any{"closed": id, "active_tab_id": shortID(d.target)}, nil)
	case "back":
		var history struct {
			CurrentIndex int
			Entries      []struct {
				ID  int
				URL string
			}
		}
		if err := d.client.Call(ctx, d.sessions[d.target], "Page.getNavigationHistory", nil, &history); err != nil {
			return nil, err
		}
		if history.CurrentIndex > 0 && history.CurrentIndex < len(history.Entries) {
			d.generation++
			if err := d.client.Call(ctx, d.sessions[d.target], "Page.navigateToHistoryEntry", map[string]any{"entryId": history.Entries[history.CurrentIndex-1].ID}, nil); err != nil {
				return nil, err
			}
			ticker := time.NewTicker(25 * time.Millisecond)
			defer ticker.Stop()
			for {
				page, err := d.page(ctx)
				if err == nil && page["url"] == history.Entries[history.CurrentIndex-1].URL {
					break
				}
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-ticker.C:
				}
			}
			if err := d.waitPage(ctx, ""); err != nil {
				return nil, err
			}
		}
		result, err := d.page(ctx)
		return d.finishInteraction(interactionGeneration, result, err)
	default:
		return nil, fmt.Errorf("unknown browser operation: %s", strings.TrimSpace(operation))
	}
}
