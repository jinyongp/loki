package main

import (
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
	"loki/internal/tools"
)

func TestSetupSelectionPreservesNextWizardInput(t *testing.T) {
	input := strings.NewReader("workspace,git\nNEXT\n")
	options, err := parseSetup(t.Context(), nil, input, io.Discard)
	if err != nil || options.mode != tools.Full || len(options.selected) != 2 {
		t.Fatalf("selection: %+v %v", options, err)
	}
	next, _ := io.ReadAll(input)
	if string(next) != "NEXT\n" {
		t.Fatalf("wizard input consumed: %q", next)
	}
	for _, args := range [][]string{{"unknown"}, {"git", "git"}, {"git", "--mode", "project-host"}, {"git", "--version", "bad"}, {"browser", "--catalog", "x", "--version", "0.2.5"}} {
		if _, err := parseSetup(t.Context(), args, strings.NewReader(""), io.Discard); err == nil {
			t.Fatalf("invalid setup accepted: %v", args)
		}
	}
}

func TestEmptySetupDoesNotCreateState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-created")
	options, err := parseSetup(t.Context(), nil, strings.NewReader(""), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runSetup(t.Context(), management.Store{Root: root}, options, strings.NewReader(""), &commandOutput{Writer: &output, json: true}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("cancel changed filesystem: %v", err)
	}
	if singleJSON(t, output.Bytes())["state"] != "cancelled" {
		t.Fatal(output.String())
	}
}

type catalogRoundTripper func(*http.Request) (*http.Response, error)

func (f catalogRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAutomaticCatalogRequiresMatchingReleaseChecksum(t *testing.T) {
	catalog := tools.Catalog{Schema: 1, Release: management.ManagerRelease}
	data, _ := json.Marshal(catalog)
	target := management.LocalTarget(tools.ProjectHost)
	asset := fmt.Sprintf("loki-catalog-%s-%s-project-host.json", target.OS, target.Arch)
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprint(corrupt), func(t *testing.T) {
			client := &http.Client{Transport: catalogRoundTripper(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "github.com" || !strings.Contains(r.URL.Path, "/releases/download/v"+management.ManagerRelease+"/") {
					t.Fatal(r.URL)
				}
				body := data
				if strings.HasSuffix(r.URL.Path, "SHA256SUMS") {
					digest := sha256.Sum256(data)
					if corrupt {
						digest[0] ^= 1
					}
					body = []byte(fmt.Sprintf("%x  %s\n", digest, asset))
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
			})}
			_, err := acquireCatalogForMode(context.Background(), tools.ProjectHost, "", "", io.Discard, client)
			if corrupt && err == nil {
				t.Fatal("tampered catalog accepted")
			}
			if !corrupt && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRemoteGitHubFilesBecomeProtectedStdin(t *testing.T) {
	root := t.TempDir()
	unlock, err := (management.Store{Root: root}).Lock()
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	key := filepath.Join(root, "key")
	if err := os.WriteFile(key, []byte("private-material"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := protectTestCredentialFile(key); err != nil {
		t.Fatal(err)
	}
	args, input, cleanup, err := remoteIntegrationInput([]string{"integrations", "setup", "git", "--identity-name", "Example", "--key-file", key}, strings.NewReader("unused"))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	for _, arg := range args {
		if arg == key || strings.Contains(arg, "private-material") {
			t.Fatal("file or secret leaked into argv")
		}
	}
	material, _ := io.ReadAll(input)
	if string(material) != "private-material" || !strings.Contains(strings.Join(args, " "), "--key-stdin") {
		t.Fatal("protected import missing")
	}
}

func TestInterruptedPreparationProofSurvivesFailedPurge(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("WSL removal requires a disposable owned host")
	}
	store := management.Store{Root: t.TempDir()}
	proof := management.HostPreparation{Schema: 1, Distribution: "loki-tools", Token: strings.Repeat("a", 64), Phase: "reserved"}
	if err := store.SaveHostPreparation(proof); err != nil {
		t.Fatal(err)
	}
	if err := runHosts(t.Context(), store, []string{"remove", "--purge"}, io.Discard, io.Discard); err == nil {
		t.Fatal("unsupported owned-host removal unexpectedly succeeded")
	}
	if remaining, err := store.HostPreparation(); err != nil || remaining == nil || *remaining != proof {
		t.Fatalf("failed removal discarded preparation proof: %+v %v", remaining, err)
	}
}
