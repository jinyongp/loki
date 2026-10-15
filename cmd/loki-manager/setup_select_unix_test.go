//go:build linux || darwin

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"charm.land/huh/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"golang.org/x/term"

	"loki/internal/management"
	"loki/internal/tools"
	"loki/modules/browser"
)

func TestSetupMenuKeyboardBackAndExit(t *testing.T) {
	for _, kind := range []string{"tools", "tools-install-cancel", "home", "fresh", "hosts-back", "hosts-back-narrow", "unreachable", "unreachable-tools", "preparation-failed", "remote-home", "connections", "connections-connect-unchanged", "connections-disconnect-unchanged", "install-many-failed", "git-signing-save", "git-signing-cancel", "git-signing-narrow", "result", "result-narrow", "result-escape", "readiness", "features-save", "features-cancel", "features-narrow", "features-all-result", "browser-menu-narrow"} {
		t.Run(kind, func(t *testing.T) { testSetupMenuKeyboardBackAndExit(t, kind) })
	}
}

func TestSetupProgressRestoresTerminal(t *testing.T) {
	for _, kind := range []string{"working-success", "working-failure", "working-cancel"} {
		t.Run(kind, func(t *testing.T) {
			master, terminal, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer terminal.Close()
			if err := pty.Setsize(terminal, &pty.Winsize{Rows: 20, Cols: 50}); err != nil {
				t.Fatal(err)
			}
			captured := make(chan string, 1)
			go func() {
				var transcript strings.Builder
				buffer := make([]byte, 4096)
				for {
					n, err := master.Read(buffer)
					transcript.Write(buffer[:n])
					if err != nil || strings.Contains(transcript.String(), "[RESULT] Fixture action complete") {
						captured <- transcript.String()
						return
					}
				}
			}()
			setupProgressFixture(t, terminal, kind)
			select {
			case transcript := <-captured:
				assertSetupProgressTranscript(t, transcript)
			case <-time.After(5 * time.Second):
				t.Fatal("progress did not restore terminal output")
			}
		})
	}
}

func testSetupMenuKeyboardBackAndExit(t *testing.T, kind string) {
	t.Setenv("TERM", "xterm-256color")
	master, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer terminal.Close()
	if err := pty.Setsize(terminal, &pty.Winsize{Rows: 30, Cols: 120}); err != nil {
		t.Fatal(err)
	}
	if kind == "result-narrow" || kind == "features-narrow" || kind == "browser-menu-narrow" || kind == "hosts-back-narrow" || kind == "git-signing-narrow" {
		if err := pty.Setsize(terminal, &pty.Winsize{Rows: 20, Cols: 50}); err != nil {
			t.Fatal(err)
		}
	}
	before, err := term.GetState(int(terminal.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	chunks := make(chan string, 64)
	go func() {
		defer close(chunks)
		buffer := make([]byte, 4096)
		for {
			n, err := master.Read(buffer)
			select {
			case chunks <- string(buffer[:n]):
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	var completeTranscript strings.Builder
	waitFor := func(texts ...string) string {
		t.Helper()
		var screen strings.Builder
		containsAll := func() bool {
			normalized := strings.Join(strings.Fields(ansi.Strip(screen.String())), " ")
			for _, text := range texts {
				if !strings.Contains(screen.String(), text) && !strings.Contains(normalized, text) {
					return false
				}
			}
			return true
		}
		for !containsAll() {
			select {
			case chunk, ok := <-chunks:
				if !ok {
					t.Fatal("terminal closed before rendering", texts)
				}
				screen.WriteString(chunk)
				completeTranscript.WriteString(chunk)
			case <-ctx.Done():
				t.Fatal("menu did not render", texts)
			}
		}
		return screen.String()
	}
	root := filepath.Join(t.TempDir(), "untouched")
	if strings.HasPrefix(kind, "connections") || kind == "readiness" {
		t.Setenv("CODEX_HOME", t.TempDir())
	}
	var initialState management.Snapshot
	var initialConfig []byte
	if kind == "browser-menu-narrow" || kind == "connections-connect-unchanged" {
		store := management.Store{Root: root}
		initialState, err = store.Load()
		if err != nil {
			t.Fatal(err)
		}
		artifact := tools.Artifact{Module: "browser", Release: management.Release, Target: management.LocalTarget(tools.ProjectHost), URL: "https://example.com/browser.zip", SHA256: strings.Repeat("a", 64), Bytes: 1, Format: "zip"}
		initialState.Installed["browser"] = management.Installation{Artifact: artifact, Manifest: tools.Manifest{Schema: 1, ID: "browser", Release: management.Release, Targets: []tools.Target{artifact.Target}, Capabilities: slices.Clone(browser.Capabilities)}}
		initialState.Config.Tools = []tools.Selection{{ID: "browser", Enabled: false, Capabilities: slices.Clone(browser.Capabilities)}}
		if kind == "connections-connect-unchanged" {
			initialState.Config.Tools[0].Enabled = true
		}
		if err := store.Save(initialState); err != nil {
			t.Fatal(err)
		}
		if kind == "connections-connect-unchanged" {
			if err := connectCodex(store, []string{"codex"}, io.Discard, io.Discard); err != nil {
				t.Fatal(err)
			}
			config, _ := defaultCodexConfig()
			initialConfig, err = os.ReadFile(config)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if kind == "readiness" {
		installSetupSSHFixture(t, `{"target":{"os":"linux","arch":"amd64","mode":"project-host"},"ready":false,"healthy":false,"tools":{"browser":{"installed":true,"enabled":true,"readiness":"degraded","reason":"Chrome startup failed\n<launching> long Chrome command\nerror while loading shared libraries: libnspr4.so: cannot open shared object file"}}}`)
	}
	if strings.HasPrefix(kind, "unreachable") || kind == "preparation-failed" || kind == "remote-home" {
		installSetupSSHFixture(t, `{}`)
		t.Setenv("CODEX_HOME", t.TempDir())
		if kind == "preparation-failed" {
			t.Setenv("LOKI_REVIEW_SSH_UNREACHABLE", "1")
		}
	}
	var output bytes.Buffer
	var featureCommand []string
	var signingCommand []string
	finished := make(chan error, 1)
	go func() {
		host := setupMenuHost{frontend: management.Store{Root: root}}
		if kind == "install-many-failed" {
			failure := errors.New("download failed before installation")
			cursor := "browser"
			picker := newSetupToolsPicker(management.Report{Target: tools.Target{OS: "linux", Arch: "amd64", Mode: tools.Full}}, "Fixture Linux", false, &cursor)
			_, err := runSetupForm(ctx, huh.NewForm(huh.NewGroup(picker)), terminal, terminal)
			if err != nil {
				finished <- err
				return
			}
			changed, err := installSetupTools(ctx, picker.installations(), "Fixture Linux", terminal, terminal, func(command []string, _ io.Writer) error {
				if !reflect.DeepEqual(command, []string{"setup", "browser", "workspace"}) {
					return fmt.Errorf("wrong batch installation: %v", command)
				}
				return failure
			})
			if errors.Is(err, failure) {
				err = success(&commandOutput{Writer: &output, json: true}, "", map[string]any{"changed": changed})
			} else {
				err = fmt.Errorf("installation failure was lost: %v", err)
			}
			finished <- err
		} else if strings.HasPrefix(kind, "git-signing") {
			changed, err := setupGitSigning(ctx, "Fixture Linux", terminal, terminal, func(args []string, output, diagnostics io.Writer) error {
				if reflect.DeepEqual(args, []string{"--json", "integrations", "status", "git"}) {
					_, err := io.WriteString(output, `{"identity":{"name":"Saved Author","email":"saved@example.com"}}`)
					return err
				}
				signingCommand = append([]string{}, args...)
				_, err := io.WriteString(output, "Public signing key: ssh-ed25519 fixture")
				return err
			})
			if err == nil {
				err = success(&commandOutput{Writer: &output, json: true}, "", map[string]any{"changed": changed})
			}
			finished <- err
		} else if strings.HasPrefix(kind, "hosts-back") {
			_, changed, err := chooseSetupHost(ctx, host.frontend, terminal, terminal, false)
			if err == nil {
				err = success(&commandOutput{Writer: &output, json: true}, "", map[string]any{"changed": changed})
			}
			finished <- err
		} else if strings.HasPrefix(kind, "features-") {
			state := tools.State{Installed: true, Enabled: false, Capabilities: []string{"pdf"}}
			if kind == "features-all-result" {
				state.Capabilities = nil
				for _, feature := range browserFeatures {
					state.Capabilities = append(state.Capabilities, feature.id)
				}
			}
			changed, err := setupBrowserFeatures(ctx, state, "WSL: Ubuntu", terminal, terminal, func(args []string, log io.Writer) error { featureCommand = args; return nil })
			if err == nil {
				err = success(&commandOutput{Writer: &output, json: true}, "", map[string]any{"changed": changed})
			}
			finished <- err
		} else if kind == "readiness" {
			host.selected = &management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "ssh", Address: "fixture@host"}, Command: "loki"}
			err := setupReadiness(ctx, host, terminal, terminal)
			if err == nil {
				err = success(&commandOutput{Writer: &output, json: true}, "", map[string]any{"changed": false})
			}
			finished <- err
		} else if strings.HasPrefix(kind, "result") {
			summary := "Missing Linux library: libnspr4.so"
			if kind == "result-narrow" {
				summary += strings.Repeat("\nAdditional diagnostic summary", 10) + "\nFinal recovery instruction"
			}
			err := showSetupResult(ctx, "Readiness — needs attention", summary, strings.Repeat("Chrome fixture diagnostic line\n", 40), terminal, terminal)
			if err == nil {
				err = success(&commandOutput{Writer: &output, json: true}, "", map[string]any{"changed": false})
			}
			finished <- err
		} else if strings.HasPrefix(kind, "connections") {
			report := management.Report{Target: management.LocalTarget(tools.ProjectHost), Tools: map[tools.ID]tools.State{"browser": {Installed: true, Enabled: true}}}
			changed, err := setupConnections(ctx, host, report, terminal, terminal)
			if err == nil {
				err = success(&commandOutput{Writer: &output, json: true}, "", map[string]any{"changed": changed})
			}
			finished <- err
		} else if kind == "tools" || kind == "tools-install-cancel" || kind == "browser-menu-narrow" {
			finished <- runToolsMenu(ctx, []string{"--root", root}, host, terminal, &commandOutput{Writer: &output, json: true}, terminal)
		} else {
			host.explicit = kind == "home"
			if strings.HasPrefix(kind, "unreachable") || kind == "preparation-failed" || kind == "remote-home" {
				host.explicit = true
				host.selected = &management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "ssh", Address: "fixture@host"}, Command: "loki"}
			}
			finished <- runSetupMenu(ctx, []string{"--root", root}, host, terminal, &commandOutput{Writer: &output, json: true}, terminal)
		}
	}()
	if kind == "install-many-failed" {
		waitFor("Loki tools", "coordination")
		_, _ = io.WriteString(master, " \x1b[B \r")
		waitFor("Install and enable")
		_, _ = io.WriteString(master, "\r")
	} else if kind == "tools-install-cancel" {
		waitFor("Loki tools", "coordination")
		_, _ = io.WriteString(master, " \x1b[B \r")
		waitFor("browser, workspace", "Install and enable")
		_, _ = io.WriteString(master, "\x1b")
		waitFor("Loki tools", "coordination")
		_, _ = io.WriteString(master, "\x1b")
	} else if strings.HasPrefix(kind, "git-signing") {
		waitFor("Git signing name")
		if kind == "git-signing-cancel" {
			_, _ = io.WriteString(master, "\x1b")
		} else {
			_, _ = io.WriteString(master, "\r\r")
			waitFor("Configure commit signing?", "Saved Author", "saved@example.com")
			_, _ = io.WriteString(master, "\x1b[D\r")
			waitFor("Git signing — configured", "Public signing key")
			_, _ = io.WriteString(master, "\r")
		}
	} else if kind == "browser-menu-narrow" {
		waitFor("Loki tools")
		_, _ = io.WriteString(master, "\r")
		waitFor("Optional features — video recording")
		_, _ = io.WriteString(master, "\r")
		waitFor("Record WebM video")
		_, _ = io.WriteString(master, strings.Repeat("\x1b[B", len(browserFeatures)-1))
		waitFor("Allow code execution")
		_, _ = io.WriteString(master, "\x1b")
		waitFor("Loki tools")
		_, _ = io.WriteString(master, "\x1b")
	} else if strings.HasPrefix(kind, "features-") {
		transcript := waitFor("Record WebM video")
		if !strings.Contains(transcript, "Video recording") {
			t.Fatal("feature selector does not show the focused feature and its purpose", transcript)
		}
		if kind == "features-cancel" {
			_, _ = io.WriteString(master, "\x1b")
		} else if kind == "features-all-result" {
			_, _ = io.WriteString(master, "\r")
			waitFor("Browser features — unchanged", "Optional features: 14 selected.", "Details — read full output")
			_, _ = io.WriteString(master, "\x1b[B\r")
			waitFor("Page 1/")
			_, _ = io.WriteString(master, "\x1b[B\r")
			waitFor("Page 2/", "[unsafe-code]")
			_, _ = io.WriteString(master, "\x1b")
			// Details repeats the result title. Wait for the result's own
			// control before sending Enter, including split terminal frames.
			waitFor("Continue — return to menu")
			_, _ = io.WriteString(master, "\r")
		} else {
			_, _ = io.WriteString(master, " \r")
			waitFor("Browser features — saved")
			_, _ = io.WriteString(master, "\r")
			waitFor("Reconnect the client's Loki MCP server")
		}
	} else if kind == "readiness" {
		waitFor("Missing Linux library")
		_, _ = io.WriteString(master, "\x1b[B\r")
		waitFor("<launching>")
		_, _ = io.WriteString(master, "\x1b")
		waitFor("Missing Linux library")
		_, _ = io.WriteString(master, "\r")
		transcript := waitFor("[RESULT] Readiness — needs attention", "Codex connection not tested.")
		if !strings.Contains(transcript, "[RESULT] Readiness — needs attention") {
			t.Fatal("readiness result missing from restored terminal", transcript)
		}
	} else if strings.HasPrefix(kind, "result") {
		waitFor("Missing Linux library")
		if kind == "result-escape" {
			_, _ = io.WriteString(master, "\x1b")
		} else {
			_, _ = io.WriteString(master, "\x1b[B\r")
			waitFor("Page 1/")
			_, _ = io.WriteString(master, "\x1b[B\r")
			waitFor("Page 2/")
			_, _ = io.WriteString(master, "\x1b")
			waitFor("Missing Linux library")
			_, _ = io.WriteString(master, "\r")
		}
		lastLine := "  Missing Linux library: libnspr4.so"
		if kind == "result-narrow" {
			lastLine = "  Final recovery instruction"
		}
		waitFor("[RESULT] Readiness — needs attention", lastLine)
		transcript := completeTranscript.String()
		restored := strings.Index(transcript, "\x1b[?1049l")
		result := strings.Index(transcript, "[RESULT] Readiness — needs attention")
		if restored < 0 || result < restored {
			t.Fatal("result not preserved after leaving alternate screen", transcript)
		}
	} else if kind == "connections" {
		waitFor("Connect Codex")
		_, _ = io.WriteString(master, "\r")
		waitFor("Loki tools: browser")
		_, _ = io.WriteString(master, "\x1b")
		waitFor("Client connections", "Inspect client connections")
		_, _ = io.WriteString(master, "\x1b[B\r")
		waitFor("Loki client connections (on this computer)")
		_, _ = io.WriteString(master, "\r")
		waitFor("Client connections", "Inspect client connections")
		_, _ = io.WriteString(master, "\x1b")
	} else if kind == "connections-disconnect-unchanged" {
		waitFor("Client connections")
		_, _ = io.WriteString(master, "\x1b[B\x1b[B\r")
		waitFor("Disconnect Local", "from Codex?")
		_, _ = io.WriteString(master, "\x1b[D\r")
		waitFor("Codex — unchanged", "No Loki-managed connection")
		_, _ = io.WriteString(master, "\r")
		waitFor("Client connections")
		_, _ = io.WriteString(master, "\x1b")
	} else if kind == "connections-connect-unchanged" {
		waitFor("Client connections")
		_, _ = io.WriteString(master, "\r")
		waitFor("Connect Codex to", "Loki tools: browser")
		_, _ = io.WriteString(master, "\x1b[D\r")
		waitFor("Codex — unchanged", "already configured")
		_, _ = io.WriteString(master, "\r")
		waitFor("Client connections")
		_, _ = io.WriteString(master, "\x1b")
	} else if strings.HasPrefix(kind, "hosts-back") {
		transcript := waitFor("Choose execution host", "Set up another execution host")
		if strings.Contains(transcript, "managed services") || strings.Contains(transcript, "SSH Linux host") {
			t.Fatal("preparation routes mixed with saved hosts", transcript)
		}
		_, _ = io.WriteString(master, "\x1b[B\r")
		waitFor("Set up execution host", "SSH Linux host")
		_, _ = io.WriteString(master, "\x1b")
		waitFor("Choose execution host", "Set up another execution host")
		// Reopen the submenu to establish that Back returned to a live chooser.
		_, _ = io.WriteString(master, "\x1b[B\r")
		waitFor("Set up execution host", "SSH Linux host")
		_, _ = io.WriteString(master, strings.Repeat("\x1b[B", len(setupHostPreparationChoices(runtime.GOOS))-1)+"\r")
		waitFor("Choose execution host", "Set up another execution host")
		_, _ = io.WriteString(master, "\x1b")
	} else if kind == "fresh" {
		waitFor("Choose execution host")
		_, _ = io.WriteString(master, "\x1b")
	} else if kind == "remote-home" {
		waitFor("Manage tools")
		_, _ = io.WriteString(master, "\x1b")
	} else if kind == "unreachable-tools" {
		waitFor("Manage tools")
		_, _ = io.WriteString(master, "\r")
		waitFor("Host preparation — needs attention")
		_, _ = io.WriteString(master, "\r")
		waitFor("Manage tools")
		_, _ = io.WriteString(master, "\x1b")
	} else if kind == "unreachable" || kind == "preparation-failed" {
		if kind == "preparation-failed" {
			waitFor("Manage tools")
			_, _ = io.WriteString(master, "\r")
			waitFor("Host preparation — needs attention")
			_, _ = io.WriteString(master, "\x1b[B\r")
			waitFor("Page 1/", "SSH fixture authentication denied")
			_, _ = io.WriteString(master, "\x1b")
			waitFor("Continue — return to menu")
			_, _ = io.WriteString(master, "\r")
		}
		waitFor("Manage tools")
		_, _ = io.WriteString(master, "\x1b[B\r")
		waitFor("Inspect client connections")
		_, _ = io.WriteString(master, "\x1b[B\r")
		waitFor("codex: not configured")
		_, _ = io.WriteString(master, "\r")
		waitFor("Client connections", "Inspect client connections")
		_, _ = io.WriteString(master, "\x1b")
		waitFor("Manage tools")
		_, _ = io.WriteString(master, "\x1b")
	} else {
		if kind == "home" {
			waitFor("Manage tools")
			_, _ = io.WriteString(master, "\r")
		}
		waitFor("coordination")
		_, _ = io.WriteString(master, "\r")
		waitFor("Install only")
		_, _ = io.WriteString(master, "\x1b")
		waitFor("coordination")
		if kind == "tools" {
			_, _ = io.WriteString(master, strings.Repeat("\x1b[B", len(publicToolNames))+"\r")
			waitFor("Choose execution host")
			_, _ = io.WriteString(master, "\x1b")
			waitFor("coordination")
		}
		_, _ = io.WriteString(master, "\x1b")
		if kind == "home" {
			waitFor("Manage tools")
			_, _ = io.WriteString(master, "\x1b")
		}
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("menu did not exit")
	}
	fmt.Fprint(terminal, "SETUP_SESSION_CLOSED\r\n")
	waitFor("SETUP_SESSION_CLOSED")
	if kind == "home" || kind == "preparation-failed" || kind == "browser-menu-narrow" || strings.HasPrefix(kind, "result") {
		transcript := completeTranscript.String()
		if enters, exits := strings.Count(transcript, ansi.SetModeAltScreenSaveCursor), strings.Count(transcript, ansi.ResetModeAltScreenSaveCursor); enters != 1 || exits != 1 {
			t.Fatalf("stage transitions exposed the shell: %d screen entries, %d exits", enters, exits)
		}
	}
	if kind == "remote-home" {
		if calls, err := os.ReadFile(os.Getenv("LOKI_REVIEW_SSH_LOG")); !os.IsNotExist(err) {
			t.Fatalf("opening setup contacted the remote host: %s (%v)", calls, err)
		}
	}
	after, err := term.GetState(int(terminal.Fd()))
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("terminal was not restored", err)
	}
	if kind == "browser-menu-narrow" || kind == "connections-connect-unchanged" {
		after, err := (management.Store{Root: root}).Load()
		if err != nil || !reflect.DeepEqual(after, initialState) {
			t.Fatal("cancelled feature menu changed installation", err)
		}
		if kind == "connections-connect-unchanged" {
			config, _ := defaultCodexConfig()
			actual, err := os.ReadFile(config)
			if err != nil || !bytes.Equal(actual, initialConfig) {
				t.Fatal("repeated connection rewrote Codex settings", err)
			}
		}
	} else if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("cancelled menu changed installation", err)
	}
	var result struct {
		Changed bool `json:"changed"`
		Success bool `json:"success"`
	}
	wantChanged := kind == "features-save" || kind == "features-narrow" || strings.HasPrefix(kind, "git-signing") && kind != "git-signing-cancel"
	if err := json.Unmarshal(output.Bytes(), &result); err != nil || !result.Success || result.Changed != wantChanged {
		t.Fatalf("menu contaminated JSON output: %s (%v)", output.String(), err)
	}
	if strings.HasPrefix(kind, "git-signing") {
		want := []string{"integrations", "setup", "git", "--identity-name", "Saved Author", "--identity-email", "saved@example.com"}
		if kind == "git-signing-cancel" {
			want = nil
		}
		if !reflect.DeepEqual(signingCommand, want) {
			t.Fatal("signing configuration did not preserve the identity or mutated on cancel", signingCommand)
		}
	} else if wantChanged {
		if len(featureCommand) != 5 || featureCommand[1] != "disable" || !strings.Contains(featureCommand[4], "devtools") || !strings.Contains(featureCommand[4], "pdf") {
			t.Fatal("feature save lost checked choices or enabled browser", featureCommand)
		}
	} else if (kind == "features-cancel" || kind == "features-all-result") && featureCommand != nil {
		t.Fatal("unchanged or cancelled features were saved", featureCommand)
	}
}

func TestSetupKeyboardSelection(t *testing.T) {
	for _, test := range []struct {
		name string
		keys string
		want []tools.ID
	}{
		{"multiple and toggle", "\x1b[B \x1b[B\x1b[B \x1b[A\x1b[A  \r", []tools.ID{"workspace", "git"}},
		{"select all", "\x01\r", []tools.ID{"browser", "workspace", "execution", "git", "github", "secrets", "sharing", "coordination"}},
		{"empty confirmation", "\r", nil},
		{"cancel checked choice", " \x03", nil},
		{"escape", " \x1b", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			options, err := setupKeyboardPrompt(t, test.keys, false)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(options.selected, test.want) {
				t.Fatalf("selected %v, want %v", options.selected, test.want)
			}
		})
	}
}

func TestSetupKeyboardContextCancellation(t *testing.T) {
	_, err := setupKeyboardPrompt(t, "", true)
	if err == nil {
		t.Fatal("context cancellation was ignored")
	}
}

func setupKeyboardPrompt(t *testing.T, keys string, cancelPrompt bool) (setupOptions, error) {
	t.Helper()
	t.Setenv("TERM", "xterm-256color")
	master, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer terminal.Close()
	if err := pty.Setsize(terminal, &pty.Winsize{Rows: 30, Cols: 100}); err != nil {
		t.Fatal(err)
	}
	before, err := term.GetState(int(terminal.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	type result struct {
		options setupOptions
		err     error
	}
	finished := make(chan result, 1)
	go func() {
		options, err := parseSetup(ctx, nil, terminal, terminal)
		finished <- result{options, err}
	}()
	ready := make(chan struct{}, 1)
	go func() {
		var screen strings.Builder
		buffer := make([]byte, 4096)
		for {
			n, err := master.Read(buffer)
			screen.Write(buffer[:n])
			if strings.Contains(screen.String(), "coordination") {
				ready <- struct{}{}
				_, _ = io.Copy(io.Discard, master)
				return
			}
			if err != nil {
				return
			}
		}
	}()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("checkbox list did not render all eight tools")
	}
	if cancelPrompt {
		cancel()
	} else if _, err := io.WriteString(master, keys); err != nil {
		t.Fatal(err)
	}
	var completed result
	select {
	case completed = <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("keyboard confirmation/cancellation did not finish")
	}
	after, err := term.GetState(int(terminal.Fd()))
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("terminal mode was not restored: %v", err)
	}
	return completed.options, completed.err
}

func TestSetupSessionInputOwnership(t *testing.T) {
	for _, kind := range []string{"session-password", "session-cancel"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("TERM", "xterm-256color")
			master, terminal, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer terminal.Close()
			if err := pty.Setsize(terminal, &pty.Winsize{Rows: 30, Cols: 100}); err != nil {
				t.Fatal(err)
			}
			before, err := term.GetState(int(terminal.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			chunks := make(chan string, 32)
			stop := make(chan struct{})
			defer close(stop)
			go func() {
				buffer := make([]byte, 4096)
				for {
					n, err := master.Read(buffer)
					select {
					case chunks <- string(buffer[:n]):
					case <-stop:
						return
					}
					if err != nil {
						return
					}
				}
			}()
			var transcript strings.Builder
			waitFor := func(text string) {
				t.Helper()
				var current strings.Builder
				timeout := time.NewTimer(5 * time.Second)
				defer timeout.Stop()
				for !strings.Contains(ansi.Strip(current.String()), text) {
					select {
					case chunk := <-chunks:
						current.WriteString(chunk)
						transcript.WriteString(chunk)
					case <-timeout.C:
						t.Fatalf("input stage did not render %q: %s", text, current.String())
					}
				}
			}
			finished := make(chan struct{})
			go func() {
				setupSessionInputFixture(t, terminal, terminal, kind)
				fmt.Fprint(terminal, "INPUT_FIXTURE_CLOSED\r\n")
				close(finished)
			}()
			if kind == "session-cancel" {
				waitFor("Waiting for cancellation")
				io.WriteString(master, "\x03")
			} else {
				waitFor("Administrator password")
				// Background progress must leave the focused input usable.
				time.Sleep(20 * time.Millisecond)
				io.WriteString(master, "fixture-secret-password\r")
				waitFor("Password input — accepted")
				io.WriteString(master, "\r")
			}
			waitFor("INPUT_FIXTURE_CLOSED")
			<-finished
			after, err := term.GetState(int(terminal.Fd()))
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("session did not restore input", err)
			}
			if strings.Contains(transcript.String(), "fixture-secret-password") {
				t.Fatal("password appeared in terminal output")
			}
			want := 1
			if enters, exits := strings.Count(transcript.String(), ansi.SetModeAltScreenSaveCursor), strings.Count(transcript.String(), ansi.ResetModeAltScreenSaveCursor); enters != want || exits != want {
				t.Fatalf("unexpected screen lifecycle: %d entries, %d exits; want %d", enters, exits, want)
			}
		})
	}
}
