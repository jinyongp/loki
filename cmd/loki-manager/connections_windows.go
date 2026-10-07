//go:build windows

package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/term"
	connectcatalog "loki/internal/host/connect"
	windowshost "loki/internal/host/windows"
	"loki/internal/management"
	"loki/internal/progress"
	windowspackage "loki/packaging/tools"
)

type embeddedHelperDownloader struct {
	catalog  connectcatalog.Catalog
	progress progress.Reporter
}

func (d embeddedHelperDownloader) Fetch(ctx context.Context, url string, max int64) ([]byte, error) {
	if strings.HasSuffix(url, "/loki-connect-helpers.json") {
		return append([]byte(nil), windowspackage.Helpers...), nil
	}
	for _, helper := range d.catalog.Helpers {
		if strings.HasSuffix(url, "/"+helper.Archive.MirrorAsset) {
			return (windowshost.HTTPHelperDownloader{Progress: d.progress}).Fetch(ctx, helper.Archive.SourceURL, max)
		}
	}
	return nil, fmt.Errorf("helper acquisition is outside the compiled upstream catalog")
}

type toolsConnectionAppliance struct {
	frontend    management.Store
	selected    management.ExecutionSelection
	diagnostics io.Writer
}

func (p toolsConnectionAppliance) Prepare(ctx context.Context, distribution string) error {
	if p.selected.Host.Kind == "wsl" {
		record, err := p.frontend.HostPreparation()
		if err != nil || record == nil || record.Distribution != distribution || !p.selected.Owned {
			return fmt.Errorf("managed WSL connection requires its verified Loki host; run loki setup")
		}
		probe := p.selected
		probe.Command = "/usr/bin/cat"
		command, err := management.RelaySelection(ctx, probe, []string{"/var/lib/loki-host-owner"})
		if err != nil {
			return err
		}
		out, err := command.Output()
		if err != nil || strings.TrimSpace(string(out)) != record.Token {
			return fmt.Errorf("managed WSL ownership changed; existing data were retained")
		}
		if err := windowshost.EnsureToolsStartup(ctx, p.frontend.Root, distribution); err != nil {
			return err
		}
	}
	args := []string{"tools", "start"}
	if p.selected.Root != "" {
		args = append([]string{"--root", p.selected.Root}, args...)
	}
	relay, err := management.RelaySelection(ctx, p.selected, args)
	if err != nil {
		return err
	}
	relay.Stdout, relay.Stderr = p.diagnostics, p.diagnostics
	if err := relay.Run(); err != nil {
		return fmt.Errorf("selected tool services are not ready: %w", err)
	}
	return ensureConnectionBridge(ctx, p.frontend, p.diagnostics)
}

type toolsBridgeSource struct{ store management.Store }

func (s toolsBridgeSource) Read(context.Context, string) (windowshost.ConnectionMaterial, error) {
	token, err := connectionBridgeToken(s.store)
	if err != nil {
		return windowshost.ConnectionMaterial{}, err
	}
	defer clear(token)
	return windowshost.ConnectionMaterial{LocalOrigin: connectionBridgeOrigin(s.store.Root), Transport: "streamable-http", Reachability: "loopback", AuthenticationType: "bearer", Token: string(token)}, nil
}

func ensureConnectionBridge(ctx context.Context, store management.Store, diagnostics io.Writer) error {
	platform := windowshost.WindowsHelperInstallPlatform{}
	directory := filepath.Join(store.Root, "control", "connections")
	if err := platform.EnsurePrivateDirectory(ctx, directory); err != nil {
		return err
	}
	path := filepath.Join(directory, "bridge.token")
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		var token [32]byte
		if _, err := rand.Read(token[:]); err != nil {
			return err
		}
		if err := platform.WritePrivateFile(ctx, path, []byte(hex.EncodeToString(token[:]))); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if err := platform.VerifyPrivatePath(path, false); err != nil {
		return err
	}
	token, err := connectionBridgeToken(store)
	if err != nil {
		return err
	}
	defer clear(token)
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("owned bridge redirects are disabled") }}
	owner := sha256.Sum256([]byte(filepath.Clean(store.Root)))
	probe := func() bool {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, connectionBridgeOrigin(store.Root)+"/healthz", nil)
		if err != nil {
			return false
		}
		request.Header.Set("Authorization", "Bearer "+string(token))
		response, err := client.Do(request)
		if err != nil {
			return false
		}
		defer response.Body.Close()
		var result struct {
			Owner string
			PID   int
		}
		return response.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&result) == nil && result.Owner == fmt.Sprintf("%x", owner)
	}
	if probe() {
		return nil
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	binary, err = store.ManagerExecutable(binary)
	if err != nil {
		return err
	}
	command := newConnectionCommand(context.Background(), binary, "--root", store.Root, "--host", "local", "_connection-bridge")
	logPath := filepath.Join(directory, "bridge.log")
	if err := platform.WritePrivateFile(ctx, logPath, nil); err != nil {
		return err
	}
	log, err := os.OpenFile(logPath, os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	command.Stdout, command.Stderr = io.Discard, &boundedBridgeLog{file: log, remaining: 1 << 20}
	// Background lifetime is independent of the requesting terminal. Startup
	// restoration comes from the owned GUI scheduled-task companion.
	if err := command.Start(); err != nil {
		log.Close()
		return err
	}
	completed := make(chan error, 1)
	go func() { completed <- command.Wait(); log.Close() }()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	for {
		if probe() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("owned connection bridge did not become ready; inspect %s and retry connections start", logPath)
		case err := <-completed:
			return fmt.Errorf("owned connection bridge exited during startup; inspect %s: %v", logPath, err)
		case <-ticker.C:
		}
	}
}

func runConnections(ctx context.Context, store management.Store, args []string, input io.Reader, out, diagnostics io.Writer) error {
	if len(args) > 1 && args[0] == "setup" && args[1] == "codex" {
		selected, err := store.ExecutionSelection()
		if err != nil {
			return err
		}
		if selected != nil && selected.Remote() {
			return connectRemoteCodex(ctx, *selected, args[1:], out, diagnostics)
		}
		return connectCodex(store, args[1:], out, diagnostics)
	}
	if len(args) == 0 || len(args) == 1 && args[0] == "list" {
		return result(out, "Loki connections", map[string]any{"providers": []string{"codex", "openai"}})
	}
	f := flag.NewFlagSet("connections", flag.ContinueOnError)
	f.SetOutput(diagnostics)
	tunnel := f.String("tunnel-id", "", "OpenAI tunnel ID")
	keyStdin := f.Bool("runtime-key-stdin", false, "read the runtime key privately from stdin")
	distribution := f.String("distribution", "", "startup restoration guard")
	if err := f.Parse(toolArguments(args[1:])); err != nil {
		return err
	}
	action := args[0]
	if action != "restore" && (f.NArg() != 1 || f.Arg(0) != "openai") {
		return fmt.Errorf("managed connection actions require openai")
	}
	if action == "restore" && f.NArg() != 0 {
		return fmt.Errorf("invalid connection restore arguments")
	}
	if action != "setup" && (*tunnel != "" || *keyStdin) {
		return fmt.Errorf("credentials are accepted only by connections setup")
	}
	if !strings.Contains("|setup|start|stop|remove|status|show|doctor|restore|", "|"+action+"|") {
		return fmt.Errorf("unknown connection action")
	}
	selected, err := store.ExecutionSelection()
	if err != nil {
		return err
	}
	if selected == nil || !selected.Remote() {
		return fmt.Errorf("select and configure a full execution host with loki setup before connecting OpenAI")
	}
	alias := connectionHostAlias(*selected)
	if *distribution != "" && *distribution != alias {
		return fmt.Errorf("startup host differs from the selected execution host")
	}
	unlock, err := store.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	states := windowshost.WindowsConnectionStateStore{ConnectionsRoot: filepath.Join(store.Root, "control", "connections", "providers")}
	setup := windowshost.OpenAISetupConfig{TunnelID: *tunnel}
	if action == "setup" {
		root, err := states.ProviderRoot(alias, "openai")
		if err != nil {
			return err
		}
		metadata, present, err := windowshost.NewWindowsOpenAIProviderStore().Read(root)
		if err != nil {
			return err
		}
		if setup.TunnelID == "" && present {
			setup.TunnelID = metadata.TunnelID
		}
		if setup.TunnelID == "" {
			if file, ok := input.(*os.File); !*keyStdin && ok && term.IsTerminal(int(file.Fd())) {
				fmt.Fprint(diagnostics, "OpenAI tunnel ID: ")
				setup.TunnelID, err = readSetupLine(input, 256)
				if err != nil {
					return err
				}
			} else {
				return fmt.Errorf("OpenAI tunnel ID is required; supply --tunnel-id for noninteractive setup")
			}
		}
		var key []byte
		if *keyStdin {
			key, err = io.ReadAll(io.LimitReader(input, 4097))
			if len(key) > 4096 {
				return fmt.Errorf("runtime key exceeds its bound")
			}
		} else if file, ok := input.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
			fmt.Fprint(diagnostics, "OpenAI runtime API key (input hidden; Enter keeps the stored key): ")
			key, err = term.ReadPassword(int(file.Fd()))
			fmt.Fprintln(diagnostics)
		}
		if err != nil {
			return err
		}
		defer clear(key)
		setup.RuntimeKey = strings.TrimSpace(string(key))
	}
	reporter := progress.NewLineReporter(diagnostics)
	catalog, err := connectcatalog.LoadCatalog(windowspackage.Helpers)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(windowspackage.Helpers)
	helpers := windowshost.HelperManager{Platform: windowshost.WindowsHelperInstallPlatform{}, Downloader: embeddedHelperDownloader{catalog: catalog, progress: reporter}, HelpersRoot: filepath.Join(store.Root, "control", "connections", "helpers"), Binding: windowshost.ReleaseBinding{ReleaseTag: "v" + management.ManagerRelease, HelperCatalog: windowshost.FileBinding{SHA256: fmt.Sprintf("%x", digest), Length: int64(len(windowspackage.Helpers))}}}
	adapter := windowshost.NewWindowsOpenAIAdapter(setup)
	adapter.Local = toolsBridgeSource{store: store}
	adapter.Progress = reporter
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	binary, err = store.ManagerExecutable(binary)
	if err != nil {
		return err
	}
	manager := windowshost.ConnectionManager{Appliance: toolsConnectionAppliance{frontend: store, selected: *selected, diagnostics: diagnostics}, Helpers: helpers, Store: states, Tasks: windowshost.ToolsConnectionTasks{Root: store.Root, Binary: binary}, Adapters: []windowshost.RemoteConnectionAdapter{adapter}, Platform: "windows-" + runtime.GOARCH}
	switch action {
	case "setup":
		err = manager.Setup(ctx, alias, "openai")
	case "start":
		err = manager.Start(ctx, alias, "openai")
	case "stop":
		err = manager.Stop(ctx, alias, "openai")
	case "remove":
		err = manager.Remove(ctx, alias, "openai")
	case "restore":
		err = manager.ReconcileEnabled(ctx, alias)
	case "status", "show", "doctor":
		status, err := manager.Status(ctx, alias, "openai")
		if err != nil {
			return err
		}
		if err := result(out, "OpenAI Secure MCP Tunnel", status); err != nil {
			return err
		}
		if action == "doctor" && (!status.Configured || !status.Runtime.Ready) {
			return fmt.Errorf("OpenAI connection is not ready; run loki connections start openai")
		}
		return nil
	}
	if err != nil {
		return err
	}
	if action == "stop" || action == "remove" {
		if err := stopConnectionBridge(ctx, store); err != nil {
			return fmt.Errorf("connection %s completed but its bridge could not stop: %w", action, err)
		}
	}
	return success(out, "Managed OpenAI connection "+action+" completed.", map[string]any{"provider": "openai", "action": action})
}

func stopConnectionBridge(ctx context.Context, store management.Store) error {
	token, err := connectionBridgeToken(store)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer clear(token)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, connectionBridgeOrigin(store.Root)+"/shutdown", nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+string(token))
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("bridge redirects disabled") }}
	response, err := client.Do(request)
	if err != nil {
		probe, probeErr := net.DialTimeout("tcp", strings.TrimPrefix(connectionBridgeOrigin(store.Root), "http://"), time.Second)
		if probeErr != nil {
			return nil
		}
		probe.Close()
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		return fmt.Errorf("owned bridge refused shutdown")
	}
	return nil
}

type boundedBridgeLog struct {
	file      *os.File
	remaining int
}

func removeHostConnections(ctx context.Context, store management.Store, selected management.ExecutionSelection, diagnostics io.Writer) error {
	states := windowshost.WindowsConnectionStateStore{ConnectionsRoot: filepath.Join(store.Root, "control", "connections", "providers")}
	_, present, err := states.Read(connectionHostAlias(selected), "openai")
	if err != nil {
		return err
	}
	if present {
		return runConnections(ctx, store, []string{"remove", "openai"}, strings.NewReader(""), diagnostics, diagnostics)
	}
	if err := stopConnectionBridge(ctx, store); err != nil {
		return err
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	binary, err = store.ManagerExecutable(binary)
	if err != nil {
		return err
	}
	return (windowshost.ToolsConnectionTasks{Root: store.Root, Binary: binary}).Reconcile(ctx, connectionHostAlias(selected), false)
}

func (w *boundedBridgeLog) Write(data []byte) (int, error) {
	n := len(data)
	if w.remaining > 0 {
		part := data[:min(n, w.remaining)]
		_, err := w.file.Write(part)
		if err != nil {
			return 0, err
		}
		w.remaining -= len(part)
	}
	return n, nil
}

func connectionHostAlias(selected management.ExecutionSelection) string {
	if selected.Host.Kind == "ssh" {
		digest := sha256.Sum256([]byte(selected.Host.Address + "\x00" + selected.Root))
		return fmt.Sprintf("ssh-%x", digest[:8])
	}
	return selected.Host.Distribution
}

// A restore task is bound to the current host. Keep that binding until its
// connection has been explicitly removed, rather than orphaning credentials/tasks.
func requireHostConnectionsDetached(store management.Store) error {
	selected, err := store.ExecutionSelection()
	if err != nil || selected == nil || !selected.Remote() {
		return err
	}
	states := windowshost.WindowsConnectionStateStore{ConnectionsRoot: filepath.Join(store.Root, "control", "connections", "providers")}
	_, present, err := states.Read(connectionHostAlias(*selected), "openai")
	if err != nil {
		return err
	}
	if present {
		return fmt.Errorf("remove the current host's managed connection with loki connections remove openai before changing or detaching its host")
	}
	return nil
}
