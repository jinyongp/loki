# Release tooling

This directory is the canonical home for release-engineering generators.

Runtime trust and release semantics are implemented under `internal/host/releases`.
Host installation and update semantics are implemented under `internal/host/lifecycle`.
Tools in this directory may assemble or build release artifacts, but they must not duplicate those policies.

## Bootstrap builder

`bootstrapbuild` builds the standalone `loki-bootstrap` binary used for source-checkout-free first-install acceptance.

The builder:

- validates the configured HTTPS metadata repository and initial TUF root before embedding them;
- builds `cmd/loki-bootstrap` reproducibly with `-trimpath` and an empty Go build ID;
- writes through a temporary output and publishes the finished executable atomically;
- does not become a dependency of the install host.

See `docs/first-install.md` for the pre-release install procedure.

Public one-line installer publication remains gated by A14 acceptance.
