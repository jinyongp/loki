package packaging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPortableContainerContractsKeepHostAdapterSeam(t *testing.T) {
	root := filepath.Join("..", "..")
	compose := readPortabilityFile(t, filepath.Join(root, "compose.yaml"))
	for _, forbidden := range []string{
		"network_mode: host",
		"/var/run/docker.sock",
		"host.docker.internal",
		"/mnt/",
		`\\wsl$`,
	} {
		if strings.Contains(compose, forbidden) {
			t.Errorf("Compose contains host-specific assumption %q", forbidden)
		}
	}
	if !strings.Contains(compose, "${LOKI_WORKSPACE:") {
		t.Fatal("Compose does not accept an operator-selected workspace path")
	}

	for _, path := range []string{
		filepath.Join(root, "scripts", "build-loki-oci.sh"),
		filepath.Join(root, "scripts", "build-loki-browser-oci.sh"),
	} {
		build := readPortabilityFile(t, path)
		if !strings.Contains(build, "--platform linux/amd64,linux/arm64") || !strings.Contains(build, "--provenance=mode=max") {
			t.Errorf("%s does not retain the multi-architecture OCI contract", path)
		}
	}

	candidateAcceptance := readPortabilityFile(t, filepath.Join(root, "scripts", "accept-loki-go-candidate.sh"))
	for _, required := range []string{"buildx version", "buildx build --quiet --load"} {
		if !strings.Contains(candidateAcceptance, required) {
			t.Errorf("Go candidate acceptance lacks %q", required)
		}
	}
	acceptanceImage := readPortabilityFile(t, filepath.Join(root, "packaging", "go", "acceptance", "Dockerfile"))
	if !strings.Contains(acceptanceImage, "FROM ubuntu:24.04@sha256:") {
		t.Fatal("Go candidate acceptance image is not digest-pinned")
	}

	acceptance := readPortabilityFile(t, filepath.Join(root, "scripts", "accept-loki-compose.sh"))
	if !strings.Contains(acceptance, "current acceptance target must be Linux or WSL2") {
		t.Fatal("current acceptance target is not explicit")
	}
	docs := readPortabilityFile(t, filepath.Join(root, "docs", "self-hosting.md"))
	for _, required := range []string{
		"The current verified host targets are Linux and WSL2.",
		"macOS execution is outside the current support and acceptance gate.",
		"scripts/accept-loki-compose-macos.sh",
		"Docker Desktop or Colima",
	} {
		if !strings.Contains(docs, required) {
			t.Errorf("self-hosting documentation lacks %q", required)
		}
	}
}

func readPortabilityFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
