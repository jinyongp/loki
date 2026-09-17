# Architecture dependency check

Run from the repository root:

```sh
go run ./tools/archcheck
```

The checker reads `policy.json`, evaluates each supported build variant with `go list`, and fails on unclassified packages, new in-module production dependency edges, nested `internal` visibility violations, selected third-party API ownership violations, restricted exported API leakage, stale baseline entries, or forbidden transitive dependencies of role entrypoints.

The checked-in `allowed_edges` list records reviewed current production edges. `edge_exceptions` are different: each is an existing structural violation with an exact source, target, reason, and architecture work unit that removes it. New violations are never added as wildcard legacy allowances. When a refactor removes or replaces an edge, update the policy in the same work unit; stale allowed edges and stale exceptions fail validation.

Tests and tooling are classified explicitly. Test imports still receive private-package and selected third-party ownership checks, but production-edge approval is tracked separately so a test helper does not become a runtime dependency by accident.

Role rules are allowed to name future optional entrypoints. Once an entrypoint exists, its complete in-module dependency closure is checked against forbidden authority tags. This is a source-architecture guardrail only; it does not prove runtime sandbox, mount, credential, process, or network isolation.
