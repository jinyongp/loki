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
	"slices"
	"strings"
	"testing"

	"loki/internal/management"
	"loki/internal/tools"
)

func TestBrowserInstallationVerificationRetainsFilesAndActivationOnFailure(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			store := management.Store{Root: t.TempDir()}
			state, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			artifact := tools.Artifact{Module: "browser", Release: management.Release, Target: management.LocalTarget(tools.ProjectHost), URL: "https://example.com/browser.zip", SHA256: strings.Repeat("a", 64), Bytes: 1, Format: "zip"}
			manifest := tools.Manifest{Schema: 1, ID: "browser", Release: management.Release, Targets: []tools.Target{artifact.Target}, Tools: []string{}}
			generation, err := store.Generation(artifact)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(generation, 0700); err != nil {
				t.Fatal(err)
			}
			owner, _ := json.Marshal(management.Owner{Schema: 1, Identity: artifact.Identity()})
			module, _ := json.Marshal(manifest)
			for name, data := range map[string][]byte{".loki-owner.json": owner, "module.json": module, "fixture-data": []byte("retain")} {
				if err := os.WriteFile(filepath.Join(generation, name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			state.Installed["browser"] = management.Installation{Artifact: artifact, Manifest: manifest}
			state.Config.Tools = []tools.Selection{{ID: "browser", Enabled: enabled}}
			if err := store.Save(state); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			called := false
			err = verifyInstalledBrowserWithProbe(t.Context(), store, &output, func(context.Context, string) error {
				called = true
				return errors.New("libatomic.so.1 unavailable")
			})
			if !called || err == nil || !strings.Contains(err.Error(), "retained") || !strings.Contains(err.Error(), "libatomic.so.1") || strings.Contains(output.String(), "runtime verified") {
				t.Fatal("startup failure reported success or lost its cause", called, err, output.String())
			}
			after, err := store.Load()
			if err != nil || after.Config.Tools[0].Enabled != enabled || after.Installed["browser"].Artifact != artifact {
				t.Fatal("verification changed installation or activation", after, err)
			}
			data, err := os.ReadFile(filepath.Join(generation, "fixture-data"))
			if err != nil || string(data) != "retain" {
				t.Fatal("verification removed retained files", err)
			}
			if err := verifyInstalledBrowserWithProbe(t.Context(), store, &output, func(context.Context, string) error { return nil }); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), "Browser runtime verified.") {
				t.Fatal(output.String())
			}
		})
	}
}

func TestBrowserPackageNamesFollowDistributionABI(t *testing.T) {
	for _, test := range []struct {
		id, version, asound string
		supported           bool
	}{
		{"ubuntu", "22.04", "libasound2", true}, {"ubuntu", "24.04", "libasound2t64", true},
		{"debian", "12", "libasound2", true}, {"debian", "13", "libasound2t64", true},
		{"ubuntu", "20.04", "", false}, {"fedora", "43", "", false},
	} {
		packages, supported := browserSystemPackages([]byte(fmt.Sprintf("ID=%s\nVERSION_ID=\"%s\"\n", test.id, test.version)))
		if supported != test.supported || supported && (!slices.Contains(packages, test.asound) || !slices.Contains(packages, "libatomic1") || !slices.Contains(packages, "libnspr4") || !slices.Contains(packages, "libnss3")) {
			t.Fatal(test, packages, supported)
		}
	}
}

func TestRemoteAcquisitionAcceptsExecutionHostArchivePaths(t *testing.T) {
	// These arguments must also pass on the Windows frontend. The remote Linux
	// CLI validates its own path again before reading any receipt-bound archive.
	if full, err := fullInstallationRequest([]string{"browser", "--catalog", "/tmp/catalog.json", "--archives", "/tmp/archives"}); err != nil || full {
		t.Fatal("Linux execution-host path rejected by frontend", full, err)
	}
	if err := validateToolUpdateRequest([]string{"--catalog", "/tmp/catalog.json", "--archives", "/tmp/archives"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fullInstallationRequest([]string{"browser", "--archives", "relative/archives"}); err == nil {
		t.Fatal("relative archive path accepted")
	}
}

func TestBrowserEnvironmentInstallsOnlyMissingPackagesAndRechecks(t *testing.T) {
	for _, kind := range []string{"ready", "root", "password", "cached", "install-failed", "still-missing", "invalid-password"} {
		t.Run(kind, func(t *testing.T) {
			missing := kind != "ready"
			authorized := kind == "cached"
			var calls []string
			var authInput string
			deps := browserEnvironmentDependencies{os: "linux", uid: func() int {
				if kind == "root" {
					return 0
				}
				return 1000
			}, readFile: func(string) ([]byte, error) { return []byte("ID=ubuntu\nVERSION_ID=24.04\n"), nil }}
			deps.run = func(_ context.Context, binary string, args []string, input io.Reader, out, diagnostics io.Writer) error {
				call := binary + " " + strings.Join(args, " ")
				calls = append(calls, call)
				if binary == "/usr/bin/dpkg-query" {
					for _, name := range args[2:] {
						if name != "libnspr4" || !missing {
							fmt.Fprintln(out, name, "installed")
						}
					}
					return nil
				}
				if binary == "/usr/bin/sudo" && slices.Contains(args, "true") {
					if !authorized {
						return errors.New("authorization needed")
					}
					return nil
				}
				if binary == "/usr/bin/sudo" && slices.Contains(args, "-S") {
					data, _ := io.ReadAll(input)
					authInput = string(data)
					authorized = true
					return nil
				}
				if slices.Contains(args, "install") {
					if len(args) == 0 || args[len(args)-1] != "libnspr4" || slices.Contains(args, "libnss3") {
						t.Fatal("installed non-missing packages", args)
					}
					if kind == "install-failed" {
						return errors.New("apt unavailable")
					}
					if kind != "still-missing" {
						missing = false
					}
				}
				return nil
			}
			var password []byte
			if kind == "password" || kind == "install-failed" || kind == "still-missing" {
				password = []byte("fixture-password")
			}
			if kind == "invalid-password" {
				password = []byte("bad\npassword")
			}
			var diagnostics bytes.Buffer
			err := prepareBrowserEnvironment(t.Context(), password, &diagnostics, deps)
			wantError := kind == "install-failed" || kind == "still-missing" || kind == "invalid-password"
			if (err != nil) != wantError {
				t.Fatal(kind, err, calls)
			}
			if kind == "ready" && len(calls) != 1 {
				t.Fatal("ready host invoked administrator commands", calls)
			}
			if kind == "password" && authInput != "fixture-password\n" {
				t.Fatal("password was not restricted to sudo stdin")
			}
			if strings.Contains(strings.Join(calls, " ")+diagnostics.String(), "fixture-password") {
				t.Fatal("password leaked outside stdin")
			}
			if kind == "invalid-password" && authInput != "" {
				t.Fatal("invalid administrator material was sent")
			}
		})
	}
}

func TestRemoteBrowserPreparationProtocolAndNoOp(t *testing.T) {
	for _, kind := range []string{"ready", "missing", "full", "invalid", "prepare-failed"} {
		t.Run(kind, func(t *testing.T) {
			selected := management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "ssh", Address: "fixture@host"}, Command: "loki", Root: "/home/fixture/.config/loki"}
			var calls []string
			run := func(_ context.Context, got management.ExecutionSelection, args []string, input io.Reader, out, diagnostics io.Writer) error {
				if got != selected || len(args) != 3 || args[0] != "--root" || args[1] != selected.Root {
					t.Fatal("execution target changed", got, args)
				}
				calls = append(calls, args[2])
				if args[2] == "_browser-environment" {
					response := `{"schema":1,"applicable":true,"supported":true,"missing_packages":[],"administrator_required":false}`
					if kind == "missing" || kind == "prepare-failed" {
						response = `{"schema":1,"applicable":true,"supported":true,"missing_packages":["libnspr4"],"administrator_required":false}`
					}
					if kind == "full" {
						response = `{"schema":1,"applicable":false,"supported":false,"missing_packages":[],"administrator_required":false}`
					}
					if kind == "invalid" {
						response = `{}`
					}
					fmt.Fprint(out, response)
				} else if kind == "prepare-failed" {
					return errors.New("apt failed")
				}
				return nil
			}
			err := prepareRemoteBrowserHost(t.Context(), selected, strings.NewReader(""), io.Discard, run)
			if (err != nil) != (kind == "invalid" || kind == "prepare-failed") {
				t.Fatal(kind, err)
			}
			want := []string{"_browser-environment"}
			if kind == "missing" || kind == "prepare-failed" {
				want = append(want, "_prepare-browser-environment")
			}
			if !slices.Equal(calls, want) {
				t.Fatal(calls, want)
			}
		})
	}
}
