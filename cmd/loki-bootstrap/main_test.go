package main

import (
	"bytes"
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
