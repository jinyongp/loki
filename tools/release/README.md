# Release tooling

This directory is the canonical home for release-engineering generators.

Runtime trust and release semantics are implemented under `internal/host/releases`.
Host installation and update semantics are implemented under `internal/host/lifecycle`.
Tools in this directory may assemble or build release artifacts, but they must not duplicate those policies.

## Bootstrap builder

`bootstrapbuild` builds the standalone `loki-bootstrap` binary used for source-checkout-free first-install acceptance.

The builder:

- validates the configured HTTPS metadata repository and initial TUF root before embedding them;
- builds `cmd/bootstrap` reproducibly with `-trimpath` and an empty Go build ID;
- writes through a temporary output and publishes the finished executable atomically;
- does not become a dependency of the install host.

See `docs/first-install.md` for the pre-release install procedure.

Public one-line installer publication remains gated by A14 acceptance.

## Release candidate evidence builder

`evidencebuild` assembles the immutable input directory used by A14 release acceptance.

It accepts an A13 release index/manifest, the exact released targets, and a complete signed TUF repository directory produced by the configured signing system. Before copying anything it executes the accepted bootstrap's read-only `--bootstrap-info` surface, requires the embedded metadata URL and trusted-root digest to match the supplied release trust inputs, replays the signed repository through Loki's production `go-tuf/v2` client, verifies every manifest-bound release/toolchain target, converts the repository into deterministic `tuf-repository.tar.gz`, requires digest-pinned core/browser OCI references, records source/config/policy/toolchain identities, and publishes:

- `evidence.json`, with a content-derived candidate evidence ID;
- `SHA256SUMS`, covering the evidence record and every copied input;
- `inputs/`, containing the exact files consumed by later clean-host, adversarial, recovery, and independent-review acceptance, including `tuf-repository.tar.gz`.

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
  --tuf-repository /absolute/signed/tuf-repository \
  --trusted-root /absolute/signed/root.json \
  --metadata-url https://jinyongp.dev/loki/tuf/ \
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


## Publication preparation

`publishprep` converts one already-verified A14 candidate evidence bundle into
the exact public publication set. It does not build or sign a release.

The tool verifies the candidate bundle again, requires its source revision to
match the publication commit, requires the Git tag to be `v<release-version>`,
and rechecks the authenticated release-index/manifest binding before copying
accepted bytes into stable external names such as:

- `loki-linux-amd64`;
- `loki-bootstrap-linux-amd64`;
- `loki-tuf-repository.tar.gz`;
- `loki-host-assets.tar.gz`;
- `loki-release-manifest.json`;
- `loki-release-index.json`;
- `loki-provenance.bundle.json`;
- `loki-notices.tar.gz`;
- `loki-release-notes.md`.

Public artifacts keep the `loki-` product namespace even though their source
entrypoints use repository-local role names such as `cmd/bootstrap`.

Before publication, `publishprep` executes the accepted bootstrap's
`--bootstrap-info` surface, requires its metadata URL to be
`https://jinyongp.dev/loki/tuf/`, extracts the evidence-bound TUF repository
with traversal/symlink protections, locates the exact embedded root by SHA-256,
and re-verifies all required targets through the production TUF client. The
verified repository is then included under `pages/tuf/`.

The same preparation renders `install.sh` from `install.sh.tmpl`. The
rendered script contains one exact immutable Git tag and the exact SHA-256 of
`loki-bootstrap-linux-amd64`; it never resolves a mutable `latest` bootstrap
at install time. The installer only detects the supported platform, downloads
that bootstrap over HTTPS, verifies the embedded digest, and executes it with
the caller's arguments. Lifecycle behavior remains in the authenticated
bootstrap and `loki host`.

## GitHub publication

`.github/workflows/release.yml` is intentionally `workflow_call`-only. The
A14 caller must first upload the accepted candidate bundle as a workflow
artifact. The publication workflow then:

1. recreates public assets with `publishprep`;
2. publishes the exact asset set as an immutable GitHub Release through
   `releaseway/actions`, pinned to a full commit SHA;
3. archives the release-bound installer as `loki-install.sh` and the exact
   accepted TUF repository as `loki-tuf-repository.tar.gz`;
4. only after the GitHub Release succeeds, deploys those same installer bytes
   plus the verified TUF repository under `tuf/` to the Loki GitHub Pages
   project site.

When the user-site custom domain is `jinyongp.dev`, the Loki project Pages
path is `https://jinyongp.dev/loki/`, so the deployed file becomes
`https://jinyongp.dev/loki/install.sh`.

Pages must be configured to use GitHub Actions before the first live
publication. Live GitHub Release and Pages publication remain part of the A14
gate; this workflow has no tag-push or manual-dispatch trigger that can bypass
that caller.
