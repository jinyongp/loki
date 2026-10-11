package main

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"loki/internal/management"
	"loki/internal/tools"
)

func TestRemoteFullPreparationUsesSamePolicyForWSLAndSSH(t *testing.T) {
	for _, kind := range []string{"wsl", "ssh"} {
		for _, test := range []struct {
			name, authorization        string
			wantPreparation, wantError bool
		}{
			{"already prepared", `{"administrator_required":false,"prepared":true}`, false, false},
			{"cached sudo", `{"administrator_required":false,"prepared":false}`, true, false},
			{"password needs terminal", `{"administrator_required":true,"prepared":false}`, false, true},
			{"invalid response", `{}`, false, true},
			{"contradictory response", `{"administrator_required":true,"prepared":true}`, false, true},
		} {
			t.Run(kind+"/"+test.name, func(t *testing.T) {
				selected := management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "wsl", Distribution: "Ubuntu"}, Root: "/home/example/.config/loki", Command: "/home/example/.local/lib/loki/bin/loki"}
				if kind == "ssh" {
					selected.Host = tools.Host{Kind: "ssh", Address: "example@host"}
				}
				var calls []string
				run := func(_ context.Context, host management.ExecutionSelection, args []string, _ io.Reader, out, _ io.Writer) error {
					if host != selected {
						t.Fatalf("transport identity changed: %+v", host)
					}
					if len(args) < 3 || args[0] != "--root" || args[1] != selected.Root {
						t.Fatalf("remote root lost: %v", args)
					}
					call := strings.Join(args[2:], " ")
					calls = append(calls, call)
					if call == "_host-authorization" {
						fmt.Fprintln(out, test.authorization)
						return nil
					}
					if call != "hosts prepare local" {
						t.Fatalf("unexpected remote operation: %v", args)
					}
					return nil
				}
				err := prepareRemoteFullHostWithRunner(t.Context(), selected, strings.NewReader(""), io.Discard, run)
				if (err != nil) != test.wantError {
					t.Fatalf("unexpected result: %v", err)
				}
				want := []string{"_host-authorization"}
				if test.wantPreparation {
					want = append(want, "hosts prepare local")
				}
				if !reflect.DeepEqual(calls, want) {
					t.Fatalf("calls %v, want %v", calls, want)
				}
			})
		}
	}
}

func TestManagedTunnelHostSupport(t *testing.T) {
	for _, test := range []struct {
		name     string
		selected *management.ExecutionSelection
		want     bool
	}{
		{"unselected", nil, false},
		{"local", &management.ExecutionSelection{Host: tools.Host{Kind: "local"}}, false},
		{"user WSL", &management.ExecutionSelection{Host: tools.Host{Kind: "wsl", Distribution: "Ubuntu"}}, false},
		{"owned WSL", &management.ExecutionSelection{Host: tools.Host{Kind: "wsl", Distribution: "loki-tools"}, Owned: true}, true},
		{"SSH", &management.ExecutionSelection{Host: tools.Host{Kind: "ssh", Address: "example@host"}}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := supportsManagedTunnel(test.selected); got != test.want {
				t.Fatalf("support = %v, want %v", got, test.want)
			}
		})
	}
}
