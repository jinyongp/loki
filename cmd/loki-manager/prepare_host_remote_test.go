package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"loki/internal/management"
	"loki/internal/tools"
)

func TestUserManagedHostPreparation(t *testing.T) {
	for _, kind := range []string{"wsl", "ssh"} {
		for _, scenario := range []string{"existing", "outdated", "missing", "resume", "unreachable", "download-failed", "install-failed", "verification-failed"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				store := management.Store{Root: t.TempDir()}
				original := management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "local"}, Command: "loki"}
				if err := store.SelectExecutionHost(original); err != nil {
					t.Fatal(err)
				}
				selected := management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "wsl", Distribution: "Ubuntu"}, Command: "loki", User: "example"}
				if kind == "ssh" {
					selected.Host = tools.Host{Kind: "ssh", Address: "example@host"}
					selected.User = ""
				}
				installed, downloads, installs := false, 0, 0
				deps := remotePreparationDependencies{
					download: func(_ context.Context, arch string) ([]byte, error) {
						downloads++
						if arch != "arm64" {
							t.Fatalf("host architecture lost: %s", arch)
						}
						if scenario == "download-failed" {
							return nil, fmt.Errorf("download failed")
						}
						return []byte("verified Linux manager"), nil
					},
					run: func(_ context.Context, host management.ExecutionSelection, args []string, input io.Reader, out, _ io.Writer) error {
						if host.Host != selected.Host || host.User != selected.User || host.Owned {
							t.Fatalf("host identity changed: %+v", host)
						}
						if len(args) == 1 && args[0] == "version" {
							if scenario == "existing" || installed && scenario != "verification-failed" || scenario == "resume" && strings.HasPrefix(host.Command, "/home/") {
								fmt.Fprintln(out, "loki", management.ManagerRelease)
								return nil
							}
							if scenario == "outdated" {
								fmt.Fprintln(out, "loki 0.2.1")
								return nil
							}
							return fmt.Errorf("CLI missing")
						}
						if host.Command != "/bin/sh" {
							t.Fatalf("probe must use Linux shell: %s", host.Command)
						}
						if input == nil {
							if scenario == "unreachable" {
								return fmt.Errorf("WSL unavailable")
							}
							fmt.Fprint(out, "Linux\naarch64\n/home/example user\n")
							return nil
						}
						installs++
						binary, _ := io.ReadAll(input)
						if string(binary) != "verified Linux manager" || len(args) != 6 || args[4] != "/home/example user/.config/loki" || args[5] != "/home/example user/.local/lib/loki/bin" {
							t.Fatalf("wrong installation input: %q, %v", binary, args)
						}
						if scenario == "install-failed" {
							return fmt.Errorf("disk full")
						}
						installed = true
						return nil
					},
				}
				err := selectPreparedUserHost(t.Context(), store, selected, io.Discard, io.Discard, deps)
				failed := strings.Contains(scenario, "failed") || scenario == "unreachable"
				if (err != nil) != failed {
					t.Fatalf("unexpected result: %v", err)
				}
				actual, readErr := store.ExecutionSelection()
				if readErr != nil {
					t.Fatal(readErr)
				}
				if failed {
					if *actual != original {
						t.Fatalf("failed preparation changed selection: %+v", actual)
					}
				} else {
					if actual.Host != selected.Host || actual.Owned || actual.User != selected.User {
						t.Fatalf("incorrect selected host: %+v", actual)
					}
					if scenario != "existing" && actual.Command != "/home/example user/.local/lib/loki/bin/loki" {
						t.Fatalf("private executable not remembered: %+v", actual)
					}
				}
				if scenario == "existing" || scenario == "resume" || scenario == "unreachable" {
					if downloads != 0 || installs != 0 {
						t.Fatal("unnecessary installation")
					}
				}
				if scenario == "unreachable" && !strings.Contains(err.Error(), "cannot access") {
					t.Fatal(err)
				}
				if proof, err := store.HostPreparation(); err != nil || proof != nil {
					t.Fatalf("user distribution acquired ownership: %+v %v", proof, err)
				}
			})
		}
	}
}
