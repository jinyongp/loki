# Release tooling

This directory is the canonical home for release-engineering generators.

Release manifest, provenance, notices, and candidate-evidence semantics live under
`internal/host/releases`. Host installation and update semantics live under
`internal/host/lifecycle`. Release tools assemble accepted bytes; they do not
create a second lifecycle implementation.

## Bootstrap builder

`bootstrapbuild` builds the standalone `loki-bootstrap` binary used for
source-checkout-free first installation.

The builder:

- requires one exact immutable Git release tag;
- embeds the exact release manifest for that tag;
- builds `cmd/bootstrap` reproducibly with `-trimpath` and an empty Go build ID;
- writes through a temporary output and publishes the executable atomically;
- does not become an install-host dependency.

Example:

```sh
go run ./tools/release/bootstrapbuild \
  --output /absolute/output/loki-bootstrap \
  --release-tag v1.2.3 \
  --release-manifest /absolute/release/release-manifest.json
```

At runtime that bootstrap downloads only
`https://github.com/jinyongp/loki/releases/download/<tag>/loki-linux-amd64`
and verifies the host binary length and SHA-256 from its embedded manifest before
executing it.

## Release candidate evidence builder

`evidencebuild` assembles the immutable input directory used by A14 release
acceptance. It accepts the release index/manifest and exact released files,
executes the accepted bootstrap's read-only `--bootstrap-info` surface, and
requires the bootstrap's embedded tag, manifest SHA-256, and host-binary SHA-256
to match the candidate inputs.

The bundle contains:

- `evidence.json`, with a content-derived candidate evidence ID;
- `SHA256SUMS`, covering the evidence record and every copied input;
- `inputs/`, containing the exact files consumed by later clean-host,
  adversarial, recovery, and independent-review acceptance.

Digest-pinned OCI references, release/config/policy/toolchain identities,
provenance, notices, and release notes remain part of candidate evidence. The
output directory must not already exist; failed assembly never publishes a
partial bundle.

A typical invocation is:

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

A14 records the resulting evidence ID and does not substitute source-tree
binaries or mutable image tags after assembly.

## Publication preparation

`publishprep` converts one already-verified A14 candidate into the exact public
publication set. It does not build the release or choose a version.

It verifies the candidate again, requires its source revision to equal the
publication commit, requires the Git tag to be `v<release-version>`, rechecks
release-index/manifest binding, and executes `loki-bootstrap --bootstrap-info`
to prove that the bootstrap is bound to that same tag, release-manifest digest,
and host-binary digest.

Accepted bytes are copied into stable public names including:

- `loki-linux-amd64`;
- `loki-bootstrap-linux-amd64`;
- `loki-host-assets.tar.gz`;
- `loki-release-manifest.json`;
- `loki-release-index.json`;
- `loki-toolchain-catalog.json`;
- `loki-provenance.bundle.json`;
- `loki-notices.tar.gz`;
- `loki-release-notes.md`.

Public artifacts retain the `loki-` product namespace even though repository
entrypoints use local role names such as `cmd/bootstrap`.

The same preparation renders `install.sh` from `install.sh.tmpl`. The script
contains one exact immutable Git tag and the exact SHA-256 of
`loki-bootstrap-linux-amd64`; it never resolves a mutable `latest` bootstrap.
It downloads that bootstrap over HTTPS, verifies the digest, and forwards the
caller's arguments. Lifecycle behavior remains in the release-bound bootstrap
and `loki host`.

## GitHub publication

`.github/workflows/release.yml` is intentionally `workflow_call`-only. The
A14 caller first uploads the accepted candidate as a workflow artifact. The
publication workflow then:

1. recreates the public asset set with `publishprep`;
2. publishes those exact assets as an immutable GitHub Release through
   `releaseway/actions`, pinned to a full commit SHA;
3. archives the rendered installer as `loki-install.sh`;
4. only after the GitHub Release succeeds, deploys the same installer bytes to
   the Loki GitHub Pages project site.

When the user-site custom domain is `jinyongp.dev`, the deployed installer path
is `https://jinyongp.dev/loki/install.sh`.

Pages must be configured for GitHub Actions before the first live publication.
The workflow has no tag-push or manual-dispatch trigger that can bypass the A14
caller.
