//go:build linux || darwin

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
	"loki/internal/management"
	"loki/internal/tools"
)

func TestWSLAdministratorPasswordUsesProtectedStdinAndContinues(t *testing.T) {
	for _, kind := range []string{"full", "browser"} {
		t.Run(kind, func(t *testing.T) { testWSLAdministratorPassword(t, kind) })
	}
}

func testWSLAdministratorPassword(t *testing.T, kind string) {
	master, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer terminal.Close()
	before, err := term.GetState(int(terminal.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	selected := management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "wsl", Distribution: "Ubuntu"}, Command: "loki"}
	const password = "test-administrator-password"
	var diagnostics bytes.Buffer
	var calls []string
	run := func(_ context.Context, _ management.ExecutionSelection, args []string, input io.Reader, out, _ io.Writer) error {
		call := strings.Join(args, " ")
		calls = append(calls, call)
		switch call {
		case "_browser-environment":
			fmt.Fprintln(out, `{"schema":1,"applicable":true,"supported":true,"missing_packages":["libnspr4"],"administrator_required":true}`)
			return nil
		case "_host-authorization":
			fmt.Fprintln(out, `{"administrator_required":true,"prepared":false}`)
			return nil
		case "_prepare-host-relay", "_prepare-browser-environment":
			material, err := io.ReadAll(input)
			if err != nil || string(material) != password {
				return fmt.Errorf("password was not delivered through protected stdin")
			}
			if call == "_prepare-browser-environment" {
				return nil
			}
			return json.NewEncoder(out).Encode(management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "local"}, System: true, Owned: true, Root: "/var/lib/loki-tools/1000", Command: "/usr/local/lib/loki-tools/1000/bin/loki", Socket: "/run/loki-tools-manager-1000.sock"})
		default:
			return fmt.Errorf("unexpected additional preparation: %s", call)
		}
	}
	finished := make(chan error, 1)
	go func() {
		if kind == "browser" {
			finished <- prepareRemoteBrowserHost(ctx, selected, terminal, &diagnostics, run)
		} else {
			finished <- prepareRemoteFullHostWithRunner(ctx, selected, terminal, &diagnostics, run)
		}
	}()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	// Wait for ReadPassword to disable terminal echo before sending input.
	for {
		current, err := term.GetState(int(terminal.Fd()))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, current) {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("password prompt did not disable echo")
		case <-ticker.C:
		}
	}
	if _, err := io.WriteString(master, password+"\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("administrator preparation did not finish")
	}
	if strings.Contains(diagnostics.String(), password) || strings.Contains(strings.Join(calls, " "), password) {
		t.Fatal("password exposed in output or arguments")
	}
	after, err := term.GetState(int(terminal.Fd()))
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("password input did not restore terminal state", err)
	}
}
