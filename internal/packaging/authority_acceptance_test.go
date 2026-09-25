package packaging

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestA14AuthorityAcceptanceCoversCrossPathRoutes(t *testing.T) {
	root := filepath.Join("..", "..")
	script := filepath.Join(root, "scripts", "verify", "accept-authority-matrix.sh")
	text := mustRead(t, script)
	if output, err := exec.Command("bash", "-n", script).CombinedOutput(); err != nil {
		t.Fatalf("A14 cross-path authority runner syntax: %v\n%s", err, output)
	}
	for _, required := range []string{
		"LOKI_IMAGE must be pinned by a sha256 digest",
		"io.loki.devtools.$arch.sha256",
		"TestSecretMCPRejectsManagedProfile",
		"TestSecretAndWorkflowMCP",
		"TestRealProcessInheritsBrokerSecrets",
		"TestBrokerRejectsManagedCredentialInjection",
		"TestGitInspectionDisablesRepositoryExecutables",
		"TestGitRejectsExecutableFiltersBeforeWorktreeOperations",
		"TestExecutorToLauncherTrustedAuthorityBoundary",
		"TestAsyncStartRejectsForgedReplayIdentityBeforeRunner",
		"TestLauncherRejectsUntrustedPeer",
		"TestAuthenticatedProxyRequiresAndStripsCredential",
		"TestAllowedHostResolvingToPrivateAddressIsBlocked",
		"TestInstallZipRejectsChainedSymlinkEscape",
		"TestManagerApplyRejectsStalePreparedPlanBeforeJobInspection",
		"TestUnknownToolArgumentsNeverReachHandlers",
		"--- PASS: TestRealProcessInheritsBrokerSecrets",
		"--- SKIP: TestRealProcessInheritsBrokerSecrets",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("A14 cross-path authority runner lacks %q", required)
		}
	}
}
