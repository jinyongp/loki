package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"loki/internal/tools"
)

func TestAtomicActivationSnapshotDistinguishesPrivateResources(t *testing.T) {
	target := tools.Target{OS: runtime.GOOS, Arch: runtime.GOARCH, Mode: tools.Full}
	type installation struct {
		Artifact tools.Artifact `json:"artifact"`
		Manifest tools.Manifest `json:"manifest"`
	}
	snapshot := struct {
		Schema    int                       `json:"schema"`
		Config    tools.Config              `json:"config"`
		Installed map[tools.ID]installation `json:"installed"`
	}{Schema: 1, Config: tools.Config{Schema: 1, Release: "0.2.0", Host: tools.Host{Kind: "local"}, Mode: tools.Full, Tools: []tools.Selection{{ID: "git", Enabled: true}}}, Installed: map[tools.ID]installation{}}
	for _, id := range []tools.ID{"execution", "git"} {
		manifest := tools.Manifest{Schema: 1, ID: id, Release: "0.2.0", Targets: []tools.Target{target}, Tools: []string{}}
		if id == "git" {
			manifest.Requires = []tools.ID{"execution"}
		}
		snapshot.Installed[id] = installation{Manifest: manifest, Artifact: tools.Artifact{Module: id, Release: "0.2.0", Target: target, URL: "https://example.com/tool.zip", SHA256: strings.Repeat("a", 64), Bytes: 1, Format: "zip"}}
	}
	path := filepath.Join(t.TempDir(), "state.json")
	write := func() {
		t.Helper()
		data, _ := json.Marshal(snapshot)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	gate := ToolGate{Path: path, Release: "0.2.0", Mode: tools.Full, Snapshot: true}
	if err := gate.Resource("execution"); err != nil {
		t.Fatal("private executor unavailable to Git", err)
	}
	if _, err := gate.Selection("execution"); err == nil {
		t.Fatal("private executor became publicly enabled")
	}
	snapshot.Config.Tools[0].Enabled = false
	write()
	if err := gate.Resource("execution"); err == nil {
		t.Fatal("disabled consumer retained its private prerequisite")
	}
	snapshot.Config.Tools[0].Enabled = true
	private := snapshot.Installed["execution"]
	private.Artifact.Release = "0.2.2"
	snapshot.Installed["execution"] = private
	write()
	if err := gate.Resource("execution"); err == nil {
		t.Fatal("mixed-release private resource accepted")
	}
}
