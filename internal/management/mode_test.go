package management

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"loki/internal/tools"
)

func TestModeConfigurationPreservesRetainedData(t *testing.T) {
	store := Store{Root: filepath.Join(t.TempDir(), "host")}
	if err := store.ConfigureMode(tools.ProjectHost); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(store.Root, "retained-user-data")
	if err := os.WriteFile(data, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	err := store.ConfigureMode(tools.Full)
	if runtime.GOOS != "linux" {
		if err == nil {
			t.Fatal("native non-Linux host accepted full mode")
		}
	} else {
		if err != nil {
			t.Fatal(err)
		}
		state, err := store.Load()
		if err != nil || state.Config.Mode != tools.Full {
			t.Fatalf("mode: %+v %v", state.Config, err)
		}
	}
	contents, err := os.ReadFile(data)
	if err != nil || string(contents) != "retained" {
		t.Fatal("mode change removed user data")
	}
	if err := store.ConfigureMode("unknown"); err == nil {
		t.Fatal("accepted invalid mode")
	}
}
