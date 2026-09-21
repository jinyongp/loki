package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

type captureRunner struct {
	path         string
	args         []string
	raw          []byte
	mode         os.FileMode
	manifestRaw  []byte
	manifestMode os.FileMode
	err          error
}

func (r *captureRunner) Run(_ context.Context, executable string, args []string) error {
	r.path = executable
	r.args = append([]string(nil), args...)
	info, err := os.Stat(executable)
	if err != nil {
		return err
	}
	r.mode = info.Mode().Perm()
	r.raw, err = os.ReadFile(executable)
	if err != nil {
		return err
	}
	for index := 0; index+1 < len(args); index++ {
		if args[index] != "--bootstrap-release-manifest" {
			continue
		}
		manifestInfo, statErr := os.Stat(args[index+1])
		if statErr != nil {
			return statErr
		}
		r.manifestMode = manifestInfo.Mode().Perm()
		r.manifestRaw, err = os.ReadFile(args[index+1])
		if err != nil {
			return err
		}
		break
	}
	return r.err
}

func TestRunUsesAuthenticatedCandidateWithoutSourceToolchain(t *testing.T) {
	source, host, _, secondBinary := bootstrapSourceFixture(t)
	stateRoot := filepath.Join(t.TempDir(), "bootstrap-state")
	runner := &captureRunner{}
	candidate, err := Run(t.Context(), Config{
		StateRoot:        stateRoot,
		RequestedRelease: "",
		Host:             &host,
		InstallArgs:      []string{"--system", "--workspace", "/srv/loki-workspace"},
		Runner:           runner,
		Source:           source,
	})
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Entry.Release != "2.0.0" || string(runner.raw) != string(secondBinary) {
		t.Fatalf("candidate=%#v runner bytes=%q", candidate.Entry, runner.raw)
	}
	if runner.mode != 0700 {
		t.Fatalf("staged binary mode = %04o", runner.mode)
	}
	if len(runner.args) != 7 || !slices.Equal(runner.args[:3], []string{"host", "install", "--bootstrap-release-manifest"}) ||
		!slices.Equal(runner.args[4:], []string{"--system", "--workspace", "/srv/loki-workspace"}) {
		t.Fatalf("installer args = %#v", runner.args)
	}
	if runner.manifestMode != 0600 || !slices.Equal(runner.manifestRaw, candidate.ManifestBytes) {
		t.Fatalf("staged manifest mode=%04o bytes_match=%v", runner.manifestMode, slices.Equal(runner.manifestRaw, candidate.ManifestBytes))
	}
	if _, err = os.Stat(runner.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bootstrap staging path was not cleaned: %v", err)
	}
	info, err := os.Stat(stateRoot)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("bootstrap state root = %v, %v", info, err)
	}
}

func TestRunCandidateRejectsTamperedBinaryBeforeExecution(t *testing.T) {
	source, host, _, _ := bootstrapSourceFixture(t)
	candidate, err := Resolve(t.Context(), source, host, "")
	if err != nil {
		t.Fatal(err)
	}
	candidate.Binary = append([]byte(nil), candidate.Binary...)
	candidate.Binary[0] ^= 0x20
	runner := &captureRunner{}
	stateRoot := filepath.Join(t.TempDir(), "bootstrap-state")
	if err = ensureStateRoot(stateRoot); err != nil {
		t.Fatal(err)
	}
	if err = runCandidate(t.Context(), stateRoot, candidate, nil, runner); err == nil {
		t.Fatal("tampered host binary was executed")
	}
	if runner.path != "" {
		t.Fatal("runner was called for a tampered host binary")
	}
}

func TestRunPropagatesInstallerFailure(t *testing.T) {
	source, host, _, _ := bootstrapSourceFixture(t)
	runner := &captureRunner{err: errors.New("install failed")}
	_, err := Run(t.Context(), Config{
		StateRoot: filepath.Join(t.TempDir(), "bootstrap-state"),
		Host:      &host,
		Runner:    runner,
		Source:    source,
	})
	if err == nil {
		t.Fatal("host installer failure was ignored")
	}
}

func TestDefaultStateRootUsesExplicitScope(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/tmp/loki-test-state")
	root, err := DefaultStateRoot(false)
	if err != nil {
		t.Fatal(err)
	}
	if root != "/tmp/loki-test-state/loki/bootstrap" {
		t.Fatalf("user bootstrap state root = %q", root)
	}
	system, err := DefaultStateRoot(true)
	if err != nil {
		t.Fatal(err)
	}
	if system != "/var/lib/loki/bootstrap" {
		t.Fatalf("system bootstrap state root = %q", system)
	}

	t.Setenv("XDG_STATE_HOME", "relative")
	if _, err = DefaultStateRoot(false); err == nil {
		t.Fatal("relative XDG state root was accepted")
	}
}
