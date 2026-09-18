# Devtools protocol fixtures

`catalog-protocol-v3.json` is a source-reviewed consumer subset, not a captured
full release catalog. It contains the existing `process start` / `process restart` contracts plus the
non-executing `project inspect`, `command list`, and `command inspect` metadata
contracts used by Loki's typed project adapter. Metadata output schemas in this
bootstrap fixture are intentionally broad objects; the typed adapter performs a
second strict decode and path sanitization. The fixture also contains the
read-only task/workstream/run context queries admitted by Loki. It also carries
the narrow claim/takeover/resume/checkpoint/release/done mutation contracts used
by the session-bound coordination adapter. Claim credentials are stripped from
public projections and kept only in protected session state; task definition
edits, validation mutations, doctor execution and runtime lifecycle mutations
remain excluded. Runtime verification replaces this fixture with the selected
binary's actual catalog before any call.

Review basis (2026-09-19): devtools commit
`7ef0deb215ec7deca4c4c67248e3d18683c2e875`, including
`internal/cli/processes.go`, `internal/cli/commands.go`, `internal/cli/schema.go`,
`internal/cli/cli.go`, `internal/cli/tasks.go`, `internal/cli/task_schema.go`,
`internal/tasks/context.go`, and `internal/project/project.go`. The latest tagged
source basis remains v0.17.0; task compaction is reviewed from the commit above.
The inspected source SHA-256 values include:

- processes.go: `ec8077fba6fd5d3072d816d5fb2b8354127788cba4f7d7d3588afd6d77aa9d1c`
- schema.go: `dda61bc4475e98cc7c0fc12f8b417c1ca73c8fc30304f9743a9a081b60c0cced`
- commands.go: `25dc5b8fba6dfd2c44e09f8c4cef45af2f80ee9d126eadfb160be86130c85772`
- cli.go: `5374c8e7b16273c695b70bc4afadacf4c213f32ec492b775df94453573df4460`
- tasks.go: `6690bf56a306d0fffc95130737ae304c00b81bff17bc07bdcccc836f8166e0f6`
- task_schema.go: `a25c6c1ab4e7192f96d619c94289157ab5ffd6c0063f8daa5d890c6d8c50ee48`
- tasks/context.go: `1249d3d887ead1386f5a5f13e5891ec05b14208ab82dab1d85f3644ca056a0f2`

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
