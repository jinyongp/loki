package management

import (
	"context"
	"loki/internal/tools"
	"os"
	"path/filepath"
	"runtime"
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
