package management

import (
	"path/filepath"
	"strings"
	"testing"

	"loki/internal/tools"
)

func TestHostBindingsCanBeSavedBeforeToolConfiguration(t *testing.T) {
	s := Store{Root: filepath.Join(t.TempDir(), "unconfigured")}
	selected := ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "ssh", Address: "user@example.test"}, Root: "/home/user/.config/loki", Command: "/home/user/.local/bin/loki"}
	if err := s.SelectExecutionHost(selected); err != nil {
		t.Fatal(err)
	}
	actual, err := s.ExecutionSelection()
	if err != nil || actual == nil || *actual != selected {
		t.Fatalf("host not remembered: %+v %v", actual, err)
	}
	preparation := HostPreparation{Schema: 1, Distribution: "loki-tools", Token: strings.Repeat("a", 64), Phase: "reserved"}
	other := Store{Root: filepath.Join(t.TempDir(), "unconfigured")}
	if err := other.SaveHostPreparation(preparation); err != nil {
		t.Fatal(err)
	}
	if actual, err := other.HostPreparation(); err != nil || actual == nil || *actual != preparation {
		t.Fatalf("preparation not remembered: %+v %v", actual, err)
	}
}
