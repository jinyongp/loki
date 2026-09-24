package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"loki/internal/host/releases"
)

func commandManifest(t *testing.T, version string, binary []byte) []byte {
	t.Helper()
	sum := sha256.Sum256(binary)
	generation, err := releases.NewGeneration(releases.GenerationSpec{
		Version:          version,
		ReleasedAt:       time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC),
		HostBinaryDigest: "sha256:" + hex.EncodeToString(sum[:]),
		CoreImageDigest:  "sha256:" + strings.Repeat("b", 64),
		ConfigSchema:     1, PolicySchema: 1, ToolchainSchema: 1, StateSchema: 1,
		Reads: releases.Compatibility{
			Config:    releases.SchemaRange{Min: 1, Max: 1},
			Policy:    releases.SchemaRange{Min: 1, Max: 1},
			Toolchain: releases.SchemaRange{Min: 1, Max: 1},
			State:     releases.SchemaRange{Min: 1, Max: 1},
		},
		Rollback: releases.RollbackCoverage{StateSnapshot: true, ConfigSnapshot: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	target := func(path, digest string) releases.TargetDescriptor {
		return releases.TargetDescriptor{Path: path, Length: 1, SHA256: strings.Repeat(digest, 64)}
	}
	manifest := releases.ReleaseManifest{
		Version:          releases.ReleaseManifestVersion,
		Generation:       generation,
		HostBinary:       releases.TargetDescriptor{Path: "releases/bin/loki-" + version, Length: int64(len(binary)), SHA256: hex.EncodeToString(sum[:])},
		HostAssets:       target("releases/assets/loki-host-"+version+".tar.gz", "d"),
		ToolchainCatalog: target("toolchains/catalogs/"+version+".json", "e"),
		Provenance:       target("releases/provenance/"+version+".bundle.json", "f"),
		Notices:          target("releases/notices/"+version+".tar.gz", "1"),
		ReleaseNotes:     target("releases/notes/"+version+".md", "2"),
		SupportedHosts:   []releases.SupportedHost{{Environment: "native", Distribution: "ubuntu", Version: "24.04", Arch: "amd64"}},
		Runtime:          releases.RuntimeRequirements{DockerMin: "29.8.1", ComposeMin: "5.5.1"},
	}
	raw, err := releases.EncodeReleaseManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestParseBootstrapArgsSeparatesBootstrapAndInstallerFlags(t *testing.T) {
	options, err := parseBootstrapArgs([]string{
		"--bootstrap-state-root", "/tmp/loki-bootstrap-state",
		"--system",
		"--workspace", "/srv/loki",
	})
	if err != nil {
		t.Fatal(err)
	}
	if options.stateRoot != "/tmp/loki-bootstrap-state" || !options.system {
		t.Fatalf("bootstrap options = %#v", options)
	}
	want := []string{"--system", "--workspace", "/srv/loki"}
	if !reflect.DeepEqual(options.installArgs, want) {
		t.Fatalf("install args = %#v", options.installArgs)
	}
}

func TestParseBootstrapArgsRejectsReleaseOverrideAndReservedManifest(t *testing.T) {
	for _, args := range [][]string{
		{"--bootstrap-state-root"},
		{"--bootstrap-state-root="},
		{"--bootstrap-release", "1.2.3"},
		{"--bootstrap-release=v1.2.3"},
		{"--bootstrap-release-manifest", "/tmp/forged.json"},
		{"--bootstrap-release-manifest=/tmp/forged.json"},
	} {
		if _, err := parseBootstrapArgs(args); err == nil {
			t.Fatalf("invalid args accepted: %#v", args)
		}
	}
}

func TestBootstrapInfoReportsEmbeddedReleaseBindingWithoutInstallation(t *testing.T) {
	manifest := commandManifest(t, "1.2.3", []byte("host-binary"))
	encoded := base64.StdEncoding.EncodeToString(manifest)
	var stdout, stderr bytes.Buffer
	code := runBootstrap(
		[]string{"--bootstrap-info"},
		bytes.NewReader(nil),
		&stdout,
		&stderr,
		"v1.2.3",
		encoded,
	)
	if code != 0 {
		t.Fatalf("bootstrap info exit = %d, stderr=%q", code, stderr.String())
	}
	loaded, err := releases.LoadReleaseManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(manifest)
	want := bootstrapInfo{
		ReleaseTag:            "v1.2.3",
		ReleaseManifestSHA256: hex.EncodeToString(sum[:]),
		HostBinarySHA256:      loaded.HostBinary.SHA256,
	}
	var got bootstrapInfo
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("bootstrap info = %#v, want %#v", got, want)
	}
}

func TestParseBootstrapInfoRejectsInstallationOptions(t *testing.T) {
	for _, args := range [][]string{
		{"--bootstrap-info", "--system"},
		{"--bootstrap-info", "--workspace", "/srv/loki"},
		{"--bootstrap-info", "--bootstrap-state-root", "/tmp/state"},
	} {
		if _, err := parseBootstrapArgs(args); err == nil {
			t.Fatalf("mixed bootstrap info args accepted: %#v", args)
		}
	}
}

func TestRunBootstrapFailsClosedWithoutEmbeddedRelease(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runBootstrap(nil, bytes.NewReader(nil), &stdout, &stderr, "", ""); code != 1 {
		t.Fatalf("missing release tag exit = %d, stderr=%q", code, stderr.String())
	}
	stderr.Reset()
	manifest := commandManifest(t, "1.2.3", []byte("host-binary"))
	if code := runBootstrap(nil, bytes.NewReader(nil), &stdout, &stderr, "v9.9.9", base64.StdEncoding.EncodeToString(manifest)); code != 1 {
		t.Fatalf("mismatched release tag exit = %d, stderr=%q", code, stderr.String())
	}
}
