# Loki 0.2.x implementation plan

## Completed investigation

The canonical architecture and prerequisite investigation are recorded in [spec.md](spec.md) and [preflight.md](preflight.md). Preserve their recorded evidence and explicit external gaps as implementation inputs.

## Implementation delivery

1. Define module manifests/registry, capability and support contracts, private prerequisites, lifecycle states, composition interfaces and fresh 0.2 CLI/config/state schemas. Specify artifact identity, atomic ownership/recovery and execution-host selection.
2. Build `modules/browser` as an independently installable tool using exact official Playwright/DevTools MCP dependencies, deterministic launchers, engine profiles, native prerequisites, owned file/image staging and actionable diagnostics.
3. Build management-only Windows/Linux/macOS bootstraps and selected tools lifecycle, with portable management entrypoints, declared prerequisites, host selection and interruption recovery.
4. Extract workspace and Git ownership and narrow resource/execution adapters. Git owns signing configuration and its protected internal agent services.
5. Extract GitHub provider credentials/API and application secrets into separate owners. Keep Personal Projects optional and provide disabled organization Projects guidance with explicit administration authorization.
6. Extract execution, sharing and external coordination adapters with declared sandbox, endpoint, secret and CLI dependencies. Pin the supported real devtools protocol/catalog.
7. Compose tool-only, combined and full MCP/runtime configurations from selected enabled capabilities and private prerequisites. Use the same official browser implementation in standalone and full operation.
8. Write fresh plural CLI help, plugin definitions, support/capability matrices, setup diagnostics and installation/removal documentation.
9. Generate the 0.2.0 candidate installers, module artifacts, manifests/checksums, notices and dependency closures. Add native Linux/Windows/macOS GitHub Actions jobs and acceptance fixtures for the final validation phase.

Implementation tasks deliver code, fixtures and documentation. Their required product checks are owned by the final validation tasks so implementation can proceed in dependency order. Delivery is distinct from verified acceptance.

On 2026-10-04 the user approved aligning the linked implementation task
descriptions with this delivery boundary. Source delivery completion retains all
acceptance references and final required validation definitions. It does not
claim successful packaged execution or accepted native support.

## Final validation

Start this phase after implementation and candidate preparation. Run all required procedures here:

1. Check canonical design, prerequisite evidence and complete devtools requirement/acceptance/task/validation references against the final source and definitions.
2. Run manifest, registry, lifecycle, invalid-input, cycle, support, enablement and architecture/dependency checks.
3. Exercise the packaged official browser engines: protocol/discovery changes, optional capabilities, cancellation, roots, files/images, unsafe capability gates, profiles, concurrency and cleanup. Test standalone with core services absent and full-mode authority separately.
4. Verify the real Windows Codex desktop -> SSH -> WSL workflow, development-server reachability and usable screenshot/file results.
5. Run native management-only and selected-tool install/update/activate/disable/remove/status/doctor acceptance on Linux, Windows and macOS through GitHub Actions, including interruption recovery, ownership, unsupported targets and error/progress propagation. Cross-compilation is supplementary evidence.
6. Test scoped workspace/Git operations, signing on/off, protected keys/agents, provider API without local Git, token permissions/redaction and separate secret storage/delivery grants.
7. Exercise real job sandbox/toolchain/network cleanup, sharing endpoint ownership, optional secret delivery and coordination against the pinned real devtools binary.
8. Run representative combinations and full-runtime protocol/lifecycle/authority acceptance, duplicate-binding checks, disabled cached-call rejection and credential isolation.
9. Validate CLI help, plugin schemas/paths, executable documentation examples and support matrices against accepted artifacts.
10. Verify the exact 0.2.0 candidate identities, dependency closures, upstream integrity/notices, manifests/checksums and management-only/tool-only/full installers. Rebuild changed artifacts after fixes and rerun affected checks before accepting them.

Record current-definition/code-basis evidence. Unavailable environments remain explicit completion gaps. Historical research passes do not certify the final product. All required validation definitions remain closure gates.

## Release and checkpoints

After final acceptance, publication and cutover to existing user installations are separately authorized operations. Maintain repository documents and linked devtools definitions together using revision-guarded dry-run/atomic edits and reference readback. Claim source work and checkpoint decisions, remaining work, next action and public evidence paths without execution credentials. Report implementation delivery, verified acceptance and live rollout separately.
