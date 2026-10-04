package management

import (
	"loki/internal/tools"
	"testing"
)

func TestNativeArchiveMatchesCombinedCatalogWithoutExpandingAuthority(t *testing.T) {
	linux := tools.Target{OS: "linux", Arch: "amd64", Mode: tools.ProjectHost}
	mac := tools.Target{OS: "darwin", Arch: "arm64", Mode: tools.ProjectHost}
	actual := tools.Manifest{Schema: 1, ID: "browser", Release: "0.2.1", Targets: []tools.Target{linux}, Tools: []string{"loki_browser_files"}}
	expected := actual
	expected.Targets = []tools.Target{linux, mac}
	if !sameTargetManifest(actual, expected, linux) {
		t.Fatal("receipt-bound native archive did not match its combined catalog")
	}
	actual.Capabilities = []string{"unsafe-code"}
	if sameTargetManifest(actual, expected, linux) {
		t.Fatal("native projection admitted an extra capability")
	}
	actual.Capabilities = nil
	actual.Requires = []tools.ID{"execution"}
	if sameTargetManifest(actual, expected, linux) {
		t.Fatal("native projection admitted an extra prerequisite")
	}
	actual.Requires = nil
	actual.Targets = append(actual.Targets, tools.Target{OS: "windows", Arch: "amd64", Mode: tools.ProjectHost})
	if sameTargetManifest(actual, expected, linux) {
		t.Fatal("archive claimed an untrusted native target")
	}
	actual.Targets = []tools.Target{mac}
	if sameTargetManifest(actual, expected, linux) {
		t.Fatal("archive did not contain the requested native target")
	}
}
