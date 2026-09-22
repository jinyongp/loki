package bootstrap

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loki/internal/host/releases"
)

type cleanHostRunner struct {
	workdir string
	home    string
	marker  string
}

func (r cleanHostRunner) Run(ctx context.Context, executable string, args []string) error {
	command := exec.CommandContext(ctx, executable, args...)
	command.Dir = r.workdir
	command.Env = []string{
		"HOME=" + r.home,
		"PATH=" + filepath.Join(r.workdir, "bin"),
		"LOKI_ACCEPTANCE_MARKER=" + r.marker,
	}
	return command.Run()
}

func sourceFreeBootstrapFixture(t *testing.T, binary []byte) *fakeReleaseSource {
	t.Helper()
	now := time.Date(2026, 9, 22, 6, 30, 0, 0, time.UTC)
	manifest, manifestRaw := bootstrapManifest(t, "3.0.0", now, binary)
	manifestDescriptor := bootstrapDescriptor("releases/manifests/3.0.0.json", manifestRaw)
	entry, err := releases.IndexEntryForManifest(manifest, manifestDescriptor)
	if err != nil {
		t.Fatal(err)
	}
	index, err := releases.NewReleaseIndex([]releases.ReleaseIndexEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	indexRaw, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeReleaseSource{targets: map[string]struct {
		descriptor releases.TargetDescriptor
		raw        []byte
	}{
		"index.json": {
			descriptor: bootstrapDescriptor("releases/index.json", indexRaw),
			raw:        indexRaw,
		},
		"manifests/3.0.0.json": {descriptor: manifestDescriptor, raw: manifestRaw},
		"bin/loki-3.0.0":       {descriptor: manifest.HostBinary, raw: binary},
	}}
}

func TestSourceFreeBootstrapAcceptanceUbuntuAndWSL(t *testing.T) {
	for _, environment := range []string{"native", "wsl"} {
		t.Run(environment, func(t *testing.T) {
			cleanRoot := t.TempDir()
			home := filepath.Join(cleanRoot, "home")
			if err := os.Mkdir(home, 0700); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(cleanRoot, "handoff.txt")
			binary := []byte("#!/bin/sh\n" +
				"set -eu\n" +
				"test ! -e ./go.mod\n" +
				"test ! -e ./package.json\n" +
				"test ! -e ./pyproject.toml\n" +
				"if command -v go >/dev/null 2>&1 || command -v node >/dev/null 2>&1 || command -v python >/dev/null 2>&1 || command -v python3 >/dev/null 2>&1; then exit 41; fi\n" +
				"test \"$1\" = host\n" +
				"test \"$2\" = install\n" +
				"test \"$3\" = --bootstrap-release-manifest\n" +
				"test -f \"$4\"\n" +
				"shift 4\n" +
				"printf '%s\\n' \"$@\" > \"$LOKI_ACCEPTANCE_MARKER\"\n")
			source := sourceFreeBootstrapFixture(t, binary)
			host := releases.SupportedHost{
				Environment: environment, Distribution: "ubuntu", Version: "24.04", Arch: "amd64",
			}
			stateRoot := filepath.Join(cleanRoot, "state")
			workspace := filepath.Join(cleanRoot, "workspace")
			args := []string{"--workspace", workspace}
			if environment == "wsl" {
				args = append([]string{"--system"}, args...)
			}
			candidate, err := Run(t.Context(), Config{
				StateRoot:        stateRoot,
				RequestedRelease: "3.0.0",
				Host:             &host,
				InstallArgs:      args,
				Runner: cleanHostRunner{
					workdir: cleanRoot, home: home, marker: marker,
				},
				Source: source,
			})
			if err != nil {
				t.Fatal(err)
			}
			if candidate.Host != host || candidate.Entry.Release != "3.0.0" {
				t.Fatalf("bootstrap candidate = %#v", candidate)
			}
			raw, err := os.ReadFile(marker)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Fields(string(raw))
			want := []string{"--workspace", workspace}
			if environment == "wsl" {
				want = append([]string{"--system"}, want...)
			}
			if strings.Join(lines, "\x00") != strings.Join(want, "\x00") {
				t.Fatalf("installer handoff = %#v, want %#v", lines, want)
			}
			entries, err := os.ReadDir(stateRoot)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".install-") {
					t.Fatalf("bootstrap staging survived successful handoff: %s", entry.Name())
				}
			}
		})
	}
}
