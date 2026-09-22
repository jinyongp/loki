package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"
)

func TestParseBootstrapArgsSeparatesBootstrapAndInstallerFlags(t *testing.T) {
	options, err := parseBootstrapArgs([]string{
		"--bootstrap-release=1.2.3",
		"--bootstrap-state-root", "/tmp/loki-bootstrap-state",
		"--system",
		"--workspace", "/srv/loki",
	})
	if err != nil {
		t.Fatal(err)
	}
	if options.requestedRelease != "1.2.3" || options.stateRoot != "/tmp/loki-bootstrap-state" || !options.system {
		t.Fatalf("bootstrap options = %#v", options)
	}
	want := []string{"--system", "--workspace", "/srv/loki"}
	if !reflect.DeepEqual(options.installArgs, want) {
		t.Fatalf("install args = %#v", options.installArgs)
	}
}

func TestParseBootstrapArgsRejectsMissingBootstrapValues(t *testing.T) {
	for _, args := range [][]string{
		{"--bootstrap-release"},
		{"--bootstrap-release="},
		{"--bootstrap-state-root"},
		{"--bootstrap-state-root="},
		{"--bootstrap-release-manifest", "/tmp/forged.json"},
		{"--bootstrap-release-manifest=/tmp/forged.json"},
	} {
		if _, err := parseBootstrapArgs(args); err == nil {
			t.Fatalf("invalid args accepted: %#v", args)
		}
	}
}

func TestBootstrapInfoReportsEmbeddedTrustWithoutInstallation(t *testing.T) {
	root := []byte("{\"signed\":\"fixture\"}\n")
	encoded := base64.StdEncoding.EncodeToString(root)
	var stdout, stderr bytes.Buffer
	code := runBootstrap(
		[]string{"--bootstrap-info"},
		bytes.NewReader(nil),
		&stdout,
		&stderr,
		"https://jinyongp.dev/loki/tuf/",
		encoded,
	)
	if code != 0 {
		t.Fatalf("bootstrap info exit = %d, stderr=%q", code, stderr.String())
	}
	sum := sha256.Sum256(root)
	want := bootstrapInfo{
		MetadataURL:       "https://jinyongp.dev/loki/tuf/",
		TrustedRootSHA256: hex.EncodeToString(sum[:]),
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
		{"--bootstrap-info", "--bootstrap-release", "1.2.3"},
		{"--bootstrap-info", "--bootstrap-state-root", "/tmp/state"},
	} {
		if _, err := parseBootstrapArgs(args); err == nil {
			t.Fatalf("mixed bootstrap info args accepted: %#v", args)
		}
	}
}

func TestRunBootstrapFailsClosedWithoutEmbeddedTrust(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runBootstrap(nil, bytes.NewReader(nil), &stdout, &stderr, "", ""); code != 1 {
		t.Fatalf("missing metadata URL exit = %d, stderr=%q", code, stderr.String())
	}
	stderr.Reset()
	if code := runBootstrap(nil, bytes.NewReader(nil), &stdout, &stderr, "https://updates.example.test/repository", ""); code != 1 {
		t.Fatalf("missing trusted root exit = %d, stderr=%q", code, stderr.String())
	}
}
