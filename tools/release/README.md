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

## Release candidate evidence builder

`evidencebuild` assembles the immutable input directory used by A14 release acceptance.

It accepts an A13 release index/manifest and the exact released targets, verifies every manifest-bound file before copying it, requires digest-pinned core/browser OCI references, records source/config/policy/toolchain identities, and publishes:

- `evidence.json`, with a content-derived candidate evidence ID;
- `SHA256SUMS`, covering the evidence record and every copied input;
- `inputs/`, containing the exact files consumed by later clean-host, adversarial, recovery, and independent-review acceptance.

The output directory must not already exist. Failed assembly does not publish a partial bundle.

A typical release-engineering invocation supplies absolute paths:

```sh
go run ./tools/release/evidencebuild \
  --output /absolute/output/a14-candidate \
  --source-revision COMMIT_SHA \
  --release-index /absolute/release/index.json \
  --release-manifest /absolute/release/manifest.json \
  --host-binary /absolute/release/loki \
  --bootstrap /absolute/release/loki-bootstrap \
  --host-assets /absolute/release/host-assets.tar.gz \
  --toolchain-catalog /absolute/release/toolchain-catalog.json \
  --provenance /absolute/release/provenance.bundle.json \
  --notices /absolute/release/notices.tar.gz \
  --release-notes /absolute/release/release-notes.md \
  --effective-policy /absolute/release/effective-policy.json \
  --effective-config /absolute/release/effective-config.toml \
  --core-image registry.example/loki@sha256:... \
  --browser-image registry.example/loki-browser@sha256:...
```

A14 acceptance records the resulting `evidence.json` ID. It does not substitute source-tree binaries or mutable image tags after the bundle is assembled.
