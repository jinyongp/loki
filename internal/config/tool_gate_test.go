package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loki/internal/tools"
)

func TestToolGateRechecksActivationAndPinnedRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tools.json")
	gate := ToolGate{Path: path, Release: "0.2.0", Mode: tools.Full}
	configuration := tools.Config{Schema: 1, Release: "0.2.0", Host: tools.Host{Kind: "local"}, Mode: tools.Full,
		Tools: []tools.Selection{{ID: "browser", Enabled: true, Capabilities: []string{"unsafe-code"}}}}
	write := func() {
		t.Helper()
		data, err := json.Marshal(configuration)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path+".new", data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(path+".new", path); err != nil {
			t.Fatal(err)
		}
	}
	write()
	first := gate.Revision()
	selected, err := gate.Selection("browser")
	if err != nil || len(selected.Capabilities) != 1 {
		t.Fatalf("selection: %+v %v", selected, err)
	}
	configuration.Tools[0].Enabled = false
	write()
	if _, err := gate.Selection("browser"); err == nil {
		t.Fatal("cached activation survived disable")
	}
	if gate.Revision() == first {
		t.Fatal("revision did not observe atomic replacement")
	}
	configuration.Tools[0].Enabled = true
	configuration.Release = "0.2.1"
	write()
	if _, err := gate.Selection("browser"); err == nil {
		t.Fatal("worker accepted a different release")
	}
	if gate.Revision() != "unavailable" {
		t.Fatal("invalid release was exposed as available")
	}
}

func TestToolGateRejectsUntrustedFileShapes(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "tools.json")
	gate := ToolGate{Path: path, Release: "0.2.0", Mode: tools.Full}
	for _, data := range []string{`{"schema":1,"unknown":true}`, strings.Repeat("x", tools.MaxManifestBytes+1)} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := gate.Selection("browser"); err == nil {
			t.Fatal("accepted invalid public configuration")
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := gate.Selection("browser"); err == nil {
		t.Fatal("accepted directory")
	}
}
