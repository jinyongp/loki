# Devtools protocol fixtures

`catalog-protocol-v3.json` is the source-reviewed Loki consumer subset, not a
captured full release catalog. It contains the 21 commands admitted by Loki:
`process start` / `process restart`, typed project metadata
(`project inspect`, `command list`, `command inspect`), and the
read-only/mutation task coordination commands used by Loki's typed adapters.
Task definition edits, validation mutations, doctor execution, backup/cleanup,
secret management, generic command execution, and runtime lifecycle mutations
remain excluded.

The reviewed release basis is devtools v0.19.9 at commit
`ce9e198872a2de7754efe3db1ead166653746402` (reviewed 2026-09-28).
The v0.19.9 task CLI retains the same Loki-approved protocol-3 executable
contract fingerprint as the prior v0.18.0 baseline while upstream implementation
and non-approved surfaces continue to evolve. Loki does not expose those forms: generic
`Client.Call` rejects coordination queries and mutations, while
`QueryCoordination` and `MutateCoordination` construct their accepted inputs
from typed Loki requests. The reviewed baseline nevertheless fingerprints the
exact approved upstream command/options/input/output contracts so future
upstream drift still requires an explicit review before release. Command aliases
are not execution authority in this adapter and are not part of the fingerprint.

Agent Skill/guidance ownership and semantic handoff/compaction remain Loki-native.
Claim credentials are stripped from public projections and retained only in
protected session state. Runtime verification reads the selected binary's actual
catalog before any call and requires the approved contract fingerprint to match
this reviewed baseline.

Reviewed v0.19.9 source includes `internal/cli/processes.go`,
`internal/cli/schema.go`, `internal/cli/commands.go`,
`internal/cli/cli.go`, `internal/cli/tasks.go`,
`internal/cli/task_schema.go`, `internal/tasks/commands.go`,
`internal/tasks/model.go`, `internal/tasks/query.go`, and
`internal/project/project.go`. Inspected source SHA-256 values include:

- processes.go: `01230d33db5a262efb84f94b60729d1dc26c04435c4da5ec47254774c9e71092`
- schema.go: `dda61bc4475e98cc7c0fc12f8b417c1ca73c8fc30304f9743a9a081b60c0cced`
- commands.go: `9bc9be4042e7ab29e82ef07193f1ee7629f379e3407b18d407ceda49e7efe47b`
- cli.go: `976cf6013b6fc8d8c3cfc73b282f1fbde3c57591e746432b1e6c9fc05e385e73`
- tasks.go: `dbb1c20e9c44dc4b0669447ac479e717702679b8ed67bd0dbb12f69739cc4f68`
- task_schema.go: `75580b45df9fbfe58e45f47020dbc4fc42f0dd8a39fa95379975522bb8bfdb1e`
- tasks/commands.go: `aebc76e3b5b818e486cc210a173ab8592c6b0d9e8edb508d307ffefe8f4bb2df`
- tasks/model.go: `ed5f274930c8a3f4b0939f690d7b8b24e764ce64a7de604f0831cf7900cd1ae5`
- tasks/query.go: `65f6445ab880b775f8f392d5d578c4fb671778ac9373fcc95eb9fb00bf7c6316`
- project.go: `d1623d81ea4bbd39e67df71ea2e85beeba51cd3ed257c4dda58b91fe5cfe69ca`

An exact v0.19.9 build verified through Loki's runtime `Client.Verify()` reports
protocol 3, 21 approved commands, and catalog fingerprint
`f9cd7292b95ea7538ec20004a2da4ac7a5d6886897a2d04cf464ef236cb8b708`,
which matches the embedded reviewed subset.

CLI protocol 3, response envelope 1, and the devtools task journal are separate
contracts. This adapter checks the CLI/transport contract and does not read or
write the devtools task journal directly.

`catalog-v0.9.0.json` is retained only as a negative protocol-1 fixture. It is
not embedded as a runtime fallback.

Deterministic tests add unrelated text/artifact/passthrough commands without
output schemas and mutate approved executable contracts to exercise rejection
paths. Candidate verification permits command-level editorial description
changes but rejects option/schema drift. Release preflight also binds the
release-input devtools version and commit to this reviewed baseline, and source
gates verify the actual built pinned binary before race tests or immutable image
construction.
