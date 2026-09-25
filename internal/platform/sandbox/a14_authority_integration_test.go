//go:build linux

package sandbox

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRealOCIJobScriptAuthorityBoundary(t *testing.T) {
	if os.Getenv("LOKI_REQUIRE_OCI_JOB_TESTS") != "1" {
		t.Skip("set LOKI_REQUIRE_OCI_JOB_TESTS=1 with explicit Docker socket, pinned image, and shared workspace fixtures")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("real OCI script authority acceptance requires Linux")
	}
	socket := requiredOCIEnv(t, "LOKI_TEST_DOCKER_SOCKET")
	image := requiredOCIEnv(t, "LOKI_TEST_DOCKER_IMAGE")
	workspace := requiredOCIEnv(t, "LOKI_TEST_DOCKER_WORKSPACE")
	if !filepath.IsAbs(socket) || !filepath.IsAbs(workspace) {
		t.Fatal("OCI authority fixture paths must be absolute")
	}
	info, err := os.Stat(workspace)
	if err != nil || !info.IsDir() {
		t.Fatalf("OCI authority workspace fixture must be an existing directory: %v", err)
	}

	peerUID := optionalOCIUint32(t, "LOKI_TEST_DOCKER_PEER_UID", 0)
	workloadUID := optionalOCIUint32(t, "LOKI_TEST_WORKLOAD_UID", 65534)
	workloadGID := optionalOCIUint32(t, "LOKI_TEST_WORKLOAD_GID", 65534)
	policyDigest := strings.Repeat("e", 64)
	policy, err := NewPolicy(PolicyOptions{
		GenerationSHA256: policyDigest,
		Image:            image,
		Gateway: GatewayPolicyOptions{
			Image: image, Binary: "/opt/loki/bin/loki",
			ExecutionContract: "/usr/share/doc/loki/execution-contract.json",
			EgressPolicy:      "/usr/share/doc/loki/egress-policy.json", ProxyPort: 18766,
			MemoryBytes: 64 << 20, PIDs: 16, TmpfsBytes: 16 << 20,
		},
		Workspace:   workspace,
		UID:         workloadUID,
		GID:         workloadGID,
		Environment: []string{"PATH=/opt/loki/bin:/usr/local/bin:/usr/bin:/bin"},
		MemoryBytes: 128 << 20,
		PIDs:        32,
		TmpfsBytes:  16 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}

	hostCanary := filepath.Join(t.TempDir(), "host-control-canary")
	if err = os.WriteFile(hostCanary, []byte("host-only\n"), 0600); err != nil {
		t.Fatal(err)
	}
	script := `set -eu
test -x /opt/loki/bin/loki
test ! -e "$1"
test ! -S /run/docker.sock
test ! -S /run/loki/launcher/control.sock
test ! -S /run/loki/runtime/control.sock
test ! -r /var/lib/loki/runtime
test ! -x /var/lib/loki/runtime
test ! -r /var/lib/loki/signing
test ! -x /var/lib/loki/signing
test ! -x /run/loki
test ! -e /run/loki-private
if env | grep -Eq '^(LOKI_MCP_TOKEN|LOKI_JOB_PROXY_TOKEN|LOKI_GITHUB_PRIVATE_KEY|GITHUB_TOKEN|GH_TOKEN)='; then
  exit 31
fi
if /opt/loki/bin/loki health --unix /run/loki/launcher/control.sock >/tmp/launcher-health 2>&1; then
  exit 32
fi
printf 'authority-shell=ok\n'
`
	plan, err := policy.Plan(WorkloadSpec{
		ID:           randomOCIJobID(t),
		PolicySHA256: policyDigest,
		CWD:          ".",
		Argv:         []string{"/bin/sh", "-c", script, "authority-shell", hostCanary},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := realOCIEngine(t, socket, peerUID).Run(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != OutcomeExited || !result.ExitCodeKnown || result.ExitCode != 0 ||
		result.Cleanup != CleanupComplete || !strings.Contains(string(result.Output), "authority-shell=ok") {
		t.Fatalf("script authority result = %#v", result)
	}
}
