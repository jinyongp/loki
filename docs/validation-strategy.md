# Validation tiers and prerequisites

This document defines which Loki checks must be deterministic on an ordinary developer checkout and which checks require an explicit disposable host/runtime fixture. A missing prerequisite is not allowed to turn a product regression into an environment-only skip.

## Validation profiles

Validation status is always reported as one of the following profiles. Passing a lower profile must never be described as passing a higher one.

| Profile | Canonical command or gate | Required environment | What a pass means |
| --- | --- | --- | --- |
| source | `sh ./scripts/verify/verify-source.sh` | Ordinary checkout with Go | Deterministic tests, vet, build, architecture, module tidiness and diff hygiene passed. Fixture-gated integration is explicitly excluded. |
| race | `sh ./scripts/verify/verify-race.sh` | Ordinary checkout with Go race support | The source suite's exercised Go paths are race-clean. Process, container and OS integration is explicitly excluded. |
| preflight | `sh ./scripts/verify/verify-preflight.sh [--oci]` | Go and race support; `--oci` additionally requires Linux, Docker, Buildx/BuildKit and the OCI network fixture | source + race passed. With `--oci`, real self-contained OCI Job acceptance also passed and missing OCI prerequisites fail. OCI coverage is reported explicitly. |
| exact-candidate | `LOKI_SIGNING_KEY_FILE=/path/to/synthetic-key sh ./scripts/verify/verify-release.sh CANDIDATE CORE@sha256:... BROWSER@sha256:...` | Linux release-engineering host plus immutable candidate artifacts and a synthetic signing key | The exact candidate passed source/race, exact-image OCI authority, cross-path authority, bootstrap, candidate lifecycle and Compose browser/signing acceptance. Native Windows/WSL and publication are still outstanding. |
| publication | GitHub release workflow acceptance, publication and public-install jobs | GitHub-hosted Linux and Windows/WSL clean hosts, immutable candidate and previous immutable release | All independent exact-candidate domains, native Windows/provider/WSL gates, immutable publication, installer deployment and public Linux/Windows installation passed. Only this profile establishes release completion. |

Two rules apply across profiles:

- An integration test may skip only outside a profile that requires it. Required profiles must provision the fixture or fail before claiming a pass.
- A defect first discovered in exact-candidate or publication validation must gain deterministic or lower-tier integration regression coverage where technically possible before the release is retried. Release-only discovery is evidence of a missing lower-tier test, not a reason to leave the defect release-only.

Independent acceptance domains must execute independently. Browser, signing, OCI, authority, recovery and bootstrap failures should be reported in the same release run whenever their fixtures do not depend on one another; a serial fail-fast script must not hide unrelated defects.

### Managed WSL OS contract

`internal/host/appliance/requirements.tsv` declares boot packages, exact image
build versions, required files, executable permissions, systemd units, and
package version peers. The rootfs build and offline archive verifier read this
file; the Linux CLI embeds it for diagnostics and approved reconciliation.
Change the contract and `packaging/wsl/apt-delta.lock` together when the image's
package transaction changes. Runtime checks report the embedded contract hash
and preserve installed OS versions rather than enforcing the image build pins.

Source tests cover each missing file, execute permissions, repeated repair,
PAM/systemd pairing, skipped kernel-dependent kmod units, repository failure,
unrelated failed services, candidate identity, and doctor failure with a healthy
runtime. Offline archive checks additionally verify contract bytes and appliance
directory permissions. They cannot establish systemd boot health.

Native WSL acceptance calls `host appliance check` after installation, recovery,
and cold startup, and checks the default user from a real WSL user invocation.
The recovery scenario removes boot prerequisites only from its owned disposable
distribution, requires doctor to fail, repairs twice, and verifies Loki health.
Public Windows acceptance checks fresh installation and cold startup after an
upgrade from the previous published release. A successful update must restore
OS prerequisites as well as the Loki runtime.

### Local validation and release entrypoints

Use the complete preflight for a completed implementation batch:

```sh
sh ./scripts/verify/verify-preflight.sh
```

Commit and push the intended source, then start a release through:

```sh
sh ./scripts/maintainer/release.sh                 # patch release
sh ./scripts/maintainer/release.sh minor           # minor release
sh ./scripts/maintainer/release.sh --validate-only # candidate CI without publication
```

The release entrypoint always invokes the existing source + race preflight.
Any failed check prevents workflow dispatch. It requires a clean main checkout
matching the live origin/main before and after validation, and passes the
validated commit to CI. CI rejects a different commit before artifact builds
or acceptance start. There is no local validation bypass option.

`--oci` adds the disposable local OCI fixture; `--check-updates` requests live
dependency freshness checks in CI. The command returns after dispatch and does
not watch the run. A successful dispatch is not release completion.

Direct GitHub workflow dispatch with `publish=false` remains available for CI
diagnosis and still runs all required CI gates. Publication requires the
locally validated source commit input; an unbound publication request fails
before the expensive jobs start. The commit input binds source identity, not
a cryptographic proof of local execution; the full CI gates still establish
publication evidence. Narrow source/race commands remain useful while
developing and as the parallel CI job entrypoints.

### Release fan-out and publication barrier

The release workflow deliberately separates candidate identity from validation. `preflight` only establishes that the workflow is operating on the intended main-branch commit and resolves the candidate version/tag. After that point, independent evidence fans out:

- `release-contracts` runs the early release contract subset. `check_updates=true` additionally requires live action, metadata and container freshness checks; the default validates the selected pinned candidate without requiring upstream latest versions;
- deterministic `source-gates` and `source-race` use the pinned ripgrep artifact from `release-inputs`. They run independently of one another. Self-contained `oci-gates` and native Windows validation have their own fixtures;
- `release-inputs` and the immutable candidate build do not depend on independent validation results, so a pin, source, race or OCI defect does not hide build/acceptance evidence;
- candidate-dependent Linux acceptance, WSL acceptance, and Windows provider acceptance depend only on the resolved candidate and the artifact they actually require, not on unrelated validation jobs;
- `publish` is the fan-in barrier. It requires release contracts, source, fixture-backed source runtime checks, race, source OCI, native Windows, every exact-candidate Linux domain, WSL and Windows provider acceptance. Any failed or skipped required gate blocks publication. Live dependency freshness is required when the operator selects `check_updates`;

This structure intentionally spends more CI after a failure in exchange for a complete defect set from one release attempt. A fixture or artifact construction failure may still make dependent checks impossible; such checks are reported as blocked by that concrete prerequisite rather than being treated as successful or silently omitted.

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
sh ./scripts/verify/verify-preflight.sh
```

This includes tests, vet, command builds, the executable architecture checker,
module tidiness, diff hygiene and race detection. Testing the checker package
alone does not check the repository dependency graph.

The source profile owns the full `go vet` pass. Tests and race checks disable
their implicit vet pass, and the explicit build links command binaries while
tests compile the remaining packages. Ordinary source, race and repeated CI
subsets use Go's cache; changed code, tests and observed inputs invalidate it.
External-fixture and exact-candidate acceptance retain `-count=1` so retained
cache results cannot stand in for the current candidate or host fixture.

Validation checks behavior and stable contracts. Source/build pins must remain
explicit and immutable; tests accept valid pin updates without copying each
version and digest into a second hardcoded inventory. Docker and Compose
support minimums describe compatibility floors, while appliance pins identify
selected versions. Minimums must fit the selected appliance rather than track
each upstream release. Generated Python caches and package metadata in a
developer checkout are outside current source-entrypoint checks.

Tests that are explicitly classified as integration checks may report a clear skip when their prerequisite is absent during ordinary development. Everything else in the default suite must be independent of ambient host state such as umask, user home contents, network access, or a previously installed Loki instance.

File permissions, archive containment, auth and daemon behavior are deterministic
product checks. Their fixtures apply explicit modes and cover safe and unsafe
paths without relying on the developer's umask. These checks stay in the default
suite; integration prerequisites apply only to tests that use external fixtures.

## Validation ownership

| Boundary | Check owner | Purpose |
| --- | --- | --- |
| MCP request shape | The resolved tool input schema | Validate types, required fields, actions and unknown fields once before the handler. Missing required-field diagnostics use the decoded input; errors never expose submitted values. |
| Workspace read size | The workspace service | Clamp positive requested counts and depth to configured bounds; report pagination and truncation so a bounded result is explicit. |
| Mutations and protected resources | The owning service and each authority boundary | Preserve file/index preconditions, replay identity, confinement, credential separation, persisted-state integrity and resource ownership. Public schema validation cannot establish these stateful facts. |
| Go source | The source profile | Run tests, full vet, command linking, architecture, module tidiness and diff hygiene once per coherent batch. Reuse valid Go cache results. |
| Concurrent Go behavior | The race profile | Exercise Go memory race checks separately from container and process lifecycle checks. |
| CI workflow | Parsed jobs, prerequisites, pinned actions and gate execution tests | Preserve required acceptance domains and publication prerequisites while allowing job order, step labels and valid dependency pins to change. |
| Candidate/host integration | Explicit integration and release acceptance | Exercise actual pinned binaries, images, host permissions and recovery fixtures with fresh test execution. |
| Dependency freshness | The optional `check_updates` release input | Query live upstream metadata when requested; immutable pinning and candidate integrity remain part of ordinary validation. |

## 3. Explicit integration checks

Integration checks may require a disposable external executable, host identity, or artifact. Their prerequisite must be visible in the command and in a skip/failure message.

| Capability | Check | Required fixture |
| --- | --- | --- |
| Release-bound source-free bootstrap matrix | `./scripts/verify/accept-bootstrap.sh` | Release-engineering host with Go for the test harness. The simulated install host uses an empty working directory and empty development-tool PATH, exercises both Ubuntu 24.04 native and WSL2 identities, and executes only the privately staged host binary after manifest-bound length/SHA-256 verification. The fixture rejects release-tag, manifest and host-binary identity drift. |
| Compose topology/isolation smoke + workspace ACL behavior | `LOKI_IMAGE=<digest-pinned-image> ./scripts/verify/accept-compose.sh` | Disposable Linux/WSL2 Docker host with Compose, Buildx/BuildKit, and `setfacl`. This gate checks the portable container topology, role isolation, optional profiles, restart behavior, derived-image invariants, and the minimal workspace ACL preparation only. Host backup/update/rollback is validated by A11 host-manager acceptance instead of a shell lifecycle implementation. |
| Chromium/CDP | `LOKI_REQUIRE_BROWSER_TESTS=1 LOKI_TEST_CHROME=/absolute/chromium go test ./internal/integrations/browser/...` | Explicit Chromium binary; `LOKI_TEST_CHROME_LIBS` when the candidate needs a non-default library path. |
| Real devtools broker process | `LOKI_DEVTOOLS_BINARY=/absolute/devtools go test ./internal/devtools -run TestRealProcessInheritsBrokerSecrets` | Pinned devtools candidate binary. Synthetic secret only. |
| Locked project execution contract | `LOKI_E2E_DEVTOOLS=... LOKI_E2E_PNPM=... LOKI_E2E_NODE=... LOKI_E2E_CHROMIUM=... go test ./internal/e2e -run TestProjectExecutionContract` | Four absolute candidate executables plus its isolated fixture/cache directories. |
| Runner/vault OS permissions | release gate builds `./internal/execution` as a test binary and executes `TestLinuxRunnerCanWriteStateButCannotReadVaultKey` under effective UID 0 | Disposable Linux host only. The probe creates synthetic root-owned vault material, drops to the synthetic runner UID, proves runner-state write access and vault-key read denial, and must run under UID 0 rather than skip; never run it on a production host. |
| OCI archive contents/reproducibility | `LOKI_OCI_ARCHIVE=/absolute/archive [LOKI_OCI_ARCHIVE_REPEAT=/absolute/archive2] go test ./internal/packaging -run TestOCIArchiveContents` | Built OCI archive(s), not a mutable tag. |
| Real OCI Job lifecycle + A06/A07 network/authority acceptance | `./scripts/verify/accept-oci-jobs.sh` for a self-contained source fixture, or `LOKI_OCI_ACCEPTANCE_IMAGE=<repo@sha256> ./scripts/verify/accept-oci-jobs.sh` for the exact release core image | Disposable Linux host with an accessible local Docker daemon on a Unix socket, Docker Buildx/BuildKit, and outbound access to the committed default allowlisted authority `registry.npmjs.org:443`. Self-contained mode starts a loopback-only temporary registry and builds/pushes the current source fixture; exact-image mode requires an immutable `repo@sha256` reference, pulls it, and does not rebuild or substitute the workload image. Both modes derive the Docker peer UID, create or consume an explicit shared workspace, require every `TestRealOCIJob*` case, including setsid detached-descendant cleanup, and remove only runner-owned temporary resources. Non-default layouts may override `LOKI_TEST_DOCKER_SOCKET`, `LOKI_TEST_DOCKER_WORKSPACE`, `LOKI_TEST_EGRESS_ALLOWED_AUTHORITY`, `LOKI_TEST_DOCKER_PEER_UID`, `LOKI_TEST_WORKLOAD_UID`, `LOKI_TEST_WORKLOAD_GID`, or `LOKI_OCI_ACCEPTANCE_REGISTRY_IMAGE`. The test process must be able to reach daemon-host loopback because Job endpoints are intentionally published on `127.0.0.1`. |

### A06/A07 network acceptance semantics

The A06/A07 source slice has deterministic coverage for domain creation/cleanup, per-Job proxy authentication and forwarding configuration, loopback/unique host-port parsing, durable endpoint leases, exact-resource recovery, public Job schema/handler authority filtering, preview lease revalidation, packaging roles and architecture boundaries. Those checks are not a substitute for a real supported-host network fixture.

The real OCI fixture now contains the A06/A07 scenario: it runs a digest-pinned Loki workload through `dependency-install`, verifies proxy authentication, denied and allowlisted CONNECTs plus direct-egress denial, publishes a declared endpoint through the trusted gateway to a loopback-only ephemeral host port, reopens the exact resource domain through a fresh Engine, exercises HTTP and WebSocket through `preview_publish(action=job)`, and proves that replacing the lease identity while reusing the numeric host port invalidates the old share. Deterministic replacement-resource tests remain responsible for deliberately forged gateway/network incarnations.
The real OCI authority matrix also includes `TestRealOCIJobDetachedDescendantCleanup`: the workload launches a new-session (`setsid`) descendant that continuously updates a host-observable synthetic heartbeat, then Loki cleans the exact Job resource and proves the descendant can no longer advance the heartbeat. Release acceptance supplies `LOKI_OCI_ACCEPTANCE_IMAGE` from the candidate build output so this test and the network/endpoint suite run against the exact immutable release core image rather than a rebuilt lookalike.

A06/A07 runtime acceptance requires `./scripts/verify/accept-oci-jobs.sh` to pass on a supported disposable Linux Docker host. For A14 release-artifact authority closure, the same runner must use `LOKI_OCI_ACCEPTANCE_IMAGE=<repo@sha256>` so the exact candidate core image is exercised. The underlying tests still require `LOKI_REQUIRE_OCI_JOB_TESTS=1`; the runner sets it and all normal fixture inputs automatically. An ordinary direct `go test` run without that require flag intentionally reports the real OCI cases as skipped; such a skip is implementation evidence only and cannot be counted as release acceptance.

An integration test may skip only when run outside a gate that declares it required. A gate that requires the behavior must provide the fixture or convert its absence to failure. Do not add generic `CI` checks or silent fallback fixtures that make it unclear which environment was actually tested.

## 4. Static/race checks

`go vet ./...`, `go build ./cmd/...`, and the architecture checker are deterministic checks. `go test -race -vet=off ./...` follows the same prerequisite classification as the default test suite: explicitly classified integration cases may skip, but ordinary unit/product regressions still fail.

The race detector proves only races observable in the exercised Go memory model. It does not establish cgroup cleanup, cross-process filesystem CAS, network isolation, credential separation, or container namespace safety; those remain integration/acceptance properties.

## 5. Release and clean-host acceptance

`scripts/verify/verify-release.sh` is a release-artifact gate, not a substitute for A14 clean-host/adversarial acceptance. It invokes the disposable Compose topology acceptance directly; that acceptance requires `setfacl`, so the workspace ACL gate cannot silently disappear on the verifier host.

The GitHub release workflow makes the runner/vault permission probe and exact-image OCI authority suite non-optional release-acceptance steps: the permission test is compiled as an ordinary runner process and executed as root only for the disposable UID-drop probe, while `LOKI_OCI_ACCEPTANCE_IMAGE` is bound to the build job's immutable core-image digest. Either failure blocks publication; neither path accepts a skipped integration test.

A14 also runs `scripts/verify/accept-authority-matrix.sh` against that same digest-pinned core image. The matrix extracts and verifies the image's pinned devtools binary, then exercises dedicated secret tools, direct broker/generic managed-process calls, repository dependency hooks, launcher/executor authority inputs, network proxy credentials/destinations, toolchain archive trust, stale host plans, and strict MCP contracts. The exact-image OCI suite separately exercises project binaries, `/bin/sh` interpreter calls, direct/bypass network attempts, endpoint leases, and a new-session detached descendant while proving control sockets, Docker authority, runtime/vault paths, and protected environment values are unavailable to the workload.

The current release verifier still does not make every optional development integration above mandatory. A13/A14 must wire the candidate artifacts into the integration/acceptance checks that are part of the released milestone and fail when a required fixture or check is absent. Required browser, MCP-only project execution, credential-bound workload, distinct-release update/recovery, and sandbox/permission checks may not be reported as successful release coverage when their test was skipped.

Release evidence records at least the source commit, core/browser image digests, effective policy/config generation, candidate binary identity, exact commands, pass/fail/skip counts, and recovery result. A skip is acceptable only for a capability explicitly outside the released milestone; otherwise it is a failed gate.

## 6. Reporting results

Report the executed profile and its result, along with any required check that
failed or lacked a fixture. A passing source or race profile establishes its
own coverage. Exact-candidate and publication results require their pinned
artifacts and supported host fixtures. Local preflight includes OCI coverage
only when `--oci` was requested and passed.
