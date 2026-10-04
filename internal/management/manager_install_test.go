package management

import (
	"context"
	"fmt"
	"loki/internal/tools"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestManagerPublicationProtectsUnownedCommands(t *testing.T) {
	s := Store{Root: t.TempDir()}
	source := filepath.Join(t.TempDir(), "candidate")
	if err := os.WriteFile(source, []byte("candidate binary"), 0755); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	name := "loki"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(bin, name)
	if err := os.WriteFile(path, []byte("existing user command"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InstallManager(context.Background(), source, bin); err == nil {
		t.Fatal("replaced an unowned user command")
	}
	if bytes, err := os.ReadFile(path); err != nil || string(bytes) != "existing user command" {
		t.Fatal("unowned command changed")
	}
}

func TestManagerExecutableRejectsCopiedOwnership(t *testing.T) {
	store := Store{Root: t.TempDir()}
	source := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(source, []byte("owned binary"), 0755); err != nil {
		t.Fatal(err)
	}
	owned, err := store.InstallManager(t.Context(), source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(t.TempDir(), filepath.Base(owned.Executable))
	if err := os.WriteFile(clone, []byte("owned binary"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(clone+".loki-owner.json", owned); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ManagerExecutable(clone); err == nil {
		t.Fatal("copied owner record redirected executable publication")
	}
	if actual, err := store.ManagerExecutable(owned.Executable); err != nil || actual != owned.Executable {
		t.Fatalf("owned command: %q %v", actual, err)
	}
}

func TestManagerExecutablePreservesParentAlias(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows native upgrade acceptance covers short/long paths")
	}
	parent := t.TempDir()
	realParent := filepath.Join(parent, "real")
	aliasParent := filepath.Join(parent, "alias")
	if err := os.Mkdir(realParent, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realParent, aliasParent); err != nil {
		t.Fatal(err)
	}
	store := Store{Root: t.TempDir()}
	source := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(source, []byte("owned binary"), 0755); err != nil {
		t.Fatal(err)
	}
	owned, err := store.InstallManager(t.Context(), source, filepath.Join(aliasParent, "bin"))
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(owned.Executable)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := store.ManagerExecutable(resolved)
	if err != nil || actual != owned.Executable {
		t.Fatalf("recorded alias not retained: %q %v", actual, err)
	}
}

func TestManagerPublicationRecordsVerifiedTargetRelease(t *testing.T) {
	store := Store{Root: t.TempDir()}
	source := filepath.Join(t.TempDir(), "candidate")
	if err := os.WriteFile(source, []byte("target binary"), 0755); err != nil {
		t.Fatal(err)
	}
	record, err := store.InstallManagerVersion(t.Context(), source, t.TempDir(), "0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if record.Release != "0.2.1" || state.Config.Release != "0.2.1" {
		t.Fatalf("target release not preserved: %+v %+v", record, state.Config)
	}
	publication, err := store.readManagerPublication()
	if err != nil || publication.Candidate.Release != "0.2.1" {
		t.Fatalf("journal release: %+v %v", publication, err)
	}
}

func TestManagerRecoveryRestoresJournalOwnedBackup(t *testing.T) {
	store := Store{Root: t.TempDir()}
	directory := t.TempDir()
	source := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(source, []byte("old binary"), 0755); err != nil {
		t.Fatal(err)
	}
	previous, err := store.InstallManager(t.Context(), source, directory)
	if err != nil {
		t.Fatal(err)
	}
	candidate := previous
	candidate.SHA256 = strings.Repeat("a", 64)
	publication := managerPublication{Schema: 1, ID: "op-interrupted", Phase: tools.Staged, Previous: &previous, Candidate: candidate}
	if err := atomicJSON(filepath.Join(store.Root, "manager-install.json"), publication); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(directory, ".loki-manager-op-interrupted.tmp.previous")
	if err := os.Rename(previous.Executable, backup); err != nil {
		t.Fatal(err)
	}
	if err := store.recoverManagerPublication(); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(previous.Executable); err != nil || string(data) != "old binary" {
		t.Fatalf("old CLI recovery: %q %v", data, err)
	}
	actual, err := store.readManagerPublication()
	if err != nil || actual.Phase != tools.Aborted {
		t.Fatalf("recovery journal: %+v %v", actual, err)
	}
}

func TestManagerPublicationAdvancesOnlyEmptyRelease(t *testing.T) {
	for _, populated := range []bool{false, true} {
		t.Run(fmt.Sprint(populated), func(t *testing.T) {
			store := Store{Root: t.TempDir()}
			state, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			state.Config.Release = "0.2.1"
			if populated {
				target := LocalTarget(tools.ProjectHost)
				state.Installed["browser"] = Installation{
					Artifact: tools.Artifact{Module: "browser", Release: "0.2.1", Target: target, URL: "https://example.test/browser.zip", SHA256: strings.Repeat("a", 64), Bytes: 1, Format: "zip"},
					Manifest: tools.Manifest{Schema: 1, ID: "browser", Release: "0.2.1", Targets: []tools.Target{target}, Tools: []string{"browser_test"}},
				}
			}
			if err := store.Save(state); err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(t.TempDir(), "candidate")
			if err := os.WriteFile(source, []byte("new manager binary"), 0755); err != nil {
				t.Fatal(err)
			}
			bin := t.TempDir()
			for range 2 {
				if _, err := store.InstallManager(t.Context(), source, bin); err != nil {
					t.Fatal(err)
				}
				actual, err := store.Load()
				if err != nil {
					t.Fatal(err)
				}
				want := Release
				if populated {
					want = "0.2.1"
				}
				if actual.Config.Release != want || len(actual.Installed) != len(state.Installed) {
					t.Fatalf("manager changed tool release/state incorrectly: %+v", actual)
				}
			}
		})
	}
}

func TestManagerPublicationRecoversBetweenBinaryAndMarker(t *testing.T) {
	s := Store{Root: t.TempDir()}
	source := filepath.Join(t.TempDir(), "candidate")
	if err := os.WriteFile(source, []byte("first binary"), 0755); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	first, err := s.InstallManager(context.Background(), source, bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("second binary"), 0755); err != nil {
		t.Fatal(err)
	}
	digest, size, err := managerDigest(source)
	if err != nil {
		t.Fatal(err)
	}
	candidate := first
	candidate.SHA256, candidate.Bytes = digest, size
	p := managerPublication{Schema: 1, ID: "op-publish", Phase: tools.Staged, Previous: &first, Candidate: candidate}
	if err := atomicJSON(filepath.Join(s.Root, "manager-install.json"), p); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(first.Executable, []byte("second binary"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := s.Recover(); err != nil {
		t.Fatal(err)
	}
	var marker ManagerRecord
	if err := readOwnedJSON(first.Executable+".loki-owner.json", tools.MaxManifestBytes, &marker); err != nil {
		t.Fatal(err)
	}
	if marker.SHA256 != digest {
		t.Fatal("ownership marker did not follow committed binary")
	}
	journal, err := s.readManagerPublication()
	if err != nil || journal.Phase != tools.Committed {
		t.Fatalf("publication journal: %+v %v", journal, err)
	}
}
