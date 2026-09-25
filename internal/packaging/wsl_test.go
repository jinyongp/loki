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
		"ARG SOURCE_DATE_EPOCH=0",
		"UBUNTU_ACL_VERSION=2.3.2-1build1.1",
		"UBUNTU_CA_CERTIFICATES_VERSION=20260601~24.04.1",
		"UBUNTU_CURL_VERSION=8.5.0-2ubuntu10.15",
		"UBUNTU_DBUS_VERSION=1.14.10-4ubuntu4.1",
		"UBUNTU_IPTABLES_VERSION=1.8.10-3ubuntu2",
		"UBUNTU_PASSWD_VERSION=1:4.13+dfsg1-4ubuntu3.2",
		"UBUNTU_SYSTEMD_VERSION=255.4-1ubuntu8.17",
		"UBUNTU_SYSTEMD_SYSV_VERSION=255.4-1ubuntu8.17",
		`"acl=$UBUNTU_ACL_VERSION"`,
		`"ca-certificates=$UBUNTU_CA_CERTIFICATES_VERSION"`,
		`"curl=$UBUNTU_CURL_VERSION"`,
		`"dbus=$UBUNTU_DBUS_VERSION"`,
		`"iptables=$UBUNTU_IPTABLES_VERSION"`,
		`"passwd=$UBUNTU_PASSWD_VERSION"`,
		`"systemd=$UBUNTU_SYSTEMD_VERSION"`,
		`"systemd-sysv=$UBUNTU_SYSTEMD_SYSV_VERSION"`,
		"DOCKER_CE_VERSION=5:29.8.1-1~ubuntu.24.04~noble",
		"CONTAINERD_VERSION=2.3.6-1~ubuntu.24.04~noble",
		"DOCKER_BUILDX_VERSION=0.37.1-1~ubuntu.24.04~noble",
		"DOCKER_COMPOSE_VERSION=5.5.1-1~ubuntu.24.04~noble",
		"COPY --chmod=0444 packaging/wsl/apt-delta.lock /tmp/loki-wsl-apt-delta.lock",
		"loki-wsl-base-packages",
		"comm -13 /tmp/loki-wsl-base-packages /tmp/loki-wsl-final-packages",
		"comm -23 /tmp/loki-wsl-base-packages /tmp/loki-wsl-final-packages",
		"WSL apt package delta changed",
		"WSL apt transaction removed base packages",
		"rm -f /etc/apt/keyrings/docker.asc /etc/apt/sources.list.d/docker.sources",
		"rm -rf /var/lib/apt/lists/* /var/log/apt",
		"rm -f /var/log/dpkg.log /var/log/alternatives.log",
		"getent group ubuntu",
		`test "$(getent group ubuntu | cut -d: -f3)" = 1000`,
		"groupadd --gid 1000 ubuntu",
		"getent passwd ubuntu",
		"useradd --uid 1000 --gid 1000",
		"chage --lastday -1 ubuntu",
		"/home/ubuntu/workspace",
		"COPY --from=release --chmod=0755 loki-linux-amd64 /usr/lib/loki-appliance/loki",
		"COPY --from=release --chmod=0600 release-manifest.json /usr/lib/loki-appliance/release-manifest.json",
		"systemctl enable docker.service containerd.service loki-appliance-provision.service",
		"rm -f /var/lib/dbus/machine-id",
		"truncate -s 0 /etc/machine-id",
		"FROM scratch",
		"COPY --from=rootfs --exclude=etc/resolv.conf / /",
	} {
		if !strings.Contains(dockerfile, required) {
			t.Errorf("WSL Dockerfile lacks %q", required)
		}
	}
	for _, forbidden := range []string{
		"usermod -aG docker",
		"usermod --append --groups docker",
		"NOPASSWD:",
		"sudoers.d/loki-operator",
		"mcp-token",
		"var/lib/loki/lifecycle",
		"ubuntu:latest",
	} {
		if strings.Contains(dockerfile, forbidden) {
			t.Errorf("WSL Dockerfile contains forbidden %q", forbidden)
		}
	}

	aptLock := strings.TrimSpace(read("packaging/wsl/apt-delta.lock"))
	if aptLock == "" || strings.Contains(aptLock, "sudo=") {
		t.Fatalf("unexpected WSL apt transaction lock:\n%s", aptLock)
	}
	lockLines := strings.Split(aptLock, "\n")
	for index := 1; index < len(lockLines); index++ {
		if lockLines[index-1] >= lockLines[index] {
			t.Fatalf("WSL apt transaction lock is not strictly sorted: %q then %q", lockLines[index-1], lockLines[index])
		}
	}
	for _, required := range []string{
		"acl=2.3.2-1build1.1",
		"containerd.io=2.3.6-1~ubuntu.24.04~noble",
		"docker-ce=5:29.8.1-1~ubuntu.24.04~noble",
		"systemd=255.4-1ubuntu8.17",
	} {
		if !strings.Contains("\n"+aptLock+"\n", "\n"+required+"\n") {
			t.Errorf("WSL apt transaction lock lacks %q", required)
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

	dockerIgnore := read(".dockerignore")
	for _, required := range []string{"!packaging/wsl/", "!packaging/wsl/**"} {
		if !strings.Contains(dockerIgnore, required) {
			t.Errorf(".dockerignore does not include WSL build input %q", required)
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
