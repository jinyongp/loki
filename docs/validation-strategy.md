# Validation tiers and prerequisites

This document defines which Loki checks must be deterministic on an ordinary developer checkout and which checks require an explicit disposable host/runtime fixture. A missing prerequisite is not allowed to turn a product regression into an environment-only skip.

## 1. Default deterministic checks

These checks must not require Docker, Chromium, root, systemd, POSIX ACL utilities, live providers, or real credentials:

```sh
go run ./tools/archcheck
go test ./...
go test -race ./...
go vet ./...
go build ./...
```

Tests that are explicitly classified as integration checks may report a clear skip when their prerequisite is absent during ordinary development. Everything else in the default suite must be independent of ambient host state such as umask, user home contents, network access, or a previously installed Loki instance.

A default-suite failure caused by product behavior remains a failure. `internal/toolchain.TestInstallZipArtifactPreservesExecutables` now verifies the R9 correction under both umask `0022` and `0077`; the tar.gz case checks the same final mode policy. R8 containment regressions reject chained and late symlink parents, unsafe resolved links, duplicate paths, reserved metadata, and unsupported object types, while preserving safe in-root link chains. These are product tests, not optional integration prerequisites. The ZIP extractor uses root-bound writes; the retained external GNU tar extractor receives final tree validation and mode normalization, not a new extraction sandbox. A09 still owns that broader isolation work.

The auth and daemon fixtures that previously depended on ambient umask are ordinary unit tests. Their fixture modes are now applied explicitly; they are not moved to the integration tier.

## 2. Explicit integration checks

Integration checks may require a disposable external executable, host identity, or artifact. Their prerequisite must be visible in the command and in a skip/failure message.

| Capability | Check | Required fixture |
| --- | --- | --- |
| Compose lifecycle POSIX ACL behavior | `LOKI_REQUIRE_POSIX_ACL_TESTS=1 go test ./internal/packaging -run 'TestComposeLifecycle'` | `setfacl` on PATH. Ordinary developer runs may skip these tests; release verification sets the require flag so absence is a failure. |
| Chromium/CDP | `LOKI_REQUIRE_BROWSER_TESTS=1 LOKI_TEST_CHROME=/absolute/chromium go test ./internal/browser ./internal/cdp` | Explicit Chromium binary; `LOKI_TEST_CHROME_LIBS` when the candidate needs a non-default library path. |
| Real devtools broker process | `LOKI_DEVTOOLS_BINARY=/absolute/devtools go test ./internal/devtools -run TestRealProcessInheritsBrokerSecrets` | Pinned devtools candidate binary. Synthetic secret only. |
| Locked project execution contract | `LOKI_E2E_DEVTOOLS=... LOKI_E2E_PNPM=... LOKI_E2E_NODE=... LOKI_E2E_CHROMIUM=... go test ./internal/e2e -run TestProjectExecutionContract` | Four absolute candidate executables plus its isolated fixture/cache directories. |
| Runner/vault OS permissions | root-owned disposable Linux test environment running `go test ./internal/execution -run TestLinuxRunnerCanWriteStateButCannotReadVaultKey` | Effective UID 0 only to create/drop to the synthetic runner UID; never a production host. |
| OCI archive contents/reproducibility | `LOKI_OCI_ARCHIVE=/absolute/archive [LOKI_OCI_ARCHIVE_REPEAT=/absolute/archive2] go test ./internal/packaging -run TestOCIArchiveContents` | Built OCI archive(s), not a mutable tag. |
| Real OCI job lifecycle | `LOKI_REQUIRE_OCI_JOB_TESTS=1 LOKI_TEST_DOCKER_SOCKET=/absolute/docker.sock LOKI_TEST_DOCKER_IMAGE=registry/repo@sha256:... LOKI_TEST_DOCKER_WORKSPACE=/absolute/shared-workspace go test ./internal/platform/sandbox -run TestRealOCIJobLifecycle` | Disposable Linux Docker daemon over a Unix socket, an already-available digest-pinned image containing `/bin/sh`, and a workspace path visible to both the test process and daemon. `LOKI_TEST_DOCKER_PEER_UID`, `LOKI_TEST_WORKLOAD_UID`, and `LOKI_TEST_WORKLOAD_GID` override their documented defaults when the fixture uses different identities. |

An integration test may skip only when run outside a gate that declares it required. A gate that requires the behavior must provide the fixture or convert its absence to failure. Do not add generic `CI` checks or silent fallback fixtures that make it unclear which environment was actually tested.

## 3. Static/race checks

`go vet ./...`, `go build ./...`, and the architecture checker are always deterministic checks. `go test -race ./...` follows the same prerequisite classification as the default test suite: explicitly classified integration cases may skip, but ordinary unit/product regressions still fail.

The race detector proves only races observable in the exercised Go memory model. It does not establish cgroup cleanup, cross-process filesystem CAS, network isolation, credential separation, or container namespace safety; those remain integration/acceptance properties.

## 4. Release and clean-host acceptance

`scripts/verify-loki-release.sh` is a release-artifact gate, not a substitute for A14 clean-host/adversarial acceptance. It now sets `LOKI_REQUIRE_POSIX_ACL_TESTS=1`, so the lifecycle ACL coverage cannot disappear because the verifier host lacks `setfacl`.

The current release verifier still does not make every optional development integration above mandatory. A13/A14 must wire the candidate artifacts into the integration/acceptance checks that are part of the released milestone and fail when a required fixture or check is absent. Required browser, MCP-only project execution, credential-bound workload, distinct-release update/recovery, and sandbox/permission checks may not be reported as successful release coverage when their test was skipped.

Release evidence records at least the source commit, core/browser image digests, effective policy/config generation, candidate binary identity, exact commands, pass/fail/skip counts, and recovery result. A skip is acceptable only for a capability explicitly outside the released milestone; otherwise it is a failed gate.

## 5. Current baseline interpretation

At the reviewed baseline, full normal and race runs failed in `internal/auth`, `internal/daemon`, `internal/toolchain`, and `internal/packaging`, and nine integration tests were skipped. The classification is:

- `internal/auth` and `internal/daemon`: deterministic test-fixture defects caused by ambient umask assumptions; keep in the default tier and fix the fixtures.
- `internal/toolchain`: R9 product defect; keep in the default tier and do not skip or relax the expected executable mode.
- Compose lifecycle tests in `internal/packaging`: explicit POSIX ACL integration prerequisite; developer runs may skip when `setfacl` is absent, while the release verifier requires it.
- Chromium/CDP, real devtools, project execution, root permission, OCI archive, and real OCI job-lifecycle tests: explicit integration prerequisites listed above. Their absence remains visible and their required release/acceptance coverage is closed only when a later milestone supplies the fixture and records a pass.

This classification does not claim that the current Go candidate is release-ready. It prevents environment prerequisites from obscuring product regressions while the architecture remediation proceeds.
