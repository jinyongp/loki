# Release tooling

This directory is the canonical home for release-engineering generators.

Release manifest, provenance, notices, and candidate-evidence semantics live under
`internal/host/releases`. Host installation and update semantics live under
`internal/host/lifecycle`. Release tools assemble accepted bytes; they do not
create a second lifecycle implementation.


## Release version

`version` resolves the next stable `vMAJOR.MINOR.PATCH` tag from repository
history. With no existing release tag, patch and minor start at `v0.1.0`; major
starts at `v1.0.0`. The GitHub release workflow uses patch by default and lets
the operator select minor or major when dispatching a release.

## Release assembler

`releasebuild` creates the canonical release input set from one source commit
and the accepted digest-pinned core/browser OCI images. It builds the Linux
amd64 host binary and release-bound bootstrap, renders the immutable release
manifest/index, materializes the embedded host assets, compiles the effective
policy/config evidence, and creates provenance, notices, toolchain-catalog and
release-note targets. The notice bundle includes the digest-pinned WSL base
identity plus the exact locked Ubuntu/Docker package delta that the appliance
builder must reproduce. The output is atomically published as one directory and
then consumed by `evidencebuild`.

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
- `loki-wsl-amd64.wsl`;
- `loki-release-manifest.json`;
- `loki-release-index.json`;
- `loki-toolchain-catalog.json`;
- `loki-provenance.bundle.json`;
- `loki-notices.tar.gz`;
- `loki-release-notes.md`.

Public artifacts retain the `loki-` product namespace even though repository
entrypoints use local role names such as `cmd/bootstrap`.

The same preparation renders both installer frontends. `install.sh` is
rendered from `install.sh.tmpl` with one exact immutable Git tag and the exact
SHA-256 of `loki-bootstrap-linux-amd64`. `install.ps1` is rendered from
`install.ps1.tmpl` with that same release tag plus the accepted
`loki-wsl-amd64.wsl` length and SHA-256. Neither frontend resolves a mutable
`latest` artifact. The shell installer delegates lifecycle behavior to the
release-bound bootstrap; the Windows installer verifies and registers the
accepted WSL appliance, whose first-boot service delegates Loki lifecycle
behavior to `loki host`.

## GitHub publication

`.github/workflows/release.yml` is the end-to-end release entry point. Run it
manually on `main` and choose the semantic-version increment. From that point
the workflow performs the release without a second operator handoff:

1. verifies that the selected source is the current remote `main`;
2. runs `actions-up@1.21.0` in report mode and refuses stale GitHub Action
   pins;
3. resolves the next release version;
4. builds required external CLI inputs from exact upstream source commits on native amd64 and arm64 GitHub runners in parallel;
5. builds and pushes the core and browser OCI images and records their immutable
   digests;
6. assembles release metadata/bootstrap, builds the release-bound WSL appliance,
   and binds it into candidate evidence;
7. runs deterministic Go gates plus real OCI, Compose/browser, bootstrap,
   devtools and toolchain acceptance;
8. creates the release Git tag only after acceptance succeeds;
9. publishes the exact accepted assets with `releaseway/actions`, pinned to the latest stable release's full commit SHA;
10. deploys the release-bound `install.sh` and `install.ps1` through GitHub
    Pages; and
11. compares both public installers to their archived release assets and runs
    the Linux source-free installation smoke test. Windows WSL boot acceptance
    is a separate exact-candidate release gate.

Every cross-repository GitHub Action remains pinned to a full commit SHA. The
adjacent version comment is maintained by `actions-up`.

The deployed installer paths are
`https://jinyongp.dev/loki/install.sh` and
`https://jinyongp.dev/loki/install.ps1`. GitHub Pages must use GitHub Actions
as its source. GHCR container packages must also be public for anonymous
source-free installation; GitHub currently exposes package visibility as a
package setting rather than a supported visibility-mutation REST endpoint, so
the workflow verifies anonymous pullability before creating a release.
