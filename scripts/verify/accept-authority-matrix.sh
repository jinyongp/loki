#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)
image=${LOKI_IMAGE:?set LOKI_IMAGE to the digest-pinned accepted core image}
docker=${LOKI_DOCKER:-docker}
socket=${LOKI_TEST_DOCKER_SOCKET:-/var/run/docker.sock}
root=$(mktemp -d "${TMPDIR:-/tmp}/loki-a14-cross-path.XXXXXX")
devtools=$root/devtools
container=

cleanup() {
  result=$?
  trap - EXIT HUP INT TERM
  if test -n "$container"; then
    "$docker" --host "unix://$socket" rm -f "$container" >/dev/null 2>&1 || true
  fi
  rm -rf -- "$root"
  exit "$result"
}
trap cleanup EXIT HUP INT TERM

fail() {
  printf 'loki A14 cross-path acceptance: %s\n' "$*" >&2
  exit 1
}

section() {
  printf 'loki A14 cross-path acceptance: %s\n' "$1"
}

case "$image" in
  *@sha256:????????????????????????????????????????????????????????????????) ;;
  *) fail "LOKI_IMAGE must be pinned by a sha256 digest" ;;
esac
case "$socket" in
  /*) ;;
  *) fail "Docker socket must be absolute" ;;
esac

command -v "$docker" >/dev/null || fail "Docker is required"
command -v go >/dev/null || fail "Go is required"
test -S "$socket" || fail "Docker socket is not a Unix socket: $socket"
"$docker" --host "unix://$socket" info >/dev/null || fail "Docker daemon is not reachable"
"$docker" --host "unix://$socket" pull "$image" >/dev/null

case $(uname -m) in
  x86_64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) fail "unsupported acceptance architecture: $(uname -m)" ;;
esac
label="io.loki.devtools.$arch.sha256"
expected_devtools=$("$docker" --host "unix://$socket" image inspect --format "{{ index .Config.Labels \"$label\" }}" "$image")
printf '%s\n' "$expected_devtools" | grep -Eq '^[0-9a-f]{64}$' ||
  fail "release image does not expose a valid $label label"
container=$("$docker" --host "unix://$socket" create "$image")
"$docker" --host "unix://$socket" cp "$container:/opt/loki/bin/devtools" "$devtools"
"$docker" --host "unix://$socket" rm -f "$container" >/dev/null
container=
chmod 0755 "$devtools"
actual_devtools=$(sha256sum "$devtools" | awk '{print $1}')
test "$actual_devtools" = "$expected_devtools" ||
  fail "release-image devtools digest does not match its immutable image label"

cd "$repo"

section "dedicated secret tool positive/negative boundary"
LOKI_DEVTOOLS_BINARY="$devtools" go test ./internal/app/mcp   -run '^(TestSecretMCPRejectsManagedProfile|TestSecretAndWorkflowMCP|TestRuntimeTypedRejectsUnknownFieldsBeforeMutation)$'   -v -count=1

section "direct broker and generic managed-process boundary"
broker_log=$root/devtools-broker.log
LOKI_DEVTOOLS_BINARY="$devtools" go test ./internal/devtools   -run '^(TestBrokerRejectsManagedCredentialInjection|TestBrokerRejectsPrivateOutputAndUnapprovedInjection|TestBrokerRejectsProcessWithoutEncryptedSecrets|TestRealProcessInheritsBrokerSecrets)$'   -v -count=1 | tee "$broker_log"
grep -F -- '--- PASS: TestRealProcessInheritsBrokerSecrets' "$broker_log" >/dev/null ||
  fail "real release-image devtools broker fixture did not pass"
if grep -F -- '--- SKIP: TestRealProcessInheritsBrokerSecrets' "$broker_log" >/dev/null; then
  fail "real release-image devtools broker fixture was skipped"
fi

section "dependency-hook and Git inspection boundary"
go test ./internal/work/workspace/git   -run '^(TestGitInspectionDisablesRepositoryExecutables|TestGitRejectsExecutableFiltersBeforeWorktreeOperations|TestGitIndexMutationDisablesRepositoryHooksAndFsmonitor)$'   -v -count=1

section "launcher, executor and forged-authority boundary"
go test ./internal/app/executor -run '^TestExecutorToLauncherTrustedAuthorityBoundary$' -v -count=1
go test ./internal/app/launcher   -run '^(TestStartRejectsNetworkAuthorityFieldsBeforeRunner|TestAsyncStartRejectsForgedReplayIdentityBeforeRunner)$'   -v -count=1
go test ./internal/work/jobs/remote -run '^TestLauncherRejectsUntrustedPeer$' -v -count=1

section "network destination and proxy credential boundary"
go test ./internal/egress   -run '^(TestAuthenticatedProxyRequiresAndStripsCredential|TestAllowedHostResolvingToPrivateAddressIsBlocked|TestRedirectTargetRequiresAnotherAllowlistedTunnel)$'   -v -count=1

section "toolchain archive and trusted-release boundary"
go test ./internal/work/toolchains   -run '^(TestInstallZipArtifactPreservesExecutables|TestInstallZipRejectsPathEscape|TestInstallZipRejectsChainedSymlinkEscape|TestInstallTarRejectsUnsafeSymlinkTarget|TestNodeReleaseRejectsUntrustedIdentityAndTamperedArtifact)$'   -v -count=1

section "host-plan and protocol/config authority boundary"
go test ./internal/host/lifecycle   -run '^(TestManagerApplyRejectsStalePreparedPlanBeforeJobInspection|TestManagerApplyExplicitInterruptionPassesExactPlanAndJobs)$'   -v -count=1
go test ./internal/mcpserver   -run '^(TestUnknownToolArgumentsNeverReachHandlers|TestGitHubCommandRejectsCredentialAndScopeArguments|TestSecretMutationSchemasRejectUnsafeShapesBeforeHandler)$'   -v -count=1

section "passed"
