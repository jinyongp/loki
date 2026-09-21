package toolchain

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func nodeRelease(version, checksum string) NodeRelease {
	return NodeRelease{
		Version: version,
		URL:     "https://nodejs.org/download/release/v" + version + "/node-v" + version + "-linux-x64.tar.xz",
		SHA256:  checksum,
	}
}

func writeNodeArchive(t *testing.T, version string) (string, string) {
	t.Helper()
	root := t.TempDir()
	name := "node-v" + version + "-linux-x64"
	tree := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(tree, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	for executable, body := range map[string]string{
		"node": "#!/bin/sh\nprintf 'node " + version + "\n'\n",
		"npm":  "#!/bin/sh\nprintf 'npm fixture\n'\n",
		"npx":  "#!/bin/sh\nprintf 'npx fixture\n'\n",
	} {
		if err := os.WriteFile(filepath.Join(tree, "bin", executable), []byte(body), 0755); err != nil {
			t.Fatal(err)
		}
	}
	archive := filepath.Join(t.TempDir(), name+".tar.xz")
	command := exec.Command("tar", "-cJf", archive, "-C", root, name)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create Node.js archive: %v: %s", err, output)
	}
	payload, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	return archive, fmt.Sprintf("%x", sha256.Sum256(payload))
}

func TestNodeVersionSchemeUsesExactPartialAndFloatingSelectors(t *testing.T) {
	scheme := NodeVersionScheme{}
	for raw, want := range map[string]Selector{
		"26.9.0":  {Kind: SelectorExact, Value: "26.9.0"},
		"v26.9.0": {Kind: SelectorExact, Value: "26.9.0"},
		"26.9":    {Kind: SelectorPartial, Value: "26.9"},
		"26":      {Kind: SelectorPartial, Value: "26"},
		"node":    {Kind: SelectorFloating, Value: "*"},
		"latest":  {Kind: SelectorFloating, Value: "*"},
	} {
		got, err := ParseProjectSelector(raw, scheme)
		if err != nil || got != want {
			t.Fatalf("Node selector %q = %#v, %v; want %#v", raw, got, err, want)
		}
	}
	for _, raw := range []string{"026", "26.09", "26.9.0-rc.1", ">=26", "26.9.0.1"} {
		if _, err := ParseProjectSelector(raw, scheme); err == nil {
			t.Fatalf("invalid Node.js selector %q accepted", raw)
		}
	}
}

func TestNodeProviderResolutionUsesInstalledGenerationUntilExplicitUpdate(t *testing.T) {
	store := generationStoreFixture(t)
	provider := NodeProvider{Store: store}
	old := nodeRelease("26.8.2", strings.Repeat("a", 64))
	current := nodeRelease("26.9.0", strings.Repeat("b", 64))
	if _, err := store.Provision(t.Context(), old.GenerationID(), func(context.Context, string) error { return nil }); err != nil {
		t.Fatal(err)
	}

	ordinary, err := provider.Resolve("26", []NodeRelease{old, current}, false)
	if err != nil {
		t.Fatal(err)
	}
	if ordinary.Resolution.Version != old.Version || !ordinary.Resolution.Installed || ordinary.Resolution.Acquire {
		t.Fatalf("ordinary resolution = %#v", ordinary)
	}
	update, err := provider.Resolve("26", []NodeRelease{old, current}, true)
	if err != nil {
		t.Fatal(err)
	}
	if update.Resolution.Version != current.Version || update.Resolution.Installed || !update.Resolution.Acquire {
		t.Fatalf("update resolution = %#v", update)
	}
}

func TestNodeProviderProvisionAndExecutablePlan(t *testing.T) {
	const version = "26.9.0"
	source, checksum := writeNodeArchive(t, version)
	release := nodeRelease(version, checksum)
	store := generationStoreFixture(t)
	provider := NodeProvider{Store: store}

	plan, err := provider.Resolve(version, []NodeRelease{release}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Resolution.Acquire || plan.Resolution.Installed || plan.GenerationID != release.GenerationID() {
		t.Fatalf("initial plan = %#v", plan)
	}
	generation, err := provider.Provision(t.Context(), plan, source)
	if err != nil {
		t.Fatal(err)
	}
	executables, err := plan.Executables(generation)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"node", "npm", "npx"} {
		path := executables[name]
		info, statErr := os.Stat(path)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0555 {
			t.Fatalf("%s executable = %q, %v, %v", name, path, info, statErr)
		}
	}
	if _, ok := executables["corepack"]; ok {
		t.Fatal("Node.js execution plan unexpectedly depends on Corepack")
	}

	reused, err := provider.Resolve("26", []NodeRelease{release}, false)
	if err != nil {
		t.Fatal(err)
	}
	if reused.Resolution.Version != version || !reused.Resolution.Installed || reused.Resolution.Acquire {
		t.Fatalf("reused plan = %#v", reused)
	}
}

func TestNodeReleaseRejectsUntrustedIdentityAndTamperedArtifact(t *testing.T) {
	valid := nodeRelease("26.9.0", strings.Repeat("a", 64))
	for _, release := range []NodeRelease{
		{Version: "v26.9.0", URL: valid.URL, SHA256: valid.SHA256},
		{Version: valid.Version, URL: "https://example.test/" + valid.Filename(), SHA256: valid.SHA256},
		{Version: valid.Version, URL: valid.URL + "?mirror=1", SHA256: valid.SHA256},
		{Version: valid.Version, URL: strings.Replace(valid.URL, valid.Filename(), "node-v26.9.0-linux-arm64.tar.xz", 1), SHA256: valid.SHA256},
	} {
		if err := release.Validate(); err == nil {
			t.Fatalf("untrusted Node.js release accepted: %#v", release)
		}
	}

	source, checksum := writeNodeArchive(t, "26.9.0")
	release := nodeRelease("26.9.0", checksum)
	provider := NodeProvider{Store: generationStoreFixture(t)}
	plan, err := provider.Resolve(release.Version, []NodeRelease{release}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(source, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = provider.Provision(t.Context(), plan, source); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("tampered Node.js artifact error = %v", err)
	}
}
