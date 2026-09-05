package action

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeStatus(t *testing.T) {
	r, c := runtimeFixture(t, nil)
	result := r.Status()
	if result["initialized"] != true || result["running_processes"] != 0 || result["process_limits"].(map[string]any)["per_profile"] != 6 {
		t.Fatal(result)
	}
	if err := os.Rename(filepath.Join(c.StateDirectory, "master.key"), filepath.Join(c.StateDirectory, "master.saved")); err != nil {
		t.Fatal(err)
	}
	if r.Status()["initialized"] != false {
		t.Fatal("partial initialization reported ready")
	}
}
