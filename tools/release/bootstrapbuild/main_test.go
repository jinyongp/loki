package main

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type fakeBuildRunner struct {
	cwd  string
	args []string
	env  []string
	err  error
}

func (r *fakeBuildRunner) Run(_ context.Context, cwd string, args, environment []string) error {
	r.cwd = cwd
	r.args = append([]string(nil), args...)
	r.env = append([]string(nil), environment...)
	if r.err != nil {
		return r.err
	}
	for index := 0; index+1 < len(args); index++ {
		if args[index] != "-o" {
			continue
		}
		return os.WriteFile(args[index+1], []byte("bootstrap-binary"), 0600)
	}
	return errors.New("builder output path was not supplied")
}

func bootstrapBuildFixture(t *testing.T) (buildOptions, []byte) {
	t.Helper()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte("module fixture\n"), 0644); err != nil {
		t.Fatal(err)
	}
	root := []byte("{\"signed\":\"fixture\"}\n")
	rootPath := filepath.Join(t.TempDir(), "root.json")
	if err := os.WriteFile(rootPath, root, 0600); err != nil {
		t.Fatal(err)
	}
	return buildOptions{
		Output:      filepath.Join(t.TempDir(), "loki-bootstrap"),
		MetadataURL: "https://updates.example.test/repository",
		TrustedRoot: rootPath,
		SourceRoot:  source,
		GOOS:        "linux",
		GOARCH:      "amd64",
	}, root
}

func TestBuildBootstrapEmbedsValidatedTrustAndPublishesAtomically(t *testing.T) {
	options, root := bootstrapBuildFixture(t)
	runner := &fakeBuildRunner{}
	var validatedURL string
	var validatedRoot []byte
	validator := func(url string, raw []byte) error {
		validatedURL = url
		validatedRoot = append([]byte(nil), raw...)
		return nil
	}
	environment := []string{
		"PATH=/usr/bin:/bin", "LANG=C.UTF-8",
		"CGO_ENABLED=1", "GOOS=windows", "GOARCH=arm64",
		"GOFLAGS=-race", "GOEXPERIMENT=arenas", "GOAMD64=v4",
		"GOTOOLCHAIN=auto", "GOENV=/tmp/goenv", "GOWORK=/tmp/go.work",
	}
	if err := buildBootstrapWithValidator(t.Context(), options, runner, environment, validator); err != nil {
		t.Fatal(err)
	}
	if validatedURL != options.MetadataURL || !slices.Equal(validatedRoot, root) {
		t.Fatalf("validated trust = %q %q", validatedURL, validatedRoot)
	}
	if runner.cwd != options.SourceRoot {
		t.Fatalf("builder cwd = %q", runner.cwd)
	}
	if !slices.Contains(runner.args, "-trimpath") || !slices.Contains(runner.args, "-buildvcs=false") ||
		!slices.Contains(runner.args, "./cmd/loki-bootstrap") {
		t.Fatalf("builder args = %#v", runner.args)
	}
	encoded := base64.StdEncoding.EncodeToString(root)
	joined := strings.Join(runner.args, "\n")
	if !strings.Contains(joined, "main.releaseMetadataURL="+options.MetadataURL) ||
		!strings.Contains(joined, "main.trustedRootBase64="+encoded) {
		t.Fatalf("embedded linker flags = %#v", runner.args)
	}
	for _, want := range []string{
		"CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64", "GOAMD64=v1",
		"GOFLAGS=", "GOEXPERIMENT=", "GOTOOLCHAIN=local", "GOENV=off", "GOWORK=off",
	} {
		if !slices.Contains(runner.env, want) {
			t.Fatalf("builder environment missing %q: %#v", want, runner.env)
		}
	}
	for _, forbidden := range []string{
		"CGO_ENABLED=1", "GOOS=windows", "GOARCH=arm64",
		"GOFLAGS=-race", "GOEXPERIMENT=arenas", "GOAMD64=v4",
		"GOTOOLCHAIN=auto", "GOENV=/tmp/goenv", "GOWORK=/tmp/go.work",
	} {
		if slices.Contains(runner.env, forbidden) {
			t.Fatalf("builder environment retained %q: %#v", forbidden, runner.env)
		}
	}
	info, err := os.Stat(options.Output)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0755 {
		t.Fatalf("bootstrap output = %v, %v", info, err)
	}
	raw, err := os.ReadFile(options.Output)
	if err != nil || string(raw) != "bootstrap-binary" {
		t.Fatalf("bootstrap output bytes = %q, %v", raw, err)
	}
	entries, err := os.ReadDir(filepath.Dir(options.Output))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".loki-bootstrap-") {
			t.Fatalf("temporary bootstrap output leaked: %s", entry.Name())
		}
	}
}

func TestBuildBootstrapFailsClosedBeforeRunner(t *testing.T) {
	options, _ := bootstrapBuildFixture(t)
	tests := map[string]func(*buildOptions){
		"relative-output": func(value *buildOptions) { value.Output = "bootstrap" },
		"relative-root":   func(value *buildOptions) { value.TrustedRoot = "root.json" },
		"bad-goos":        func(value *buildOptions) { value.GOOS = "linux;sh" },
		"bad-goarch":      func(value *buildOptions) { value.GOARCH = "../amd64" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			changed := options
			changed.Output = filepath.Join(t.TempDir(), "bootstrap")
			mutate(&changed)
			runner := &fakeBuildRunner{}
			if err := buildBootstrapWithValidator(t.Context(), changed, runner, nil, func(string, []byte) error { return nil }); err == nil {
				t.Fatal("invalid bootstrap build options were accepted")
			}
			if len(runner.args) != 0 {
				t.Fatalf("runner was called: %#v", runner.args)
			}
		})
	}
}

func TestBuildBootstrapRejectsExistingOutputAndTrustFailure(t *testing.T) {
	options, _ := bootstrapBuildFixture(t)
	if err := os.WriteFile(options.Output, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeBuildRunner{}
	if err := buildBootstrapWithValidator(t.Context(), options, runner, nil, func(string, []byte) error { return nil }); err == nil {
		t.Fatal("existing output was overwritten")
	}
	if len(runner.args) != 0 {
		t.Fatalf("runner called for existing output: %#v", runner.args)
	}

	options, _ = bootstrapBuildFixture(t)
	want := errors.New("invalid trust")
	if err := buildBootstrapWithValidator(t.Context(), options, runner, nil, func(string, []byte) error { return want }); !errors.Is(err, want) {
		t.Fatalf("trust validation error = %v", err)
	}
}

func TestValidateEmbeddedTrustRejectsInvalidBootstrapInputs(t *testing.T) {
	for name, url := range map[string]string{
		"empty": "",
		"http":  "http://updates.example.test/repository",
		"query": "https://updates.example.test/repository?channel=stable",
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateEmbeddedTrust(url, []byte("{}")); err == nil {
				t.Fatal("invalid bootstrap trust was accepted")
			}
		})
	}
}
