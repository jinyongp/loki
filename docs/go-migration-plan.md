# Go and devtools implementation plan

## Objective

Build the undeployed Go implementation around devtools while leaving the
running Python installation unchanged. The Go service defines a new MCP
contract. It does not preserve the Python tool catalog, state layout, or
migration commands.

The candidate runs and tests in the Ubuntu source environment or a disposable
Linux environment. Production deployment remains a separate operation.

## Architecture

agent shell -> devtools CLI -> tasks, workstreams, validation, public state
Loki Go MCP            -> workspace, Git, browser, artifacts, previews
Loki secret launcher   -> authenticated runtime broker -> devtools process

The bundled devtools skill teaches shell-capable agents to use the pinned CLI
directly. Loki does not mirror individual devtools commands as MCP tools. The
runtime broker owns the Loki AES-GCM secret vault and handles only configured
process launches that request selected encrypted secrets.

devtools owns tasks, workstreams, validation records, public variables,
configured processes, project inspection, instances, port allocation,
diagnostics, and backups. Loki retains these boundaries:

- encrypted secret storage and selected environment injection
- peer authorization and audit records
- secret response and error screening
- workspace path and executable confinement
- preview listener ownership checks
- Git signing key isolation
- browser and network isolation

## Secret boundary

Active secret values stay in Loki's existing versioned AES-256-GCM envelope.
The master key and encrypted state remain separate, root-owned files in a
private runtime directory. Only the runtime broker decrypts values. MCP tools
return names and metadata, and approved configured processes receive selected
values through their environment. Secret input uses a pipe and secret values
never appear in command arguments, devtools state, responses, or audit logs.

devtools may store public variables and secret metadata. Its plaintext active
secret store is not used. Encrypted devtools backups therefore do not replace
the Loki vault backup procedure.

## Devtools contract and skill

Pin the supported devtools version and generate an allowlisted runtime manifest
for the configured-process commands that accept encrypted Loki secrets.
Generation is an explicit source update, so a package upgrade cannot silently
change the privileged broker contract.

Bundle the pinned `devtools skill` as Loki's only built-in agent skill, with a
small Loki appendix that directs encrypted process launches through
`loki secret-process`. Agents discover individual command contracts with
`devtools schema COMMAND` and invoke ordinary commands from their shell.

The runtime adapter validates the small secret-launch command subset against
the pinned manifest and constructs an argument vector without a shell. It
applies request deadlines, cancellation, bounded output, stable JSON decoding,
and public error mapping. No general devtools command gateway is published by
MCP.

## Work items

### 1. Pin and inspect devtools

Add the supported version, captured schemas, manifest generator, and schema
drift tests. Add a minimal devtools.toml for Loki development after validating
the supported format.

Gate: generation is deterministic and rejects an unsupported binary version or
an unapproved command.

### 2. Replace bundled agent skills

Remove the existing bundled skills. Vendor the pinned devtools skill and make
the installer publish that one skill to agent environments.

Gate: the bundled directory contains exactly one valid `devtools/SKILL.md`, its
upstream body and Loki encrypted-secret guidance are pinned, and installation
exposes it to the agent.

### 3. Implement the secret process adapter

Add typed request validation, safe argument construction, subprocess limits,
JSON result decoding, and structured errors. Test with a fake executable and a
real isolated devtools profile.

Gate: success, invalid input, timeout, cancellation, oversized output, malformed
JSON, and version mismatch tests pass under the race detector.

### 4. Join the adapter to the encrypted runtime

Keep the current state store and secret controller. Add broker operations that
resolve secret names in the vault and inject values only into approved
configured devtools processes. Reuse output redaction and Unix peer checks.

Gate: the MCP identity cannot read the key, encrypted store, or raw values;
commands receive selected values; responses, logs, and process output do not
leak them.

### 5. Publish the reduced MCP surface

Retain focused Loki tools for workspace, Git, signing, browser, artifacts,
previews, encrypted secrets, audit, and system diagnostics.
Remove task, workstream, validation, variable, configured-process, and project
workflow tools that the devtools CLI owns.

Gate: tool discovery contains only retained Loki tools. The installed devtools
skill and its documented CLI flow succeed end to end.

### 6. Remove replaced implementation

Delete Loki-owned task, workstream, project workflow, variable, configured
process, action-policy, legacy migration, rollback, Python contract, and
Taskwarrior compatibility code after their devtools CLI paths pass. Preserve
the small generic subprocess primitive used by Loki's own service roles.

Gate: the Go binary builds without the removed packages and repository search
finds no live dependency on the Python contract or legacy migration paths.

### 7. Assemble services and packaging

Package the Go MCP and runtime broker with a pinned devtools dependency. Make
the MCP service require the runtime service and wait for its control socket.
Keep browser, port guard, signing, and network proxy roles separated by systemd
permissions.

Gate: a clean disposable install starts correctly after repeated boots and
service restarts, including delayed runtime socket creation. No Python package
or virtual environment is required by the candidate.

### 8. Integrated validation

Run formatting, vet, unit, race, broker permission, secret non-disclosure,
devtools integration, MCP end-to-end, browser, preview, Git signing, clean
installation, restart, and recovery checks against candidate-only state.

Gate: the same Go and devtools candidate artifact passes all required checks in
isolation and the running Python installation remains unchanged.

## Commit cadence

Each work item is split into compiling behavior commits. Code, tests, generated
schemas, and documentation for one behavior land together. Targeted tests run
before each commit; shared race and installation checks run at integration
boundaries. Unrelated working-tree changes remain unstaged.
