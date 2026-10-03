package browser

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionReclamationKeepsLiveProfilesResultsAndUnknownData(t *testing.T) {
	data := t.TempDir()
	project, engine := "project-fixture", "playwright"
	first, firstOutput, closeFirst, err := ownedSession(t.Context(), data, project, engine)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFirst()
	if err := os.WriteFile(filepath.Join(firstOutput, "screenshot.png"), []byte("retained result"), 0600); err != nil {
		t.Fatal(err)
	}
	profiles := filepath.Join(data, "profiles", project, engine)
	unknown := filepath.Join(profiles, "user-directory")
	if err := os.Mkdir(unknown, 0700); err != nil {
		t.Fatal(err)
	}
	abandonedName := "session-" + strings.Repeat("a", 32)
	abandoned := filepath.Join(profiles, abandonedName)
	if err := os.Mkdir(abandoned, 0700); err != nil {
		t.Fatal(err)
	}
	marker, _ := json.Marshal(sessionMarker{Schema: 1, Session: abandonedName})
	if err := os.WriteFile(filepath.Join(abandoned, ".loki-browser-session.json"), marker, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "leases", project, engine, abandonedName+".lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	second, _, closeSecond, err := ownedSession(t.Context(), data, project, engine)
	if err != nil {
		t.Fatal(err)
	}
	defer closeSecond()
	if _, err := os.Stat(abandoned); !os.IsNotExist(err) {
		t.Fatalf("abandoned owned profile survived: %v", err)
	}
	for _, path := range []string{first, second, unknown, filepath.Join(firstOutput, "screenshot.png")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("retained path %s: %v", path, err)
		}
	}
	closeFirst()
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatal("normal disconnect did not remove its profile")
	}
	if _, err := os.Stat(second); err != nil {
		t.Fatal("disconnect removed another session")
	}
}

func TestReclamationRequiresMatchingMarkerAndRegularLease(t *testing.T) {
	data := t.TempDir()
	project, engine := "project-fixture", "devtools"
	_, _, cleanup, err := ownedSession(t.Context(), data, project, engine)
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	name := "session-" + strings.Repeat("b", 32)
	profile := filepath.Join(data, "profiles", project, engine, name)
	if err := os.Mkdir(profile, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, ".loki-browser-session.json"), []byte(`{"schema":1,"session":"another"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "leases", project, engine, name+".lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	_, _, cleanup, err = ownedSession(t.Context(), data, project, engine)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if _, err := os.Stat(profile); err != nil {
		t.Fatal("unverified profile was deleted")
	}
}

func TestReclamationRetainsUncertainPreparedEngineProfiles(t *testing.T) {
	data := t.TempDir()
	profile, _, cleanup, err := ownedSession(t.Context(), data, "project", "playwright")
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	// Recreate the owned marker with its now-unlocked persistent lease, as
	// after the manager exits while child-process exit is still uncertain.
	if err := os.Mkdir(profile, 0700); err != nil {
		t.Fatal(err)
	}
	if err := markEnginePrepared(profile); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(data)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := reclaimProfiles(root, filepath.Join("profiles", "project", "playwright"), filepath.Join("leases", "project", "playwright")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(profile); err != nil {
		t.Fatal("prepared engine profile was reclaimed without process-exit evidence")
	}
}
