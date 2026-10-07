package management

import (
	"context"
	"io"
	"loki/internal/tools"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPromotionAndRollbackRetainSelectionAndData(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("full target requires Linux")
	}
	s := Store{Root: t.TempDir()}
	before, _ := s.Load()
	before.Installed["browser"] = ownedFixture(t, s, "browser")
	before.Config.Tools = []tools.Selection{{ID: "browser", Enabled: true}}
	if err := s.Save(before); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(s.Root, "data", "profile")
	if err := os.MkdirAll(filepath.Dir(data), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(data, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	browser := olderFixture(t, s, ownedFixtureTarget(t, s, LocalTarget(tools.Full), "browser"), "b", 0)
	_ = ownedFixture(t, s, "browser") // Distinct archive digests retain the original native generation.
	git := ownedFixtureTarget(t, s, LocalTarget(tools.Full), "git")
	for _, installation := range []Installation{browser, git} {
		generation, _ := s.Generation(installation.Artifact)
		payload := FullPayload{Schema: 1, Module: installation.Artifact.Module, Release: installation.Artifact.Release, Target: installation.Artifact.Target, Images: map[string]string{}, Programs: map[string]string{}, Assets: map[string]string{}}
		if err := atomicJSON(filepath.Join(generation, "full-runtime.json"), payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.InstallToolsForMode(t.Context(), fixtureCatalog(browser, git), []tools.ID{"git"}, tools.Full, io.Discard); err != nil {
		t.Fatal(err)
	}
	promoted, err := s.Load()
	if err != nil || promoted.Config.Mode != tools.Full || len(promoted.Installed) != 2 || !promoted.Config.Tools[0].Enabled {
		t.Fatalf("promotion lost selection: %+v %v", promoted, err)
	}
	for _, i := range promoted.Installed {
		if i.Artifact.Target.Mode != tools.Full {
			t.Fatal("retained browser was not retargeted")
		}
	}
	if err := s.RollbackTools(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, err := s.Load()
	if err != nil || snapshotDigest(after) != snapshotDigest(before) {
		t.Fatalf("rollback did not restore original snapshot: %v", err)
	}
	raw, _ := os.ReadFile(data)
	if string(raw) != "retained" {
		t.Fatal("promotion or rollback changed user data")
	}
}

func fixtureCatalog(items ...Installation) tools.Catalog {
	c := tools.Catalog{Schema: 1, Release: Release}
	for _, item := range items {
		c.Modules = append(c.Modules, item.Manifest)
		c.Artifacts = append(c.Artifacts, item.Artifact)
		c.Release = item.Artifact.Release
	}
	return c
}

func TestInstallClosureFailureKeepsOriginalSnapshot(t *testing.T) {
	s := Store{Root: t.TempDir()}
	state, _ := s.Load()
	state.Installed["existing"] = ownedFixture(t, s, "existing")
	state.Config.Tools = []tools.Selection{{ID: "existing", Enabled: true}}
	if err := s.Save(state); err != nil {
		t.Fatal(err)
	}
	worker := ownedFixture(t, s, "worker")
	browser := ownedFixture(t, s, "browser", "worker")
	dir, _ := s.Generation(browser.Artifact)
	if err := atomicJSON(filepath.Join(dir, ".loki-owner.json"), Owner{Schema: 1, Identity: "unowned"}); err != nil {
		t.Fatal(err)
	}
	if err := s.InstallTools(context.Background(), fixtureCatalog(worker, browser), []tools.ID{"browser"}, io.Discard); err == nil {
		t.Fatal("invalid second resource committed")
	}
	after, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if snapshotDigest(after) != snapshotDigest(state) {
		t.Fatal("failed closure changed activation or installed pointers")
	}
	j, err := s.readTransaction()
	if err != nil || j == nil || j.Phase != tools.Aborted {
		t.Fatalf("failure journal: %+v %v", j, err)
	}
}

func TestUpdateCommitsAllResourcesAndPreservesActivation(t *testing.T) {
	s := Store{Root: t.TempDir()}
	state, _ := s.Load()
	state.Installed["worker"] = ownedFixture(t, s, "worker")
	state.Installed["browser"] = ownedFixture(t, s, "browser", "worker")
	state.Installed["browser"] = func() Installation {
		i := state.Installed["browser"]
		i.Manifest.Capabilities = []string{"vision"}
		return i
	}()
	oldDirectory, _ := s.Generation(state.Installed["browser"].Artifact)
	if err := atomicJSON(filepath.Join(oldDirectory, "module.json"), state.Installed["browser"].Manifest); err != nil {
		t.Fatal(err)
	}
	state.Config.Tools = []tools.Selection{{ID: "browser", Enabled: true, Capabilities: []string{"vision"}}}
	if err := s.Save(state); err != nil {
		t.Fatal(err)
	}
	var next []Installation
	for _, id := range []tools.ID{"worker", "browser"} {
		i := state.Installed[id]
		i.Artifact.Release, i.Manifest.Release = "0.2.3", "0.2.3"
		i.Artifact.SHA256 = strings.Repeat("b", 64)
		dir, err := s.Generation(i.Artifact)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := atomicJSON(filepath.Join(dir, ".loki-owner.json"), Owner{Schema: 1, Identity: i.Artifact.Identity()}); err != nil {
			t.Fatal(err)
		}
		if err := atomicJSON(filepath.Join(dir, "module.json"), i.Manifest); err != nil {
			t.Fatal(err)
		}
		next = append(next, i)
	}
	if err := s.UpdateTools(context.Background(), fixtureCatalog(next...), io.Discard); err != nil {
		t.Fatal(err)
	}
	after, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if after.Config.Release != "0.2.3" || !after.Config.Tools[0].Enabled || after.Config.Tools[0].Capabilities[0] != "vision" {
		t.Fatal("release update changed activation")
	}
	for id, i := range after.Installed {
		if i.Artifact.Release != "0.2.3" {
			t.Fatalf("mixed release for %s", id)
		}
	}
}

func TestRecoveryDistinguishesAtomicCommitBoundary(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-commit", true: "after-commit"}[committed], func(t *testing.T) {
			s := Store{Root: t.TempDir()}
			previous, _ := s.Load()
			if err := s.Save(previous); err != nil {
				t.Fatal(err)
			}
			candidate, _ := s.Load()
			candidate.Installed["browser"] = ownedFixture(t, s, "browser")
			operation := transaction{Schema: 1, ID: "op-fixture", Action: "install", Phase: tools.Staged, Previous: previous, Candidate: candidate}
			if err := atomicJSON(filepath.Join(s.Root, "transaction.json"), operation); err != nil {
				t.Fatal(err)
			}
			if err := s.RequireMutable(); err == nil {
				t.Fatal("interrupted transaction permitted mutation")
			}
			if committed {
				if err := s.Save(candidate); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Recover(); err != nil {
				t.Fatal(err)
			}
			j, err := s.readTransaction()
			if err != nil {
				t.Fatal(err)
			}
			want := tools.Aborted
			if committed {
				want = tools.Committed
			}
			if j.Phase != want {
				t.Fatalf("recovery phase %s, want %s", j.Phase, want)
			}
		})
	}
}
