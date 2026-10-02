//go:build windows

package windows

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func keepalivePEFixture() []byte {
	// A minimal GUI/amd64 PE header; publication tests never execute it.
	raw := make([]byte, 200)
	copy(raw, "MZ")
	binary.LittleEndian.PutUint32(raw[60:], 64)
	copy(raw[64:], "PE\x00\x00")
	binary.LittleEndian.PutUint16(raw[68:], 0x8664)
	binary.LittleEndian.PutUint16(raw[84:], 112)
	binary.LittleEndian.PutUint16(raw[88:], 0x20b)
	binary.LittleEndian.PutUint16(raw[156:], 2)
	return raw
}

func TestWindowsKeepalivePayloadPublication(t *testing.T) {
	expected := ExpectedInstallation{StateDir: filepath.Join(t.TempDir(), "state")}
	expected.TaskExecutable = filepath.Join(expected.StateDir, "loki-keepalive.exe")
	platform := NewWindowsFrontendPlatform()
	if err := platform.EnsurePrivateDirectory(t.Context(), expected.StateDir); err != nil {
		t.Fatal(err)
	}
	raw := keepalivePEFixture()
	encoded := base64.StdEncoding.EncodeToString(raw)
	stopCalls := 0
	stop := func() error { stopCalls++; return nil }
	if err := ensureKeepalivePayload(t.Context(), expected, stop, encoded); err != nil {
		t.Fatal(err)
	}
	if err := verifyPrivateACL(expected.TaskExecutable, false); err != nil {
		t.Fatal(err)
	}
	if err := ensureKeepalivePayload(t.Context(), expected, stop, encoded); err != nil {
		t.Fatal(err)
	}
	if stopCalls != 0 {
		t.Fatal("unchanged companion restarted a running task")
	}
	// A changed release must stop the owned task before replacing its image.
	updated := append(bytes.Clone(raw), 1)
	if err := ensureKeepalivePayload(t.Context(), expected, stop, base64.StdEncoding.EncodeToString(updated)); err != nil {
		t.Fatal(err)
	}
	if stopCalls != 1 {
		t.Fatal("changed companion was published without stopping its task")
	}
	published, err := os.ReadFile(expected.TaskExecutable)
	if err != nil || !bytes.Equal(published, updated) {
		t.Fatalf("companion publication failed: %v", err)
	}
	// Changed disk contents cannot authorize another replacement through an old marker.
	if err := os.WriteFile(expected.TaskExecutable, append(bytes.Clone(raw), 2), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureKeepalivePayload(t.Context(), expected, stop, encoded); err == nil || !strings.Contains(err.Error(), "ownership digest changed") {
		t.Fatalf("tampered companion accepted: %v", err)
	}
	if stopCalls != 1 {
		t.Fatal("tampered companion stopped a task")
	}
}

func TestWindowsKeepalivePayloadRejectsConsoleAndForeignFiles(t *testing.T) {
	expected := ExpectedInstallation{StateDir: filepath.Join(t.TempDir(), "state")}
	expected.TaskExecutable = filepath.Join(expected.StateDir, "loki-keepalive.exe")
	platform := NewWindowsFrontendPlatform()
	if err := platform.EnsurePrivateDirectory(t.Context(), expected.StateDir); err != nil {
		t.Fatal(err)
	}
	raw := keepalivePEFixture()
	console := bytes.Clone(raw)
	binary.LittleEndian.PutUint16(console[156:], 3)
	if err := ensureKeepalivePayload(t.Context(), expected, nil, base64.StdEncoding.EncodeToString(console)); err == nil {
		t.Fatal("console executable was installed")
	}
	if err := platform.WriteProtectedAtomic(t.Context(), expected.TaskExecutable, []byte("foreign file")); err != nil {
		t.Fatal(err)
	}
	if err := ensureKeepalivePayload(t.Context(), expected, nil, base64.StdEncoding.EncodeToString(raw)); err == nil || !strings.Contains(err.Error(), "unowned") {
		t.Fatalf("foreign executable overwritten: %v", err)
	}
}
