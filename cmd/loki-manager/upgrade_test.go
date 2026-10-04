package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"loki/internal/management"
)

type upgradeTransport func(*http.Request) (*http.Response, error)

func (f upgradeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func upgradeFixture(t *testing.T, target string, corrupt bool) (upgradeDependencies, *[]string, *[]string) {
	t.Helper()
	var archive bytes.Buffer
	packed := zip.NewWriter(&archive)
	name := "loki"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	entry, err := packed.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	entry.Write([]byte("candidate binary"))
	if err := packed.Close(); err != nil {
		t.Fatal(err)
	}
	asset := "loki-manager-" + target + "-" + runtime.GOOS + "-" + runtime.GOARCH + ".zip"
	digest := sha256.Sum256(archive.Bytes())
	sums := fmt.Sprintf("%x  %s\n", digest, asset)
	if corrupt {
		sums = strings.Repeat("0", 64) + "  " + asset + "\n"
	}
	executable := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(executable, []byte("old"), 0700); err != nil {
		t.Fatal(err)
	}
	var downloads, runs []string
	deps := upgradeDependencies{
		client: &http.Client{Transport: upgradeTransport(func(r *http.Request) (*http.Response, error) {
			downloads = append(downloads, r.URL.String())
			var body []byte
			switch {
			case r.URL.Host == "api.github.com":
				body, _ = json.Marshal(upgradeRelease{Tag: "v" + target})
			case strings.HasSuffix(r.URL.Path, "/SHA256SUMS"):
				body = []byte(sums)
			case strings.HasSuffix(r.URL.Path, "/"+asset):
				body = archive.Bytes()
			default:
				t.Errorf("unexpected request: %s", r.URL)
				return nil, fmt.Errorf("unexpected request")
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body))}, nil
		})},
		executable: func() (string, error) { return executable, nil },
		run: func(ctx context.Context, binary string, args ...string) ([]byte, error) {
			runs = append(runs, strings.Join(args, " "))
			if data, err := os.ReadFile(binary); err != nil || string(data) != "candidate binary" {
				t.Errorf("unverified candidate: %q %v", data, err)
			}
			if len(args) == 1 && args[0] == "version" {
				return []byte("loki " + target + "\n"), nil
			}
			return nil, nil
		},
	}
	return deps, &downloads, &runs
}

func TestUpgradeConfirmationAndOptions(t *testing.T) {
	for _, test := range []struct {
		name     string
		args     []string
		input    string
		installs bool
		requests int
	}{
		{"yes", nil, "y\n", true, 3},
		{"enter", nil, "\n", false, 1},
		{"no", nil, "no\n", false, 1},
		{"eof", nil, "y", false, 1},
		{"check", []string{"--check"}, "", false, 1},
		{"unattended", []string{"--yes"}, "", true, 3},
		{"version", []string{"--version", "v9.0.0", "-y"}, "", true, 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			deps, downloads, runs := upgradeFixture(t, "9.0.0", false)
			root := filepath.Join(t.TempDir(), "absent")
			var output, diagnostics bytes.Buffer
			if err := runUpgrade(t.Context(), management.Store{Root: root}, test.args, strings.NewReader(test.input), &output, &diagnostics, deps); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), "Current: "+management.Release) || !strings.Contains(output.String(), "Target:  9.0.0") {
				t.Fatal(output.String())
			}
			if len(*downloads) != test.requests || (len(*runs) == 2) != test.installs {
				t.Fatalf("requests=%v runs=%v", *downloads, *runs)
			}
			if test.installs && !strings.Contains((*runs)[1], "--root "+root+" install --bin-dir ") {
				t.Fatal(*runs)
			}
			if !test.installs {
				if _, err := os.Stat(root); !os.IsNotExist(err) {
					t.Fatalf("cancel/check mutated state: %v", err)
				}
			}
		})
	}
}

func TestUpgradeReinstallDowngradeAndChecksum(t *testing.T) {
	for _, test := range []struct {
		name, target                 string
		args                         []string
		corrupt, wantError, installs bool
	}{
		{"current", management.Release, []string{"--yes"}, false, false, false},
		{"reinstall", management.Release, []string{"--yes", "--force"}, false, false, true},
		{"downgrade", "0.2.1", []string{"--yes"}, false, true, false},
		{"forced downgrade", "0.2.1", []string{"--yes", "--force"}, false, false, true},
		{"corrupt", "9.0.0", []string{"--yes"}, true, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			deps, _, runs := upgradeFixture(t, test.target, test.corrupt)
			var output, diagnostics bytes.Buffer
			err := runUpgrade(t.Context(), management.Store{Root: t.TempDir()}, test.args, strings.NewReader(""), &output, &diagnostics, deps)
			if (err != nil) != test.wantError || (len(*runs) == 2) != test.installs {
				t.Fatalf("error=%v runs=%v", err, *runs)
			}
		})
	}
}

func TestUpgradeInvalidInputsNeverContactNetwork(t *testing.T) {
	for _, args := range [][]string{{"--version", "../bad"}, {"--version", "0.2.3-preview"}, {"--timeout", "0s"}, {"extra"}} {
		deps, downloads, _ := upgradeFixture(t, "9.0.0", false)
		if err := runUpgrade(t.Context(), management.Store{Root: t.TempDir()}, args, strings.NewReader(""), io.Discard, io.Discard, deps); err == nil || len(*downloads) != 0 {
			t.Fatalf("invalid args %q: %v requests=%v", args, err, *downloads)
		}
	}
}

func TestUpgradeHTTPFailureAndVersionMismatch(t *testing.T) {
	deps, _, runs := upgradeFixture(t, "9.0.0", false)
	deps.client = &http.Client{Transport: upgradeTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader("denied"))}, nil
	})}
	if err := runUpgrade(t.Context(), management.Store{Root: t.TempDir()}, []string{"--yes"}, strings.NewReader(""), io.Discard, io.Discard, deps); err == nil || len(*runs) != 0 {
		t.Fatalf("HTTP failure: %v", err)
	}
	deps, _, runs = upgradeFixture(t, "9.0.0", false)
	deps.run = func(context.Context, string, ...string) ([]byte, error) { return []byte("loki 9.9.9"), nil }
	if err := runUpgrade(t.Context(), management.Store{Root: t.TempDir()}, []string{"--yes"}, strings.NewReader(""), io.Discard, io.Discard, deps); err == nil || len(*runs) != 0 {
		t.Fatalf("candidate mismatch: %v", err)
	}
}
