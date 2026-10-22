//go:build linux

package application

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMaintenanceRejectsMalformedOrNonprivateMetadata(t *testing.T) {
	root := t.TempDir()
	s := &Service{options: Options{Root: root}}
	id := "123e4567-e89b-42d3-a456-426614174541"
	if _, err := s.Admin(t.Context(), AdminRequest{Action: "maintenance_end", RequestID: id}, nil); err == nil {
		t.Fatal("released an unobserved gate")
	}
	if _, err := s.Admin(t.Context(), AdminRequest{Action: "maintenance_begin", RequestID: id, Resource: "app"}, nil); err == nil {
		t.Fatal("ambiguous gate control accepted")
	}
	path := filepath.Join(root, "maintenance.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"operation_id":"123e4567-e89b-42d3-a456-426614174541","active":true,"extra":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.requireWritable(); err == nil {
		t.Fatal("unknown gate metadata admitted a writer")
	}
	if _, err := s.Admin(t.Context(), AdminRequest{Action: "maintenance_begin", RequestID: id}, nil); err == nil {
		t.Fatal("replaced corrupt gate instead of failing closed")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Admin(t.Context(), AdminRequest{Action: "maintenance_begin", RequestID: id}, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := s.requireWritable(); err == nil {
		t.Fatal("non-private maintenance metadata admitted a writer")
	}
}
