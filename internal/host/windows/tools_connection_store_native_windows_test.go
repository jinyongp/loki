//go:build windows

package windows

import (
	"os"
	"path/filepath"
	"testing"
)

func TestModularConnectionStoreCreatesPrivateNestedNamespace(t *testing.T) {
	root := filepath.Join(t.TempDir(), "frontend", "control", "connections", "providers")
	store := WindowsConnectionStateStore{ConnectionsRoot: root}
	if err := store.EnsureProviderRoot(t.Context(), "loki-tools", "openai"); err != nil {
		t.Fatal(err)
	}
	path, err := store.ProviderRoot("loki-tools", "openai")
	if err != nil {
		t.Fatal(err)
	}
	if err := (WindowsHelperInstallPlatform{}).VerifyPrivatePath(path, true); err != nil {
		t.Fatal(err)
	}
	if metadata, present, err := NewWindowsOpenAIProviderStore().Read(path); err != nil || present || metadata.TunnelID != "" {
		t.Fatalf("fresh namespace contains adapter metadata: %+v %v %v", metadata, present, err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatal(err)
	}
}
