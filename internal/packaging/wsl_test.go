package packaging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWSLApplianceContract(t *testing.T) {
	root := filepath.Join("..", "..")
	read := func(relative string) string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}

	dockerfile := read("packaging/wsl/Dockerfile")
	for _, required := range []string{
		"# syntax=docker/dockerfile:1@sha256:",
		"ubuntu:24.04@sha256:008173c23f95b170204355c12626cb5a965d779a7e1283b09e9cffbb1bf33ca3",
		"DOCKER_CE_VERSION=5:29.8.1-1~ubuntu.24.04~noble",
		"CONTAINERD_VERSION=2.3.5-1~ubuntu.24.04~noble",
		"DOCKER_BUILDX_VERSION=0.37.1-1~ubuntu.24.04~noble",
		"DOCKER_COMPOSE_VERSION=5.5.1-1~ubuntu.24.04~noble",
		"groupadd --gid 1000 ubuntu",
		"useradd --uid 1000 --gid 1000",
		"/home/ubuntu/workspace",
		"NOPASSWD: /usr/local/bin/loki",
		"COPY --from=release --chmod=0755 loki-linux-amd64 /usr/lib/loki-appliance/loki",
		"COPY --from=release --chmod=0600 release-manifest.json /usr/lib/loki-appliance/release-manifest.json",
		"systemctl enable docker.service containerd.service loki-appliance-provision.service",
		"rm -f /etc/resolv.conf",
		"truncate -s 0 /etc/machine-id",
	} {
		if !strings.Contains(dockerfile, required) {
			t.Errorf("WSL Dockerfile lacks %q", required)
		}
	}
	for _, forbidden := range []string{
		"usermod -aG docker",
		"usermod --append --groups docker",
		"mcp-token",
		"var/lib/loki/lifecycle",
		"ubuntu:latest",
	} {
		if strings.Contains(dockerfile, forbidden) {
			t.Errorf("WSL Dockerfile contains forbidden %q", forbidden)
		}
	}

	wslConf := read("packaging/wsl/wsl.conf")
	if !strings.Contains(wslConf, "[boot]\nsystemd=true") ||
		!strings.Contains(wslConf, "[user]\ndefault=ubuntu") {
		t.Fatalf("unexpected wsl.conf:\n%s", wslConf)
	}
	distribution := read("packaging/wsl/wsl-distribution.conf")
	if !strings.Contains(distribution, "defaultUid=1000") ||
		strings.Contains(distribution, "command=") {
		t.Fatalf("unexpected wsl-distribution.conf:\n%s", distribution)
	}

	service := read("packaging/wsl/loki-appliance-provision.service")
	for _, required := range []string{
		"Requires=docker.service",
		"After=docker.service network-online.target",
		"ConditionPathExists=!/var/lib/loki-appliance/provisioned",
		"ExecStart=/usr/lib/loki-appliance/provision",
		"Restart=on-failure",
	} {
		if !strings.Contains(service, required) {
			t.Errorf("WSL provision service lacks %q", required)
		}
	}

	provision := read("packaging/wsl/provision.sh")
	for _, required := range []string{
		"host install",
		"--system",
		"--workspace \"$workspace\"",
		"--prepare-workspace",
		"--bootstrap-release-manifest \"$manifest\"",
		"host status --system --json",
		"host doctor --system",
	} {
		if !strings.Contains(provision, required) {
			t.Errorf("WSL provisioner lacks %q", required)
		}
	}
	for _, forbidden := range []string{"--install-prerequisites", "--allow-sudo-docker", "mcp-token"} {
		if strings.Contains(provision, forbidden) {
			t.Errorf("WSL provisioner contains forbidden %q", forbidden)
		}
	}

	builder := read("scripts/build/build-wsl.sh")
	for _, required := range []string{
		"usage: build-wsl.sh OUTPUT_WSL RELEASE_DIRECTORY",
		"--platform linux/amd64",
		"--build-context \"release=$release\"",
		"type=tar,dest=$raw",
		"gzip -n -9",
		"verify-wsl.sh",
		".host_binary.sha256",
		".host_binary.length",
	} {
		if !strings.Contains(builder, required) {
			t.Errorf("WSL builder lacks %q", required)
		}
	}
	verifier := read("scripts/verify/verify-wsl.sh")
	for _, required := range []string{
		"etc/wsl.conf",
		"etc/wsl-distribution.conf",
		"var/lib/loki/lifecycle/mcp-token",
		"var/lib/loki-appliance/provisioned",
		"defaultUid=1000",
		"must not belong to the docker group",
		"must not contain a kernel or initramfs",
	} {
		if !strings.Contains(verifier, required) {
			t.Errorf("WSL verifier lacks %q", required)
		}
	}
}
