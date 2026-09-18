# Devtools protocol fixtures

`catalog-protocol-v3.json` is a source-reviewed consumer subset, not a captured
full release catalog. It contains only the existing `process start` and
`process restart` contracts. Unused command metadata and descriptions inside
schemas are omitted. The enforced input constraints, option order and
`item`/`changed`/`replayed` result shape are retained.

Review basis (2026-09-18): devtools `internal/cli/processes.go`,
`internal/cli/schema.go`, `internal/cli/cli.go` and
`internal/project/project.go`. The previously recorded source release was
v0.17.0; the inspected process and catalog source SHA-256 values were:

- processes.go: `ec8077fba6fd5d3072d816d5fb2b8354127788cba4f7d7d3588afd6d77aa9d1c`
- schema.go: `dda61bc4475e98cc7c0fc12f8b417c1ca73c8fc30304f9743a9a081b60c0cced`

CLI protocol 3, response envelope 1 and devtools task journal 2 are separate
contracts. This adapter checks CLI/transport versions and does not read or
write the task journal.

`catalog-v0.9.0.json` is retained only as a negative protocol-1 fixture. It is
not embedded as a runtime fallback.

Deterministic tests add unrelated text/artifact/passthrough commands without
output schemas and mutate the approved commands to exercise rejection paths.
They use temporary fake executables and synthetic data only. These results do
not establish compatibility with an installed or packaged devtools binary.
Before candidate acceptance, capture the selected executable's catalog with
`internal/devtools/cmd/gencatalog`, verify its selected contracts and run the
real process integration with its declared fixture. Keep that evidence
separate from this source-derived subset.
