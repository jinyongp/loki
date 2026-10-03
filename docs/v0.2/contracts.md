# Tools contract foundation

Implementation: `internal/tools`. The contracts define catalog validation, dependency selection, observed readiness, artifact identities, execution-host configuration and owned operation transitions. The portable `cmd/loki-manager` consumes configuration and artifact contracts; standalone browser invocation also checks fresh management state. Combined/full-runtime composition and final acceptance remain pending.

## Manifest and resolution

The new tool manifest schema starts at `1`. The exact shared release identifies a stable `0.2.x` artifact train, initially `0.2.0`. A manifest declares one module ID, supported execution-host OS/architecture/mode combinations, private module prerequisites, and public MCP binding names. The registry snapshots validated inputs, rejects duplicate module IDs, missing prerequisites, cycles and mixed release trains, and resolves selected modules in deterministic dependency order. Module manifests will be owned by the corresponding `modules/<tool>` implementations when those slices are built.

Manifest JSON uses strict known fields, one document and a 1 MiB input limit. IDs are lowercase words separated by hyphens. Binding names support engine-qualified dot namespaces and upstream underscore names. Concrete engine discovery and the full transport's schema forwarding remain browser/composition work.

`Target` refers to the execution host. A Windows desktop client connected to WSL resolves a Linux target. Supported vocabulary is Linux, Windows or macOS (`darwin`), amd64 or arm64, and `project-host` or `full` mode. A vocabulary entry is not a product support claim: resolution requires the exact tuple to be declared by every implementation prerequisite. This slice contains test fixtures, not a production support catalog.

`Requires` determines the implementation closure. Explicit selection determines public tool bindings. For example, Git can require a private execution service while exposing only Git bindings. Selecting browser alone resolves browser alone when its manifest has no module prerequisites. Selecting zero tools is valid for management-only installation. Selecting two modules that claim the same MCP binding is rejected before registration.

## Observed state and discovery

Installation, desired public exposure (`enabled`) and resource readiness are separate observations. An absent module has unknown readiness and no installed release. Installed resources report unknown, ready or degraded readiness. Resource readiness for a private prerequisite is independent of that prerequisite's public exposure flag.

Available bindings require a selected and enabled module, its installed release matching the plan, ready resources, and the same checks for its complete prerequisite chain. Missing, degraded or mismatched observations suppress dependent discovery. Invalid observations return an error. Private services do not add public bindings when the host only selected their consumer.

This model computes discovery from a snapshot. Runtime composition must recheck authorization and current state for each invocation, including cached MCP calls. Host lifecycle must reconcile private service use and exact resource ownership when selections change; this contract does not start or stop processes itself.

`BindingSet` arbitrates actual static/engine registrations in separate tool and
resource namespaces. Conflicting refreshes preserve the previous owners.
`ToolGate` rechecks the pinned release/mode and desired selections on each call.
For full service adapters, it reads management's single atomic
`control/state.json` snapshot and distinguishes a public selection from a
resource in its enabled private prerequisite closure. Mount the containing
control directory read-only, so file replacement remains visible. That mount
contains public activation/artifact metadata; tool data, provider credentials
and operation journals stay outside it. Module/resource readiness remains a
separate observation and must be reconciled before host publication.

## Current evidence and remaining gates

Archive extraction accepts regular files and directories. Native macOS browser
ZIPs additionally retain relative Chrome framework links beneath
`chrome/<vendor>.app/Contents/`. A link and its fully resolved chain must remain
inside that same app. Regular files are extracted before aliases, and archive
members beneath an alias are rejected regardless of ordering. Links outside
Chrome apps, dangling/cyclic links, cross-app links, absolute targets and special
files are rejected. The trusted native target selects this narrow policy;
other modules and platforms retain the regular-file/directory contract. Candidate
preparation verifies Chrome's vendor seal; the installed seal and native startup
still require final platform acceptance.

Focused tests cover invalid manifest documents, target vocabulary, duplicate IDs/targets/bindings, missing and cyclic prerequisites (including unselected catalog entries), deterministic private/public resolution, unsupported private targets, immutable catalog inputs/results, disabled or unready discovery, and stale release observations. The architecture policy classifies this dependency-free contract package and permits no new internal imports.

Release catalogs bind module manifests and exact target artifacts to one release. Acquisition URLs use HTTPS, artifact identity includes its module/release/target/digest, and installation checks exact byte length and SHA-256. Execution-host configuration distinguishes local, WSL and SSH operation. Operation records persist prepared/staged/committed/aborted transitions and owned generation identity for recovery. Call-time authorization recomputes binding availability from a fresh state snapshot. Dynamic engine bindings across combined servers remain composition work. All new code and fixtures await the final validation phase.
