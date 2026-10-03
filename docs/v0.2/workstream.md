# Loki 0.2.x workstream tracking

Profile: `loki`. Workstream: `5933a2a0-6c9b-49ff-bf8d-751b99aeb1ed`.

This is the public definition snapshot, including the prerequisite investigation added on 2026-10-03. Current task state, claims, checkpoints and validation evidence live in devtools. Read the canonical specification in [spec.md](spec.md), delivery plan in [plan.md](plan.md) and prerequisite dossier in [preflight.md](preflight.md).

The 2026-10-04 user-approved implementation definitions record source,
documentation and verification fixture delivery. Executed product/native
acceptance remains owned by the final validation tasks below; their required
procedures and references are retained.

Inspect current progress:

```sh
devtools task workstream context 5933a2a0-6c9b-49ff-bf8d-751b99aeb1ed --profile loki
devtools task workstream check 5933a2a0-6c9b-49ff-bf8d-751b99aeb1ed --profile loki
```

## Requirements

### R_SCOPE

Loki 0.2.x is a fresh public CLI/MCP/config/state design in this repository, with reviewable implementation and separately authorized publication/cutover.

### R_MODULES

Tool modules share one source hierarchy and compose through explicit ownership and narrow interfaces. Git owns signing; provider credentials are owner-specific; application secrets are separate.

### R_HOST

Install management first on Windows/Linux/macOS, then selected verified tools and required prerequisites; declare per-tool OS/runtime support and keep install separate from activation.

### R_BROWSER

Provide standalone project-host browser tools using exact official Playwright/DevTools MCP dependencies and actual upstream schemas/capabilities, engine profiles, files/images and diagnostic causes.

### R_REMOTE

Support Windows Codex desktop -> SSH -> existing WSL project-host browser operation with development-server reachability and image/file results, without requiring the full Loki appliance.

### R_LIFECYCLE

tools and integrations use plural collection naming; selected tool lifecycle is observable, retry-safe, ownership-scoped and removable without erasing user data.

### R_COMPOSE

Tool-only and combined/full configurations use the same implementations, require only declared dependencies, prevent duplicate bindings, and expose only selected enabled/ready capabilities.

### R_AUTHORITY

Preserve scoped resources, protected credentials/signing keys and enforced full-runtime boundaries; document standalone authority and optional upstream execution capabilities honestly.

### R_EVIDENCE

Connect specification, plan, tasks and validations in devtools; claim before source work, capture current-basis evidence, and checkpoint accepted decisions/remaining work.

### R_RELEASE

Produce tested 0.2.0 candidate artifacts with exact upstream versions/integrity, notices, manifests and verifiable tool-only/full dependency closures.

### R_PREFLIGHT

Before extending the 0.2 implementation, investigate official package/runtime/transport/authority/distribution contracts and source extraction boundaries. Execute all locally feasible prerequisite probes, record actual failures and external prerequisites, and retain real host/product acceptance as separate gates.

## Acceptance criteria

### A_PREFLIGHT

A reproducible prerequisite dossier records exact browser package versions/integrity/licenses and real MCP catalogs/probes on WSL, official remote-client and OS/runtime requirements, source dependency and privilege boundaries, artifact/lifecycle implications and tests. External Windows app and macOS/Windows host checks have executable prerequisites and remain explicit pending gates. Documented constraints are reflected in the plan before further implementation.

Requirements: `R_PREFLIGHT`, `R_BROWSER`, `R_REMOTE`, `R_HOST`, `R_AUTHORITY`, `R_RELEASE`.

### A_DESIGN

Canonical design maps tool ownership, current coupling, new 0.2 contracts, authority boundaries and the implementation/validation DAG with complete devtools references.

Requirements: `R_SCOPE`, `R_MODULES`, `R_EVIDENCE`.

### A_CONTRACTS

Manifest/registry rejects invalid, duplicate, cyclic and unsupported selections and distinguishes public capabilities from private prerequisites and lifecycle states.

Requirements: `R_MODULES`, `R_LIFECYCLE`, `R_COMPOSE`.

### A_BROWSER

Pinned official engines initialize and expose actual supported tools; real fixture actions, accessibility/network/image/file results and cleanup pass, with a tested capability/runtime matrix.

Requirements: `R_BROWSER`, `R_AUTHORITY`.

### A_REMOTE

Windows desktop app connected through SSH to existing WSL can operate the project-host browser, reach a WSL dev server and receive usable screenshots/files; failures report actual causes.

Requirements: `R_REMOTE`, `R_BROWSER`.

### A_HOST

Windows/Linux/macOS management artifacts perform management-only install and selected tool install/activate/status/diagnose/remove; unsupported tool targets fail clearly and unrelated services/data remain intact.

Requirements: `R_HOST`, `R_LIFECYCLE`.

### A_GIT

Scoped workspace and Git capabilities have separate ownership; Git owns signing and declares its execution adapter; signing on/off and key protection pass without application-secret coupling.

Requirements: `R_MODULES`, `R_COMPOSE`, `R_AUTHORITY`.

### A_PROVIDERS

GitHub API-only use needs its own provider inputs and no local Git, while application secrets retain separate storage/delivery grants; credentials do not leak across modules.

Requirements: `R_MODULES`, `R_AUTHORITY`, `R_COMPOSE`.

### A_ADAPTERS

Execution, sharing and coordination declare real sandbox, content, endpoint, secret and external CLI dependencies; publication verifies endpoint ownership and optional facilities are not globally required.

Requirements: `R_MODULES`, `R_COMPOSE`, `R_AUTHORITY`.

### A_COMPOSE

Browser-only starts with core services absent; representative tool combinations and full configuration use identical implementations, have no duplicate bindings and reject cached calls for disabled capabilities.

Requirements: `R_COMPOSE`, `R_BROWSER`, `R_AUTHORITY`.

### A_DOCS

Fresh 0.2 CLI help/docs/plugins describe tested install/setup/host selection, actual capability/OS boundaries and actionable progress/diagnostics, and distinguish project-host from in-app browser sessions.

Requirements: `R_HOST`, `R_REMOTE`, `R_LIFECYCLE`, `R_BROWSER`.

### A_ACCEPTANCE

Required host, protocol, lifecycle and cross-module/full-runtime acceptance has passing current-basis evidence; unavailable environments remain explicit completion gaps.

Requirements: `R_HOST`, `R_REMOTE`, `R_COMPOSE`, `R_AUTHORITY`, `R_EVIDENCE`.

### A_CANDIDATE

A verified 0.2.0 release candidate contains exact module artifact identities, dependency closures, upstream integrity/notices and tested management-only/tool-only/full installers.

Requirements: `R_SCOPE`, `R_RELEASE`, `R_EVIDENCE`.

## Delivery graph

Investigation evidence is retained. Implementation delivers contracts, browser packaging, portable host lifecycle, workspace/Git, providers and runtime adapters, followed by composition, documentation and candidate preparation. Final browser/client validation then precedes the complete acceptance suite. Product checks run in this final phase.

### 0.2 architecture and canonical workstream documents

Task: `5c817e9d-cb3e-4825-9efe-14a5789e37d2`.

Inspect actual coupling and accepted product decisions; write docs/v0.2/spec.md and docs/v0.2/plan.md and validate devtools requirement/acceptance/task/validation coverage.

Acceptance: `A_DESIGN`. Dependencies: none.

### Investigate and validate all 0.2 implementation prerequisites

Task: `0e36e096-4c0e-4192-971f-80ddc06c5596`.

Investigate upstream engines and exact packages, probe real WSL stdio browser behavior and capabilities, inspect Codex remote configuration and local evidence, audit module extraction and authority boundaries, check host/build/distribution prerequisites, and record a reproducible dossier and exact external gates. Product code changes are deferred during this task.

Acceptance: `A_PREFLIGHT`. Dependencies: `5c817e9d-cb3e-4825-9efe-14a5789e37d2`.

### Define module registry, manifests and 0.2 lifecycle contracts

Task: `70bb63a5-4137-4a4a-98bc-01752fdd3200`.

Define tools/integrations names, host selection, manifest support/capability/dependency contracts, lifecycle/state schemas and narrow composition interfaces. Implement fixtures for final validation.

Acceptance: `A_CONTRACTS`. Dependencies: none.

### Deliver independently installable browser tool

Task: `b8dc92c0-0e21-48d5-87b1-6832c41bc868`.

Create modules/browser with exact upstream dependencies, deterministic launchers, profiles, file/image handling, progress/diagnostics and artifact closure. Implement project-host and full-mode adapters.

Acceptance: `A_BROWSER`, `A_REMOTE`. Dependencies: `70bb63a5-4137-4a4a-98bc-01752fdd3200`.

### Management-only host install and selected tools lifecycle

Task: `b2a9d2ea-dec4-42fb-bbaf-1499202f30c8`.

Build Windows/Linux/macOS management-only distribution, execution-host resolution and verified selected-tool install/update/activate/disable/remove/status/doctor with operation recovery and declared prerequisites.

Acceptance: `A_HOST`. Dependencies: `70bb63a5-4137-4a4a-98bc-01752fdd3200`.

### Separate workspace and Git ownership with signing inside Git

Task: `2b48774f-44f0-402d-a238-45318d2d951a`.

Create coherent tool source boundaries and narrow path/repository/execution adapters. Signing is owned/configured by Git; retain protected agent processes internally. Handle file operations that currently call Git explicitly.

Acceptance: `A_GIT`. Dependencies: `70bb63a5-4137-4a4a-98bc-01752fdd3200`.

### Separate GitHub provider ownership and application secrets

Task: `6e379a63-8ff0-46cc-98ab-5f88d84bc101`.

Move provider App/token/config authority into GitHub; retain optional personal Projects authorization and actionable provider readiness. Give secrets its own application-secret contract and narrow delivery adapter.

Acceptance: `A_PROVIDERS`. Dependencies: `70bb63a5-4137-4a4a-98bc-01752fdd3200`.

### Modular execution, sharing and devtools coordination

Task: `57670ba8-22ab-475c-9279-89036c07e719`.

Refactor job/toolchain composition, application-secret integration, artifact/preview sources and external task coordination; retain real sandbox/network/endpoint ownership contracts and declared module dependencies.

Acceptance: `A_ADAPTERS`. Dependencies: `70bb63a5-4137-4a4a-98bc-01752fdd3200`.

### Compose tool-only and full MCP/runtime configurations

Task: `9b6b942f-1cad-4a94-b174-6b5177dd1c3b`.

Replace monolithic required initialization with selected module bindings and private prerequisites, actual capability discovery, disable gates, scoped state and duplicate-tool checks. Integrate identical upstream browser package into full runtime.

Acceptance: `A_COMPOSE`. Dependencies: `2b48774f-44f0-402d-a238-45318d2d951a`, `57670ba8-22ab-475c-9279-89036c07e719`, `6e379a63-8ff0-46cc-98ab-5f88d84bc101`, `b2a9d2ea-dec4-42fb-bbaf-1499202f30c8`, `b8dc92c0-0e21-48d5-87b1-6832c41bc868`.

### Write fresh 0.2 help, plugin and operator documentation

Task: `0394a664-a809-4db4-a081-4afecdc7364c`.

Write plural CLI/help and browser/full plugin definitions, host/capability matrices, progress/error guidance, ownership and install/removal examples. Final acceptance establishes their tested support claims.

Acceptance: `A_DOCS`. Dependencies: `9b6b942f-1cad-4a94-b174-6b5177dd1c3b`.

### Prepare 0.2.0 candidate artifacts and native CI

Task: `18627288-0fc2-43db-9061-3aa3b5ea6e52`.

Generate 0.2.0 candidate installers/manifests/checksums/notices and module artifact closures. Add Linux/Windows/macOS GitHub Actions and final acceptance fixtures. Product and release checks run in the final validation phase before separately authorized publication/cutover.

Acceptance: `A_CANDIDATE`. Dependencies: `0394a664-a809-4db4-a081-4afecdc7364c`.

### Final packaged browser and Windows-to-WSL validation

Task: `a5d98a76-9f06-4692-8d5c-ff65295414e3`.

After candidate preparation, validate packaged official engines on WSL and the actual Windows Codex desktop SSH workflow, supported/optional capabilities, protocol behavior, authority, files/images and cleanup. Preserve exact results and gaps.

Acceptance: `A_BROWSER`, `A_REMOTE`. Dependencies: `18627288-0fc2-43db-9061-3aa3b5ea6e52`.

### Final module, host, documentation and candidate validation

Task: `51f9fbbb-7f1c-406d-851a-6365420fb4df`.

After implementation and candidate preparation, run all required contract/module/architecture/lifecycle/authority/combination/full-runtime checks, native Linux/Windows/macOS GitHub Actions, documentation and exact candidate artifact acceptance. Fix failures, rebuild affected artifacts and rerun affected checks; retain explicit gaps.

Acceptance: `A_ACCEPTANCE`, `A_ADAPTERS`, `A_BROWSER`, `A_CANDIDATE`, `A_COMPOSE`, `A_CONTRACTS`, `A_DOCS`, `A_GIT`, `A_HOST`, `A_PROVIDERS`, `A_REMOTE`. Dependencies: `18627288-0fc2-43db-9061-3aa3b5ea6e52`, `a5d98a76-9f06-4692-8d5c-ff65295414e3`.

## Final validation procedures

After candidate preparation, execute all remaining procedures here against the final definition/code basis. Historical investigation results remain research inputs; refresh document coverage at final acceptance. Native Linux/Windows/macOS installation acceptance runs in GitHub Actions, with actual Windows desktop SSH validation a separate required path.

### 0.2 architecture and canonical workstream documents acceptance

Required validation: `8330f94e-2c33-405e-a0e5-3f4e8fbbea35`. Owner: `5c817e9d-cb3e-4825-9efe-14a5789e37d2`.

Compare accepted user decisions and actual source references against canonical docs; assert every requirement has acceptance, every acceptance has a task/validation, plan includes the complete task/validation sets, and workstream check is valid.

Acceptance: `A_DESIGN`.

### Implementation prerequisite evidence and feasible probes

Required validation: `03247f74-7757-4354-a8df-b8f2338a3441`. Owner: `0e36e096-4c0e-4192-971f-80ddc06c5596`.

Verify exact upstream metadata/lock pairing and official sources, run actual MCP protocol/browser fixture probes and inspect recorded catalogs and cleanup, verify source map and contract/architecture baseline plus portable compilation, validate dossier statements against raw evidence, and check complete workstream references. Real external product acceptance is recorded pending, never inferred from local execution.

Acceptance: `A_PREFLIGHT`.

### Final packaged browser and Windows-to-WSL validation

Required validation: `831212a4-b412-4f0b-b1e6-7c8cd26727fc`. Owner: `a5d98a76-9f06-4692-8d5c-ff65295414e3`.

After candidate preparation, validate packaged official engines on WSL and the actual Windows Codex desktop SSH workflow, supported/optional capabilities, protocol behavior, authority, files/images and cleanup. Preserve exact results and gaps.

Acceptance: `A_BROWSER`, `A_REMOTE`.

### Final module, host, documentation and candidate validation

Required validation: `129cca12-3df0-4553-afd8-bd735cad642e`. Owner: `51f9fbbb-7f1c-406d-851a-6365420fb4df`.

After implementation and candidate preparation, run all required contract/module/architecture/lifecycle/authority/combination/full-runtime checks, native Linux/Windows/macOS GitHub Actions, documentation and exact candidate artifact acceptance. Fix failures, rebuild affected artifacts and rerun affected checks; retain explicit gaps.

Acceptance: `A_ACCEPTANCE`.

### Final: Compose tool-only and full MCP/runtime configurations acceptance

Required validation: `e88b57b7-1ab4-49ce-af48-3766eaf99c11`. Owner: `51f9fbbb-7f1c-406d-851a-6365420fb4df`.

Run tool-only and representative combined/full MCP and runtime tests with missing core services, duplicate bindings, disabled cached calls, file/image results and credential/authority isolation.

Acceptance: `A_COMPOSE`.

### Final: Define module registry, manifests and 0.2 lifecycle contracts acceptance

Required validation: `39d266c7-ad09-4c90-ab09-06d649258ee1`. Owner: `51f9fbbb-7f1c-406d-851a-6365420fb4df`.

Run manifest/lifecycle/registry tests with duplicate IDs, unknown/cyclic dependencies, invalid schema, unsupported OS/runtime, disabled/unready state and public/private capability selection, plus focused architecture checks.

Acceptance: `A_CONTRACTS`.

### Final: Deliver independently installable browser tool acceptance

Required validation: `f1ecaaa8-4e2c-43b8-916f-e3c7d8b23a98`. Owner: `51f9fbbb-7f1c-406d-851a-6365420fb4df`.

Install the browser artifact into a fresh isolated user prefix with core Loki services absent; run engine smoke/capability tests, file/profile/concurrency/cleanup failures and both declared authority modes.

Acceptance: `A_BROWSER`, `A_REMOTE`.

### Final: Management-only host install and selected tools lifecycle acceptance

Required validation: `63e47976-cbc8-44c2-805d-8f65d29a916a`. Owner: `51f9fbbb-7f1c-406d-851a-6365420fb4df`.

Run lifecycle recovery/ownership tests, cross-platform builds and real native Windows/Linux/macOS management-only/tool-only install/remove acceptance, including unsupported targets and progress/error propagation. Execute native Linux/Windows/macOS jobs through GitHub Actions; cross-builds do not replace native acceptance.

Acceptance: `A_HOST`.

### Final: Modular execution, sharing and devtools coordination acceptance

Required validation: `cf7151da-3248-4de0-9654-176c1f73ea03`. Owner: `51f9fbbb-7f1c-406d-851a-6365420fb4df`.

Run real job sandbox/toolchain/network cleanup checks, publication endpoint ownership tests, optional secret delivery and external coordination fixture acceptance with absent unrelated modules.

Acceptance: `A_ADAPTERS`.

### Final: Prepare and verify 0.2.0 release candidate artifacts acceptance

Required validation: `fbea24a9-8950-426a-bbc0-97ca5ed9aa05`. Owner: `51f9fbbb-7f1c-406d-851a-6365420fb4df`.

Verify the prepared 0.2.0 candidate against final current-source evidence: exact artifact identities, dependency closures, package integrity/notices, manifests/checksums and management-only/tool-only/full installers. Rebuild after fixes and rerun affected checks.

Acceptance: `A_CANDIDATE`.

### Final: Publish fresh 0.2 help, plugin and operator documentation acceptance

Required validation: `76772a26-6cc6-4727-8043-2215367e83dd`. Owner: `51f9fbbb-7f1c-406d-851a-6365420fb4df`.

Validate fresh CLI help, plugin schemas/paths, executable examples against current artifacts, and support/capability matrix against recorded acceptance evidence.

Acceptance: `A_DOCS`.

### Final: Separate GitHub provider ownership and application secrets acceptance

Required validation: `3228d75f-6ab9-49df-be3b-49419517a121`. Owner: `51f9fbbb-7f1c-406d-851a-6365420fb4df`.

Run fake-provider/broker token-scope/redaction/permission tests, GitHub API without local Git and secret store/grant delivery isolation; no unrequested external mutations.

Acceptance: `A_PROVIDERS`.

### Final: Separate workspace and Git ownership with signing inside Git acceptance

Required validation: `b3ddc884-4ca7-476e-8173-14f2d500dc91`. Owner: `51f9fbbb-7f1c-406d-851a-6365420fb4df`.

Run scoped file/Git behavior tests, signing disabled/enabled signatures, protected key/agent tests and isolated execution acceptance; assert Git-owned signing and declared adapters through import/dependency checks.

Acceptance: `A_GIT`.

### Implementation tracking references

Task-local definitions retain coverage without intermediate required test gates. Execute their procedures through the required final acceptance records above.

- Tracking `0e7ab0e7-4a5e-4a7f-80ff-cd6a6b298121` → required final check `39d266c7-ad09-4c90-ab09-06d649258ee1`.
- Tracking `135c3b80-bd84-4515-b59e-7f0675c680eb` → required final check `cf7151da-3248-4de0-9654-176c1f73ea03`.
- Tracking `2935ce1b-940f-4431-a1b2-857b160bd5bd` → required final check `76772a26-6cc6-4727-8043-2215367e83dd`.
- Tracking `38c0f132-2e59-43de-977a-31ad09ad04f5` → required final check `e88b57b7-1ab4-49ce-af48-3766eaf99c11`.
- Tracking `3c6ea6bc-835d-409b-a093-3f4f0ca2c2aa` → required final check `f1ecaaa8-4e2c-43b8-916f-e3c7d8b23a98`.
- Tracking `44ee907d-7abf-456c-965d-ea74ef1fe3b9` → required final check `3228d75f-6ab9-49df-be3b-49419517a121`.
- Tracking `6a34d17d-4a41-48a1-a79b-97dd1ea96b74` → required final check `b3ddc884-4ca7-476e-8173-14f2d500dc91`.
- Tracking `842671be-2178-48bd-bd9f-8bee880547d9` → required final check `fbea24a9-8950-426a-bbc0-97ca5ed9aa05`.
- Tracking `96e920f3-f330-46ff-8421-bf752dc4b1f3` → required final check `63e47976-cbc8-44c2-805d-8f65d29a916a`.
