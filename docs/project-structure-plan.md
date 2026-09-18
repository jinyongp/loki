# Loki package structure and dependency plan

Status: implementation target based on the repository inspected on 2026-09-17 at `50a0d65d6cacfcae5673889d004a08f8100df392`. Section 1 preserves that reviewed baseline; landed structural slices are recorded in the relevant migration sections. This document refines the package-ownership part of the [architecture improvement plan](architecture-improvement-plan.md); it does not replace the [readiness findings](go-readiness-review.md) or the agreed [installation behavior](installation-distribution-plan.md).

## 1. Reviewed baseline dependency evidence

The current Linux/amd64 build graph was obtained with `go list -mod=readonly -json ./...`; imports were separated from test imports. It contains 36 package directories, 34 with production source, 110 production Go files, 15,238 production lines, and 85 direct imports between packages in this module. There are 33 immediate directories under `internal/`. These are observations, not target counts or complexity thresholds. Build-selected imports do not cover every possible build-tag combination.

| Package | Production size | Direct internal dependencies | Structural observation |
| --- | --- | --- | --- |
| `internal/service` | 23 files / 2,227 lines | 24 | Transport handlers, use cases, credential/process wiring and service startup share one Go package. |
| `cmd/loki` | 16 files / 1,208 lines | 21 | The command package combines operator CLI, service composition, maintenance and process execution entrypoints. High fan-out is legitimate for composition, but not for hidden business or authorization policy. |
| `internal/gitops` | 3 files / 601 lines | 6 | Git behavior receives global Config and imports encrypted state infrastructure for generic persistence. |
| `internal/workspace` | 6 files / 1,137 lines | 5 | Files, Git-based removal/patching, revisions, images and bundles share global configuration and execution helpers. |
| `internal/auth` | 2 files / 257 lines | 3 | JWKS refresh imports `browsernet`, which imports `portguard`; generic safe dialing is owned by a browser-named package. |
| `internal/devtools` | 4 files / 575 lines | 3 | A CLI adapter directly imports secret management as well as process execution. |
| `internal/deployment` | 1 file / 222 lines | 0 | No production package in the observed graph imports it. Its validation cannot be assumed to run during startup merely because the package exists. |

Concrete dependency paths clarify why folder depth alone is not the issue:

```text
auth -> browsernet -> portguard
egress -> browsernet -> portguard
gitops/checkpoint -> state
workspace/revisions -> state
devtools -> secret -> state
service -> MCP SDK + mcpserver + rpc + almost every feature
contract -> MCP SDK
```

`auth/refresh.go:27` uses the browser-owned dialer. `gitops/checkpoint.go:120` and `workspace/revisions.go:55,63` import `state.AtomicWrite`, even though they do not need the vault's encryption or migration semantics. `gitops.Controller` and `workspace.Files` accept the entire `config.Config`. The `service` package's files distinguish concerns by filename, but Go's import and symbol boundaries are at package-directory level, not filename level.

Names also conceal different responsibilities. Today's `policy` is predominantly traversal-resistant filesystem access, not the future administrator authorization engine. Today's `execution` is an environment/layout contract, not the future job supervisor. Today's `contract` is an MCP catalog tied to its SDK, not a transport-neutral domain contract. Move by actual responsibility, not by matching old and new names.

There is no build-breaking production import cycle in this observed build. That does not establish appropriate layering, credential separation, or operational correctness. A package-level acyclic graph can still contain the wrong dependencies and a large mixed-purpose composition package.

## 2. Recommended organization

Use responsibility-oriented groups, vertical feature packages inside them, and narrowly owned adapters. Keep one Go module and release train initially. Here, a feature module means a package ownership boundary, not a separate `go.mod`.

Avoid both extremes: one flat list of unrelated `internal` packages, and a repository-wide `models/controllers/services/repositories` hierarchy that scatters each feature across every technical layer. A feature's use cases, types, tests and private implementations should be navigable together. Only truly shared platform primitives and inbound transport bindings live outside it.

The target shape is an ownership map, not a request to create every directory in advance:

```text
cmd/
  loki/                       user-facing host/operator CLI; keep loki host ...
  loki-gateway/               container-internal MCP/HTTP role entrypoint
  loki-executor/              container-internal job-supervisor role entrypoint
  loki-launcher/              container/host-internal minimal privileged launch role
  ...                        separate role binaries only when isolation/dependency closure requires them

internal/
  app/                        role-specific composition; no business rules
    host/
    gateway/
    executor/
    launcher/
    brokers/                  browser, signing and provider wiring as needed

  control/                    authority and protected control contracts
    identity/                 trusted principals and narrow resource identifiers
    policy/                   grant evaluation and effective-policy generation
    operations/               mutation outcomes and reconciliation contracts
    credentials/              platform credentials, never app-secret profiles
    audit/                    event contract and protected audit persistence

  work/                       ordinary constrained development capabilities
    jobs/                     job lifecycle, output and cancellation
    workspace/                file/repository identity, edits and recovery
    toolchains/               selectors, executable plans and provisioning
    secrets/                  granted application-secret delivery
    network/                  workload egress grants and enforcement adapters
    endpoints/                owned service locations and leases

  integrations/               provider/optional capability boundaries
    github/
    browser/
    signing/
    sharing/                  artifact links and preview use cases
    devtools/                 optional CLI integration, not execution authority

  host/                       operator lifecycle; never a workload API
    config/                   host file parsing and effective config assembly
    releases/                 authenticated metadata and release generations
    lifecycle/                install, prepare/apply, rollback and removal
    diagnostics/              operator health and recovery views
    assets/                   canonical embedded Compose/config templates

  transport/                  request/response translation, not domain policy
    cli/
    mcp/
    rpc/                      split framing, clients and role handlers
    http/                     connection auth and sharing/preview routes

  platform/                   reusable mechanisms, no feature authority
    safeio/                   pinned-root access, atomic publication and locks
    proc/                     OS process/identity primitives
    cgroup/                   Linux subtree/resource primitives
    sandbox/                  finite validated workload-launch backend; no raw daemon API
    netguard/                 address validation and pinned dialing mechanics
    securestore/              authenticated encrypted persistence mechanics
    buildinfo/

packaging/
  images/                     OCI build recipes and image packaging checks
  native/                     maintainer-only systemd candidate assets
scripts/
  build/
  verify/
  maintainer/
tools/
  archcheck/                  package and role dependency verification
  cataloggen/
  toolchainfetch/
tests/
  architecture/
  integration/
  acceptance/
examples/
  config/                    safe operator examples, not active policy
docs/
legacy/python/                retained until separately approved retirement
```

`app`, `control`, `work`, `integrations`, `host`, `transport` and `platform` are grouping directories, not umbrella packages exporting every child. They are not Go modules and not automatically separate services. Public CLI spelling need not change when service binaries are split.

Separate role entrypoints are recommended for inspecting build dependency closures as well as deployment privileges. A gateway build should not pull in a local vault implementation or privileged launcher; a launcher should not link the MCP SDK, browser driver or repository scripts. Binary separation supports review but does not confer security by itself: mounts, identities, credentials, sockets and network grants still require real acceptance tests. Exact optional component binaries follow the actual deployment boundaries rather than one binary per package.

## 3. Dependency direction and module internals

Arrows here mean source imports, not runtime RPC calls:

```text
cmd/<role> -> app/<role>
                 |-> inbound transport binding -> feature facade
                 |-> feature local/remote adapter -> feature facade
                 |                                  |-> explicit lower contract
                 |-> approved platform wiring       |-> pure identity/outcome types

feature implementation adapters -> platform mechanisms
platform mechanisms -> standard library / approved low-level libraries
```

An OCI adapter implements the interface consumed by jobs and imports that contract; jobs must not import the adapter. At runtime jobs can call the injected adapter without reversing the source dependency. An RPC client belongs outside the consuming domain for the same reason.

Apply these rules per package, not just per first-level directory:

| Kind | Allowed dependency shape | Prohibited dependency shape |
| --- | --- | --- |
| Pure authority contracts (`control/identity`, policy evaluator, outcome values) | Stable narrowly scoped values and standard-library computation | MCP/HTTP handlers, host commands, jobs, provider SDKs, process creation or secret storage |
| Feature facade / use case | Consumer-owned ports and explicitly reviewed lower feature contracts | Its concrete adapters, `app`, host installation, inbound transport or a sibling's private implementation |
| Feature local/remote adapter | Its owning facade, approved sibling facade, transport client/framing where needed, platform primitives | Role-wide service locator, sibling private store or arbitrary host authority |
| Inbound MCP/HTTP/RPC/CLI binding | Reviewed feature contracts and protocol codecs | Constructing vaults/workers, invoking arbitrary processes or mutating policy behind a use case |
| Role composition | Exactly the facades and implementations required by that role | Implementing feature behavior or importing another complete role just for a helper |
| Reusable platform mechanism | Standard library, approved OS libraries, other acyclic platform primitives | Feature policy, credentials by name, global Config, MCP DTOs, provider semantics or service-name constants |

A broad `work/** -> work/**` or `integrations/** -> integrations/**` allowance would recreate today's coupling. Keep explicit feature-level edges. Cross-feature workflows consume a small interface; an outer adapter composes providers where necessary. In particular, jobs must not import toolchains if toolchains already import jobs to extract artifacts. Define the actually required resolver/extraction ports on their consumers and wire them outside both cores. Share a tiny immutable value contract only when it has real consumers and avoids an otherwise justified dependency; do not create a universal model registry.

### Example: a feature that needs private adapters

```text
internal/work/jobs/
  job.go                      job states and public immutable values
  service.go                  lifecycle use cases
  ports.go                    interfaces actually consumed by the use cases
  service_test.go
  local/                      executor-side constructor/adapter
    wire.go
  remote/                     gateway-side job client
    client.go
  internal/
    journal/                  private durable implementation
    oci/                      private launch-protocol adapter, not raw Docker API
```

The core `jobs` package must not import `local`, `remote` or an implementation that imports `jobs`. This keeps adapter-to-consumer imports acyclic. `local` can import both `jobs` and `jobs/internal/journal`; role wiring imports `jobs/local`, not the hidden journal. `remote` exposes only the job operations the gateway may request. The launcher client still speaks a finite validated protocol; directory naming does not authorize launch requests.

Nested `internal` makes private implementation import restrictions enforceable by the Go toolchain: a package outside `internal/work/jobs` cannot import `internal/work/jobs/internal/journal`. It does not prevent that consumer from importing the exported `jobs/local` adapter; the role/import checker supplies that additional restriction. White-box implementation tests stay within their module subtree; root acceptance tests use exported facades or real transports.

Do not stamp this full template onto every small feature. A cohesive package with a few files can remain one package. Split only for a meaningful ownership, dependency or isolation boundary. Feature-owned assets such as browser JavaScript and private CDP plumbing stay with browser, not at the repository root. Factories return concrete implementations; introduce interfaces at actual consumption points, not a global `interfaces` package.

## 4. Specific corrections to the present ownership

### Split service, do not rename it to app

The current `internal/service` is the first large split. Move construction and lifecycle wiring from `mcp.go`, `runtime.go`, `*_role.go` into the appropriate `app/<role>` packages. Move MCP bindings from `git.go`, `workspace.go`, `browser.go`, and similar files into `transport/mcp/<capability>` bindings. Internal RPC operation bindings go into their role-specific transport packages. Move the actual preview, credential, GitHub and endpoint use cases into the owning feature packages.

Some existing files contain both RPC and MCP bindings plus use-case code. Split by function, not whole filename, and retain the behavioral regressions. `helpers.go` must not become `app/common.go`: decode helpers belong to transport; execution-environment handling belongs to work/jobs adapters; path validation belongs to its explicit filesystem owner. The final target is removal of `internal/service`, not another all-purpose Service with the same dependency reach.

### Give filesystem and network mechanisms neutral owners

Move traversal-resistant filesystem mechanics from today's `policy` and reusable private-directory/atomic-write helpers from `daemon` and `state` into narrowly named `platform/safeio` packages where semantics really match. Keep high-level workspace permissions and repository identity in `work/workspace`. Do not merge helpers whose overwrite/durability/ownership guarantees differ merely because each writes a file.

Extract public-address classification, DNS validation and pinned dialing from `browsernet` into `platform/netguard`. Browser-specific navigation policy stays in browser, dependency egress selection stays in work/network, and the decision that a local endpoint belongs to a job stays in work/endpoints. A transport authentication refresher can depend on safe dialing mechanics without importing browser behavior or port-termination policy.

S02 status: neutral destination validation, bounded dialing, and generic HTTP/CONNECT proxy mechanics now live in `internal/platform/netguard`; `internal/browsernet` has been removed. Auth, browser, egress, and browser-proxy composition consume the neutral primitive directly. Workload-scoped network grants and endpoint ownership remain A06/S04 work rather than being folded into this platform package.

### Separate configuration sources and consumed options

`host/config` parses operator configuration strictly and compiles it into typed effective component configurations. Each feature accepts only its own immutable Options and granted capabilities. Do not thread one whole server Config through Git, files, browser, execution and every adapter. Project toolchain selector parsing remains in toolchain providers: it is not host-policy parsing even if both use TOML.

Today's `internal/deployment` validators must either participate in the actual preparation/startup path with tests proving invocation, or become explicitly build-time tooling. A standalone validating package is not an enforcing dependency. Runtime policy and enabled capabilities remain immutable operator-owned input, not data inferred from a project file or instruction document.

### Split storage mechanics from the owners of data

The shared encrypted persistence implementation may live in `platform/securestore`, but platform credentials and application secrets require different controllers, storage instances and mounted access. Generic workspace revisions need safe atomic files, not a dependency on the encryption/migration package. Host lifecycle journal, job journal and audit retention have different records and failure semantics; avoid combining all of them into a global State manager.

Provider-specific key use belongs to its protected integration adapter. Application-secret delivery lives in `work/secrets`. No devtools adapter depends directly on either store; approved secret delivery is performed through the job boundary. The `control/credentials` path is not proof of protection unless the role's actual mount/identity and APIs enforce it.

## 5. Migration map for every current Go package

Destinations describe ownership, not mechanical one-to-one moves. Private implementation subpackages are created only when warranted.

| Current package | Target owner / split |
| --- | --- |
| `cmd/loki` | Thin operator command plus separate `cmd/<role>` and `app/<role>` wiring; maintenance into tools/maintainer entrypoints. |
| `internal/admin` | Operator bindings in `transport/cli`; protected use cases in `host` or `control/credentials`; platform import helpers private to their owner. |
| `internal/artifacts` | `integrations/sharing` capability-link use cases; HTTP serving in `transport/http`; bounded payload storage private to sharing. |
| `internal/audit` | `control/audit` event contract and private sink/retention adapter using safeio. |
| `internal/auth` | Core bearer/local connection auth in `transport/http` and trusted principal values in `control/identity`. Cloudflare-specific Access verification/JWKS refresh is not a core-install dependency; retain it only as an optional external-access integration or maintainer migration path if that feature is kept, and never route its safe dialing through browser policy. |
| `internal/browser` | `integrations/browser` facade, local worker adapter and private Chromium driver/JavaScript. |
| `internal/browsernet` | **S02 completed:** package removed; shared validation/dial/proxy mechanics now live in `internal/platform/netguard`. Workload grant enforcement remains targeted at `work/network`. |
| `internal/buildinfo` | `platform/buildinfo`, a leaf package containing build metadata only. |
| `internal/cdp` | `integrations/browser/internal/cdp` until another real independent consumer justifies extraction. |
| `internal/config` | Strict `host/config` loading plus feature-owned Options; neutral syntax helpers extracted only with actual reuse. |
| `internal/contract` | Reviewed MCP definitions/generated artifacts in `transport/mcp`; domain types with their respective feature, never in a global SDK-dependent contract package. |
| `internal/daemon` | Safe file/ownership helpers to `platform/safeio`, Unix framing/listener helpers to transport or platform as appropriate, startup to `app/<role>`. |
| `internal/deployment` | `host/config`/`host/releases` validation used by actual prepare/startup; purely image assertions to tests/packaging tooling. |
| `internal/devtools` | `integrations/devtools`; CLI execution through jobs; secret brokerage removed from this adapter. |
| `internal/devtools/cmd/gencatalog` | `tools/cataloggen` or a devtools-specific generator beneath it; generated catalog stays with the owning adapter. |
| `internal/dockerproxy` | Remove unused transparent forwarding; retain only the inspection/launch capability actually required by the finite `platform/sandbox` boundary, reached through role-specific transport. Never expose a raw daemon socket or generic forwarding API to jobs. |
| `internal/e2e` | `tests/acceptance` or `tests/integration`, explicitly classified by runtime prerequisites. |
| `internal/egress` | `work/network` facade and enforcement adapters; share mechanics, not browser policy. |
| `internal/execution` | Split environment/spec contracts into `work/jobs` and effective host config; it is not itself the new supervisor. |
| `internal/fault` | Feature errors and narrow `control/operations` outcome categories; protocol presentation in transport. Low-level platform errors remain independent. |
| `internal/githubapp` | `integrations/github` with private token/CLI adapters; platform key access through a narrow protected credential operation. |
| `internal/gitops` | Repository behavior under `work/workspace`; Git adapter runs through jobs; snapshots use safe persistence, not vault APIs. |
| `internal/mcpserver` | `transport/mcp`; bind domain facades, reject unknown inputs, keep SDK and wire conventions out of feature cores. |
| `internal/packaging` | `tests/acceptance/packaging` plus focused architecture checks; do not retain a production-looking package containing only packaging tests. |
| `internal/policy` | Filesystem mechanisms to `platform/safeio`; workspace-specific validation to `work/workspace`. Do not simply rename it to authorization policy. |
| `internal/portguard` | OS identity/signal primitives to `platform/proc`; owned service/endpoint policy to `work/endpoints`; no cross-namespace PID interpretation. |
| `internal/previews` | Preview/share use cases in `integrations/sharing`; endpoint lookup through `work/endpoints`; forwarding in HTTP adapters. |
| `internal/process` | Low-level spawning/signals/output mechanics in `platform/proc`; lifecycle and cancellation policy owned by `work/jobs`. |
| `internal/rpc` | `transport/rpc` codec/client/role bindings; principal/grant semantics in control, not guessed by generic transport. |
| `internal/secret` | Application secret use cases to `work/secrets`; platform credentials to `control/credentials`; import/export helpers under the corresponding operator boundary. |
| `internal/service` | Function-level split across `app`, `transport`, `work`, `control` and `integrations`; eliminate the umbrella. |
| `internal/signing` | `integrations/signing` facade and private worker; SSH protocol implementation private; connection binding at transport edge. |
| `internal/state` | Reuse encrypted persistence mechanics in `platform/securestore`; move atomic files/locks to safeio where semantics match; domain state/validation stays owned; offline migration is maintainer-only. |
| `internal/toolchain` | `work/toolchains`; private providers/store/extractor adapters; approved host-native package setup belongs to `host/lifecycle`, not the project-facing resolver. |
| `internal/toolchain/cmd/fetch` | `tools/toolchainfetch`, not a nested command hidden among runtime provider code. |
| `internal/workspace` | `work/workspace` public contract; private filesystem/repository/revision implementation; serving/publishing belongs to sharing/transport. |

## 6. Assets, tests and non-Go boundaries

Keep one canonical source for each deployment asset. The proposed `internal/host/assets` package owns embedded Compose/config templates consumed by the host manager. A root `compose.yaml`, when retained for developers, is a generated view with a reproducible generation check, not a second manually maintained source. Server startup consumes the materialized effective configuration; it must not import the host installer to obtain its defaults.

`packaging/images` owns Dockerfiles and image build inputs; `packaging/native` owns the advanced systemd candidate. Safe sample configuration lives in `examples/config`. `scripts/build`, `scripts/verify` and `scripts/maintainer` contain thin orchestration, not a second implementation of policy or transactional lifecycle. Bundled Agent Skills remain one shared source while both retained Python and Go use them; do not duplicate or move them merely for visual consistency. Generated schema/provenance/notice files identify their generator and input digest. No runtime image silently includes tools, test payloads, raw keys or legacy sources.

Keep unit and white-box tests adjacent to their package. Keep multi-role integration and deployment acceptance under root `tests`, with only required public test fixtures. Move shared fixture helpers only after two real test consumers need them, and never let production import them. Architecture tests distinguish test imports from production imports and do not globally exempt all `_test.go` files from ownership rules.

The current `internal/packaging/source_boundary_test.go` bans a root `tests` directory as part of Python retirement. Replace that shape assertion when introducing Go integration tests: assert absence of legacy Python entrypoints and legacy inclusion in current artifacts, not that a generally named directory can never exist. Update path-relative fixtures, scripts, `.dockerignore`, Docker COPY, embedding, generation, docs and acceptance entrypoints in the same move unit. Preserve synthetic Go compatibility/data fixtures still needed for approved data migration; API compatibility is not required.

## 7. Make the structure enforceable

A directory tree is insufficient. Add a checked-in dependency policy and an architecture check early, before broad moves. The checker is a development tool, not an MCP permission engine.

The initial checker can use `go list -json` and standard Go AST import inspection; a new dependency is not necessary merely to print the graph. It must classify production, tests, generators and supported build targets. Validate the active supported Linux targets and inspect build-tagged source so forbidden imports cannot hide behind a file excluded on the developer's machine.

Required rules:

1. Assign each package an owner and kind: facade, private implementation, adapter, inbound transport, composition, platform or test/tooling. Unclassified new packages fail review.
2. Allow explicit facade dependencies. Reject cross-feature private imports, core-to-own-adapter imports, upward imports into app/transport/host management, and feature dependencies from neutral platform primitives. Use nested `internal` for compiler-enforced visibility where applicable.
3. Restrict third-party APIs by owner: MCP SDK in MCP bindings/generators and protocol tests; Docker/OCI host-control code in the bounded launch implementation; browser CDP in browser internals; TOML parsing in explicit host-config or project-selector adapters, not the job state machine.
4. Inspect transitive dependencies for role entrypoints. Gateway must not reach local launcher, encrypted platform-key store, local Git process execution or Chromium construction. Launcher must not reach MCP handlers, project evaluation, general devtools commands or provider private-key storage. Sharing a low-level type is not a license to import the owner's full implementation.
5. Check exported function/type signatures for leaked SDK/driver types. Prevent replacing a concrete unwanted dependency with `any`, a generic callback dispatcher, global registration or a service locator that merely hides the same authority.
6. Keep construction explicit. Domain constructors do not start listeners, create goroutines or open privileged state implicitly. Composition calls Start/Run, owns Close, and reports startup failures with cleanup in reverse dependency order. Do not self-register capabilities through `init`.
7. Verify enabled-feature descriptions against actual role wiring. Disabled browser/signing must not require sockets, load stores or start workers. A policy generation selects only implemented/authorized adapters; it cannot load a workspace-provided plugin into the gateway.
8. Distinguish compile checks from runtime guarantees: every moved trust boundary still needs the R4-R8/R10 cross-path tests. Import rules cannot detect every filesystem or network side effect and do not prove sandbox safety.

Record the existing forbidden edges as an exact reviewed baseline only while migrating, with an owner and the corresponding S/A work unit. New violations fail immediately; each migrated unit removes its old exceptions. Never add a wildcard legacy exception or claim the repository already meets the target because existing violations are baselined. Finish by removing the temporary exceptions. Test the checker itself with fixtures demonstrating a forbidden import, a permitted adapter, a private implementation access, an SDK type leak and a forbidden transitive role dependency.

## 8. Dependency-ordered implementation slices

These S units refine and accompany A01-A14 rather than adding a separate competing implementation queue. Do not leave package cleanup until A12; enforce ownership as each capability is changed. Keep semantic fixes and mechanical moves in separate reviewable commits where possible, but never leave intermediate commits uncompilable or lose a regression assertion.

| Slice | Main work | Related architecture units | Exit condition |
| --- | --- | --- | --- |
| S01 | Preserve current graph, add ownership policy/checker, classify existing edges and shape-based tests | A01-A02 | Existing baseline explicit; new violations fail; checker fixtures prove both allowed and denied cases. |
| S02 | Extract genuinely shared safeio/netguard/process primitives and pure principal/outcome contracts | A02, early safe A08/A09 fixes | No auth-to-browser or ordinary-revision-to-vault dependency; primitive semantics/regressions preserved. |
| S03 | Split `service`/`cmd` into role composition and inbound bindings; add typed consumer contracts and role build checks | A02-A03, A07 | Gateway has no local project-execution or platform-key-store dependency; launcher closure is narrow; no service umbrella. |
| S04 | Consolidate jobs/workspace/endpoints and split platform/app credentials; route adapters through those boundaries | A04-A08 | No devtools-to-vault coupling; normal MCP development works and complete process/authority tests pass. |
| S05 | Move browser/CDP, sharing, provider, signing and toolchain internals into their owning feature subtrees | A09-A10 | Peer features cannot import hidden internals; disabled integrations are absent from construction; positive feature tests pass. |
| S06 | Consolidate embedded assets, host lifecycle, scripts/tooling, docs and acceptance; remove migration-only import exceptions | A11-A14 | One source per asset, clean graph/role gates, source-free install and real distinct-release recovery acceptance. |

When extraction must be repaired first for safety, land that fix without waiting for all S units. When splitting service would require unsafe unsandboxed generic execution, defer that wiring until A03 is ready; a mechanical package split is not sufficient acceptance. Preserve operator data and the running Python deployment throughout. No layout change authorizes deployment, restart, push, credential migration or early deletion of `legacy/python`.

## 9. Completion and review evidence

The resulting structure is accepted when a developer can find a feature's public contract, its private mechanisms, its adapters, its tests and its source assets without tracing a global Service; the import checker independently verifies the intended edges; and composition/acceptance demonstrates the actual isolation and workflow. Folder count and file-size reduction are secondary observations.

This review ran current package listing and a source/import inventory. Temporary graph and product-file hashes are in `.tmp/package-structure-review/graph.json`; document validation also checks that non-document tracked files and the index remain unchanged. No full test rerun is claimed for this documentation-only refinement. The previous readiness report's failing and skipped integration gates remain unresolved.

## Go reference basis

- [Organizing a Go module](https://go.dev/doc/modules/layout): server logic in internal packages, commands in cmd, cohesive packages and hierarchy.
- [Managing module source](https://go.dev/doc/modules/managing-source): a single root module is the simpler default; multiple modules are for independently versioned needs.
- [Go command: internal directories](https://pkg.go.dev/cmd/go#hdr-Internal_Directories): internal import visibility is based on the subtree rooted at its parent, including nested internal directories.
- [Go code review: interfaces](https://go.dev/wiki/CodeReviewComments#interfaces): interfaces belong to consumers and should follow actual use, not a speculative global abstraction.

These references explain Go mechanisms. The specific responsibility groups and dependency rules above are Loki's proposed architecture, not an official universal Go directory standard.
