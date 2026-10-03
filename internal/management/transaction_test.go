package management

import (
	"context"
	"io"
	"loki/internal/tools"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
		i.Artifact.Release, i.Manifest.Release = "0.2.1", "0.2.1"
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
	if after.Config.Release != "0.2.1" || !after.Config.Tools[0].Enabled || after.Config.Tools[0].Capabilities[0] != "vision" {
		t.Fatal("release update changed activation")
	}
	for id, i := range after.Installed {
		if i.Artifact.Release != "0.2.1" {
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
