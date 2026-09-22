# Validation tiers and prerequisites

This document defines which Loki checks must be deterministic on an ordinary developer checkout and which checks require an explicit disposable host/runtime fixture. A missing prerequisite is not allowed to turn a product regression into an environment-only skip.

## 1. Validation execution cadence

Validation requirements and validation execution cadence are separate concerns. Every material behavior change still owns appropriate regression or acceptance coverage, but broad checks are not rerun mechanically after each implementation unit.

During an implementation batch:

- add or update the required tests, fixtures, snapshots and probes beside the implementation;
- run only the narrow checks needed to prove an interface or invariant that later work immediately depends on;
- otherwise record the intended command, scope and prerequisites and defer execution;
- allow a work item to become implementation-complete while validation is pending, without marking the item or milestone fully done;
- do not rerun the full test, race, vet, build or architecture gates merely because another small commit landed.

After the coherent implementation batch is complete, run the deterministic integrated gate. On failure, use a targeted regression while fixing the cause, rerun the affected broad checks, and then run the complete required deterministic gate once more before closeout. Expensive external-fixture checks run at their integration or release boundary unless an earlier result is required to make a safe downstream design decision.

This cadence intentionally favors long uninterrupted implementation runs while preserving regression code and final acceptance rigor. A commit made before the integrated gate is implementation evidence only; it is not release-readiness evidence.

## 2. Default deterministic checks

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

## 3. Explicit integration checks

Integration checks may require a disposable external executable, host identity, or artifact. Their prerequisite must be visible in the command and in a skip/failure message.

| Capability | Check | Required fixture |
| --- | --- | --- |
| Authenticated source-free bootstrap matrix | `./scripts/verify/accept-loki-bootstrap.sh` | Release-engineering host with Go for the test harness. The simulated install host uses an empty working directory and empty development-tool PATH, exercises both Ubuntu 24.04 native and WSL2 identities, and executes only the privately staged authenticated host binary. The paired metadata fixture rejects expiry and restart rollback while accepting root and independently delegated release/toolchain key rotation. |
| Compose topology/isolation smoke + workspace ACL behavior | `LOKI_IMAGE=<digest-pinned-image> ./scripts/verify/accept-loki-compose.sh` | Disposable Linux/WSL2 Docker host with Compose v2, Buildx/BuildKit, and `setfacl`. This gate checks the portable container topology, role isolation, optional profiles, restart behavior, derived-image invariants, and the minimal workspace ACL preparation only. Host backup/update/rollback is validated by A11 host-manager acceptance instead of a shell lifecycle implementation. |
| Chromium/CDP | `LOKI_REQUIRE_BROWSER_TESTS=1 LOKI_TEST_CHROME=/absolute/chromium go test ./internal/integrations/browser/...` | Explicit Chromium binary; `LOKI_TEST_CHROME_LIBS` when the candidate needs a non-default library path. |
| Real devtools broker process | `LOKI_DEVTOOLS_BINARY=/absolute/devtools go test ./internal/devtools -run TestRealProcessInheritsBrokerSecrets` | Pinned devtools candidate binary. Synthetic secret only. |
| Locked project execution contract | `LOKI_E2E_DEVTOOLS=... LOKI_E2E_PNPM=... LOKI_E2E_NODE=... LOKI_E2E_CHROMIUM=... go test ./internal/e2e -run TestProjectExecutionContract` | Four absolute candidate executables plus its isolated fixture/cache directories. |
| Runner/vault OS permissions | root-owned disposable Linux test environment running `go test ./internal/execution -run TestLinuxRunnerCanWriteStateButCannotReadVaultKey` | Effective UID 0 only to create/drop to the synthetic runner UID; never a production host. |
| OCI archive contents/reproducibility | `LOKI_OCI_ARCHIVE=/absolute/archive [LOKI_OCI_ARCHIVE_REPEAT=/absolute/archive2] go test ./internal/packaging -run TestOCIArchiveContents` | Built OCI archive(s), not a mutable tag. |
| Real OCI Job lifecycle + A06/A07 network acceptance | `./scripts/verify/accept-loki-oci-jobs.sh` | Disposable Linux host with an accessible local Docker daemon on a Unix socket, Docker Buildx/BuildKit, and outbound access for the pinned build base images, the pinned `registry:3.1.1` helper, and the committed default allowlisted authority `registry.npmjs.org:443`. The runner derives the Docker peer UID, creates a disposable shared workspace, starts a loopback-only temporary registry, builds a minimal Loki fixture image from the current checkout, pushes it locally to obtain an immutable `repo@sha256` reference, runs every `TestRealOCIJob*` case with required environment set, and removes its temporary registry/workspace/image references. Non-default layouts may override `LOKI_TEST_DOCKER_SOCKET`, `LOKI_TEST_DOCKER_WORKSPACE`, `LOKI_TEST_EGRESS_ALLOWED_AUTHORITY`, `LOKI_TEST_DOCKER_PEER_UID`, `LOKI_TEST_WORKLOAD_UID`, `LOKI_TEST_WORKLOAD_GID`, or `LOKI_OCI_ACCEPTANCE_REGISTRY_IMAGE`; the normal local-Docker path needs no manual fixture values. The test process must be able to reach the daemon host loopback because Job endpoints are intentionally published on `127.0.0.1`. |

### A06/A07 network acceptance blocker

The A06/A07 source slice has deterministic coverage for domain creation/cleanup, per-Job proxy authentication and forwarding configuration, loopback/unique host-port parsing, durable endpoint leases, exact-resource recovery, public Job schema/handler authority filtering, preview lease revalidation, packaging roles and architecture boundaries. Those checks are not a substitute for a real supported-host network fixture.

The real OCI fixture now contains the A06/A07 scenario: it runs a digest-pinned Loki workload through `dependency-install`, verifies proxy authentication, denied and allowlisted CONNECTs plus direct-egress denial, publishes a declared endpoint through the trusted gateway to a loopback-only ephemeral host port, reopens the exact resource domain through a fresh Engine, exercises HTTP and WebSocket through `preview_publish(action=job)`, and proves that replacing the lease identity while reusing the numeric host port invalidates the old share. Deterministic replacement-resource tests remain responsible for deliberately forged gateway/network incarnations.

A06/A07 still cannot be considered acceptance-complete or release-enabled until `./scripts/verify/accept-loki-oci-jobs.sh` actually passes on a supported disposable Linux Docker host. The underlying tests still require `LOKI_REQUIRE_OCI_JOB_TESTS=1`; the runner sets it and all normal fixture inputs automatically. An ordinary direct `go test` run without that require flag intentionally reports the real OCI cases as skipped; such a skip is implementation evidence only and cannot be counted as release acceptance.

An integration test may skip only when run outside a gate that declares it required. A gate that requires the behavior must provide the fixture or convert its absence to failure. Do not add generic `CI` checks or silent fallback fixtures that make it unclear which environment was actually tested.

## 4. Static/race checks

`go vet ./...`, `go build ./...`, and the architecture checker are always deterministic checks. `go test -race ./...` follows the same prerequisite classification as the default test suite: explicitly classified integration cases may skip, but ordinary unit/product regressions still fail.

The race detector proves only races observable in the exercised Go memory model. It does not establish cgroup cleanup, cross-process filesystem CAS, network isolation, credential separation, or container namespace safety; those remain integration/acceptance properties.

## 5. Release and clean-host acceptance

`scripts/verify/verify-loki-release.sh` is a release-artifact gate, not a substitute for A14 clean-host/adversarial acceptance. It invokes the disposable Compose topology acceptance directly; that acceptance requires `setfacl`, so the workspace ACL gate cannot silently disappear on the verifier host.

The current release verifier still does not make every optional development integration above mandatory. A13/A14 must wire the candidate artifacts into the integration/acceptance checks that are part of the released milestone and fail when a required fixture or check is absent. Required browser, MCP-only project execution, credential-bound workload, distinct-release update/recovery, and sandbox/permission checks may not be reported as successful release coverage when their test was skipped.

Release evidence records at least the source commit, core/browser image digests, effective policy/config generation, candidate binary identity, exact commands, pass/fail/skip counts, and recovery result. A skip is acceptable only for a capability explicitly outside the released milestone; otherwise it is a failed gate.

## 6. Current baseline interpretation

At the reviewed baseline, full normal and race runs failed in `internal/auth`, `internal/daemon`, `internal/toolchain`, and `internal/packaging`, and nine integration tests were skipped. The classification is:

- `internal/auth` and `internal/daemon`: deterministic test-fixture defects caused by ambient umask assumptions; keep in the default tier and fix the fixtures.
- `internal/toolchain`: R9 product defect; keep in the default tier and do not skip or relax the expected executable mode.
- Compose topology acceptance: explicit Docker + POSIX ACL integration prerequisite. The disposable script requires `setfacl` and fails when it is absent; lifecycle transaction coverage belongs to the Go host-manager acceptance rather than `internal/packaging` shell tests.
- Chromium/CDP, real devtools, project execution, root permission, OCI archive, and real OCI Job tests: explicit integration prerequisites listed above. The real OCI fixture now covers A05 plus A06/A07 network/endpoint/preview behavior, and the self-contained runner supplies its normal local-Docker inputs. Required release/acceptance coverage closes only when that runner actually records a pass on a supported host.

This classification does not claim that the current Go candidate is release-ready. It prevents environment prerequisites from obscuring product regressions while the architecture remediation proceeds.
