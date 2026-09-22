# First install

This document is the canonical first-install procedure for the A13/A14 release-validation phase.

The public one-line installer is **not published or advertised yet**. Release acceptance must pass before the reserved installer frontend is documented for general use.

## Inputs

A first-install validation fixture provides:

- a `loki-bootstrap` binary built for the target host;
- an embedded initial TUF root and authenticated metadata repository URL in that binary;
- authenticated release metadata and immutable release targets reachable from that repository;
- the operator-selected absolute workspace path.

The target host does not need a Loki source checkout or a local Go, Node.js, Python, Rust, or other development compiler/toolchain.

## Supported validation hosts

A13 validates:

- Ubuntu 24.04 amd64;
- WSL2 running Ubuntu 24.04 amd64.

Other hosts are not part of the A13 first-install acceptance row.

## User-scoped install

Use the exact bootstrap artifact supplied by the release-validation fixture:

```sh
chmod 0755 ./loki-bootstrap
./loki-bootstrap --workspace /absolute/workspace
```

The bootstrap authenticates release metadata, selects the matching release, verifies the release manifest and host binary, stages them in private temporary storage, and invokes `loki host install`.

To validate a specific release instead of the current release selected by authenticated metadata:

```sh
./loki-bootstrap \
  --bootstrap-release 1.2.3 \
  --workspace /absolute/workspace
```

## System-scoped install

System scope uses the same bootstrap and authenticated release flow:

```sh
sudo ./loki-bootstrap \
  --system \
  --workspace /absolute/workspace
```

The scope changes host-management ownership and paths. It does not change MCP authorization, project execution authority, or the sandbox contract.

## What the bootstrap does not do

The bootstrap does not:

- contain install, update, rollback, backup, restore, or optional-component lifecycle logic;
- configure a specific MCP client;
- trust mutable image tags or unchecked release URLs;
- accept a workspace-provided trust root;
- silently broaden workspace permissions;
- require a source checkout or development toolchain.

Lifecycle mutation remains owned by `loki host`.

## Release engineering

The canonical bootstrap builder lives at `tools/release/bootstrapbuild`. It is a release-engineering tool, not an install-host dependency. It embeds only the authenticated metadata URL and initial trusted TUF root into the standalone bootstrap binary.

Example release-engineering invocation:

```sh
go run ./tools/release/bootstrapbuild \
  --output /absolute/output/loki-bootstrap \
  --metadata-url https://release-metadata.example.invalid/loki/ \
  --trusted-root /absolute/path/to/root.json
```

The placeholder metadata URL above is intentionally not a public Loki install endpoint.

## Public installer gate

The stable shell frontend reserved by the distribution plan remains unavailable until A14 release acceptance passes with no blocking findings or required skips. Until that gate closes, documentation must not present a public one-line shell install command.
