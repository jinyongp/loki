# 0.2 native candidate preparation

`prepare_candidate.py --inputs REVIEWED-RECIPE --output FRESH-DIRECTORY`
coordinates one exact native management/tool candidate. The recipe identifies
`schema: 1`, `release: "0.2.0"`, its target, optional full `modules` keyed by
owner (`inputs` recipe path and public archive `url`), an owned `images` recipe,
and an optional `browser` entry (`inputs` and `url`). It prepares immutable
payloads, owned OCI images, module ZIPs and the complete catalog in order.
Browser full image archives are bound by actual OCI manifest/archive receipts.
Project-host candidates use the browser entry without full modules or images.
This preparation pipeline does not run product checks or publish resources.

Full candidate recipes declare `native_sources` for Git, GitHub CLI,
coordination, browser libraries and the public trust store. Preparation acquires
the pinned Go source archives and authenticates Ubuntu package indices on the
native runner, then builds each closure before binding it to module payloads
and owned images. Both Linux architectures have source recipes.

Run the manual `reviewed-browser-inputs.yml` workflow to acquire the five
project-browser targets without executing their programs. Its artifact is
`loki-tools-reviewed-project-browser-inputs`. The manual
`reviewed-full-inputs.yml` workflow retains the full source recipes as
`loki-tools-reviewed-full-input-recipes`; each subsequent native runner acquires
and authenticates its own package closure. Pass the successful input run ID and
artifact name to `native-tools.yml`, with `phase=prepare` and the matching
mode. These workflows upload temporary candidate inputs and artifacts to
Actions; they do not publish a release or change an installed deployment.

Release-owned full payload recipes for runtime-core, execution, secrets and
sharing are under `packaging/tools/inputs`. They require no vendor input archives;
Go programs and source assets come from the exact repository source. Git,
GitHub, coordination and workspace require reviewed native vendor program
closures, including dynamic libraries and notices where applicable.
`acquire_full_inputs.py --trust REVIEWED-RECIPE --output FRESH-DIRECTORY`
downloads those archives against independent HTTPS/version/length/SHA-256
receipts and emits `full-inputs.json`. Image construction separately needs its
exact base/BuildKit/frontend digests and the browser native-library closure.
Metadata generated from arbitrary acquired bytes is insufficient as a trust root.

`build_go_vendor.py --inputs REVIEWED-SOURCE --output FRESH-DIRECTORY`
prepares GitHub CLI or devtools from its finite pinned source commit on the
native Linux target. The recipe binds the source archive's primary HTTPS URL,
version, commit, length, hash, root and source notices. Pinned Go 1.27.1 builds
the declared program with read-only modules; Go and dependency notices are
retained from that exact linked graph. It emits a vendor archive and receipt
for the full module producer. Source archive hashes measured over HTTPS remain
distinct from independently authenticated release signatures. No product API
checks or publication run during this preparation.

`build_debian_closure.py --inputs REVIEWED-PACKAGES --output FRESH-DIRECTORY`
prepares the exact native Ubuntu 24.04 Git/OpenSSH, browser library/font or public
trust-store closure. Its recipe declares `owner`, exact target, distribution,
public closure URL and packages with local archive/name/version/primary HTTPS
URL/length/SHA-256 plus independent signed-index provenance. It reads package
data only; package installation and maintainer scripts never run. It retains
copyright notices, rejects conflicting files and resolves package aliases only
inside the completed closure. Git/OpenSSH wrappers use their module's vendor
program/library paths. Public CA roots are concatenated from the reviewed
certificate package. Package dependency completeness and actual native ABI
execution remain final acceptance checks.

`acquire_debian_inputs.py --trust REVIEWED-PACKAGE-SELECTION --output
FRESH-DIRECTORY` authenticates Ubuntu noble/main/universe indices in private APT
state using the administrator-owned Ubuntu archive keyring. It resolves against
an empty installed-package status, downloads each dependency against its signed
index length/SHA-256 and retains InRelease/keyring receipts. Host APT sources,
package state and installed programs are preserved. Selection recipes for both
Linux architectures are under `packaging/tools/inputs`; actual resolved versions
are frozen in the emitted closure recipe. A keyring with untrusted ownership
blocks this acquisition rather than weakening signature verification.

The disposable GitHub Ubuntu runner currently exposes the archive keyring as
root-owned mode 0777. Full preparation verifies its bytes against the retained
administrator-owned Ubuntu keyring SHA-256 before restoring mode 0644. The
producer still requires administrator ownership and denies writable keyrings;
the CI repair does not apply to installed user environments.

Exact GitHub CLI and devtools commit/source receipts are retained in the source
input recipes. Workspace recipes use the official ripgrep release asset
SHA-256/length for Linux amd64 and arm64. These receipts prepare inputs; actual
compiled closures, complete notices and native behavior remain final checks.

All owned full images additionally require a reviewed `trust_store` archive:
exact native target, primary HTTPS URL/version, independent length/SHA-256,
archive/root, `certificates` path and retained `notices`. The certificate file
contains only public PEM certificate blocks and is installed at the standard
system CA bundle path. Provider/API and toolchain HTTPS requests use this public
image input. Host certificate directories and private keys are not copied.

## Full payloads and owned images

Full Linux assembly starts with `build_full_bundle.py --payload-only`. Its
native input recipe has an empty `images` object; the result contains immutable
native files and a complete file-integrity receipt, without an install catalog.
Core owns its execution contract, egress policy and pinned browser seccomp
profile. Git owns its default configuration/templates; execution owns the
toolchain catalog and workspace owns bundled skills. Input recipes cannot
replace those release-owned files.

`build_full_images.py --inputs REVIEWED-RECIPE --output FRESH-DIRECTORY`
consumes prepared `runtime-core`, `execution` and Git payload directories.
The recipe identifies the exact Linux/full target, a digest-pinned native
`base`, `base_notices`, pinned `buildkit` and Dockerfile `frontend` images,
public `repository`, a `payloads` map and the required
`roles`: `service`, `gateway`, `workload`, `git-workload`, `browser`.
Each role copies only its declared private program closure. Git's workload
adds Git-owned programs/configuration; the browser image adds a separately
reviewed native library/font/trust/notice closure through `browser_libraries`.
That closure declares the exact target, local archive/root, primary HTTPS URL,
version, independently trusted length/SHA-256 and retained notice paths.

Image preparation uses networkless build instructions and exports native OCI
archives. Receipts record the image manifest digest, archive integrity,
prerequisite file receipts and explicit unpublished/unaccepted state. It does
not load images into an existing Loki installation or publish a registry.
It uses the local Unix Docker endpoint, a private empty client configuration
and a uniquely named Buildx builder, removed after preparation. Existing
Docker contexts, registry credentials and selected builders are preserved.
`finalize_full_bundle.py --prepared DIRECTORY [--images IMAGES-JSON]
--output FRESH-DIRECTORY --release-url HTTPS-URL` checks the owned OCI archive,
its actual manifest digest and equality with the module's prepared-file receipt.
It binds those exact images to the module ZIP/catalog without rebuilding vendor
or Go programs. Modules with no owned images omit `--images`. The final archive
retains the prepared file receipt and complete image input receipts for closure
validation. Linux amd64 candidates have been constructed locally. Native CI
preparation and final runtime acceptance remain separate required steps.

## Management-only native bundles

After preparing module artifacts, combine their local catalogs with
`merge_catalogs.py --catalog PATH [--catalog PATH ...] --output DIRECTORY`.
The merger checks each local ZIP against its length/hash/catalog manifest and
requires the complete exact-target prerequisite closure. Merge project-host
and full candidates into separate catalogs: their browser prerequisite contracts
are scoped to their runtime mode. Prepared metadata remains distinct from native
acceptance and publication.

The merged output includes `archives/<sha256>.zip` for offline candidate
installation. `loki tools install --catalog ABSOLUTE_CATALOG --archives
ABSOLUTE_ARCHIVES TOOL...` uses the same trusted receipt and extraction checks
without requiring publication of the candidate's acquisition URL. This source
path supports final isolated native acceptance; it is not an accepted pass.

`prepare_plugin.py` materializes browser-only or selected-full plugin sources
with explicit local/WSL/SSH connection arguments. See the fresh
[usage documentation](../../../docs/tools/usage.md). It writes portable manifests
and a synchronized Codex loader overlay; it does not install or upload a plugin.

`build_manager_bundle.py --output DIRECTORY` builds the portable manager with
pinned Go 1.27.1 on its actual native host. It produces a normalized ZIP and
binary/archive integrity receipt, includes Go and dependency notices from the
exact linked dependency graph, and declares an empty included-tools collection.
No Node, Chrome, appliance, provider credentials, or product tools are installed
by the management bundle.

After extracting the native bundle, run `sh install.sh [BIN_DIRECTORY]
[MANAGEMENT_ROOT]` on Linux/macOS, or `install.ps1 -BinDirectory PATH
[-ManagementRoot PATH]` on Windows. Defaults use the user's local command
directory and the separate 0.2 state namespace. The Windows installer changes
the user PATH only when `-AddToUserPath` is supplied. CLI publication is also
available as `loki install --bin-dir ABSOLUTE_PATH`. Existing commands must have
matching ownership metadata; an unrelated command requires another directory.

Publication persists its intended binary location and ownership before replacing
the command. `loki tools recover` from the extracted bundle repairs interrupted
publication without replacing commands that differ from both recorded binaries.
`tools prune TOOL --keep COUNT` removes only verified inactive program
generations. Current/leased programs, profiles, results, credentials and unknown
directories are retained. Its trash journal permits interrupted deletion to
resume even after some inner ownership markers have been deleted.

The manual `native-manager.yml` workflow has `prepare` and `validate` phases
on Linux, Windows and macOS runners. Preparation only builds artifacts. Validation
starts after all native candidate jobs complete, consumes those exact artifacts,
and runs lifecycle/contract fixtures and `accept_manager_bundle.py` in isolated
temporary namespaces. Run the validation phase after implementation is complete.
This workflow does not certify official engine packages, full-runtime authority,
desktop SSH integration, architecture, signing/notarization, or the remaining
required workstream checks. Producers, installers and acceptance fixtures are
source awaiting final acceptance; no new native support pass is claimed.

The manual `native-tools.yml` workflow consumes independently reviewed
input receipts/archives from an explicitly selected artifact of this repository.
It selects the runner's actual native recipe, prepares all candidates first and
then runs checks only for the `validate` phase. Project-host validation installs
the exact offline candidate into temporary state, observes both official
engines, transfers an owned file/resource/image, rejects a disabled cached call
and preserves the synthetic workspace across removal. Full mode prepares a
Linux candidate and runs native source/manager fixtures; its running sandbox,
provider API and service authority acceptance remain separate final gates.
Actual Windows desktop SSH rendering remains a separate gate for either path.

## Browser bundles

Acquire source inputs from an independently trusted native recipe before
assembly:

```sh
python3 scripts/maintainer/tools/acquire_browser_inputs.py \
  --trust /absolute/path/to/reviewed-vendor-receipts.json \
  --output /absolute/path/to/fresh-input-directory
```

The recipe binds exact native target, upstream version/location, byte length,
SHA-256, archive layout and notices. Acquisition keeps those trust inputs
independent of downloaded bytes, rejects changed content, and emits progress.
It produces `browser-inputs.json` for the assembler. Use `--mode full` for a
native Linux protected-service artifact; its catalog declares `runtime-core`
as a private prerequisite and must be combined with that exact full closure.
Native Windows arm64 is outside the pinned Chrome contract. Native acquisition
and assembly do not establish accepted platform support.

`build_browser_bundle.py` is source for the candidate preparation step. It has
not yet produced an accepted 0.2 candidate. Run it on each native release runner
after independently acquiring the pinned vendor archives and recording their
verified URLs, checksums and exact byte lengths in a trusted input recipe.

```sh
python3 scripts/maintainer/tools/build_browser_bundle.py \
  --inputs /absolute/path/to/trusted-inputs.json \
  --output /absolute/path/to/fresh-candidate-directory \
  --release-url https://example.com/releases/loki-browser-0.2.0-linux-amd64-project-host.zip
```

The recipe has `schema: 1`, a native `target` with `os`, `arch`, and
`mode: "project-host"`, and `assets` containing `node`, `chrome`, and `ffmpeg`.
Each asset declares `version`, `archive` (relative to the recipe directory or
absolute), `url`, `bytes`, `sha256`, `root` (inside the vendor archive), and
`notices` (nonempty paths within that root). Node additionally declares its
`executable` and `npm` JavaScript entrypoint. Chrome declares its `executable`.
`native_requirements` records the target's reviewed host library requirements;
assembly does not install host libraries.

Pinned inputs are Node 26.10.0, Chrome for Testing 155.0.8059.39, FFmpeg revision
1011, and the repository's npm lockfile. Platform acquisition recipes retain
primary-source vendor receipts. Availability and successful
assembly remain distinct from native product support.

The five native source recipes are under `packaging/tools/inputs`. Node's hash
comes from the official SHASUMS256 document; Chrome's pinned archive bytes/hash
come from the independently retained prerequisite dossier; FFmpeg's archive
receipt was captured from its pinned primary HTTPS endpoint. Their retained
`digest_provenance` distinguishes published checksums from measured archive
digests. Detached signature, complete notices and native product acceptance
remain explicit final checks. `--target OS/ARCH` permits acquisition of another
target's archives without executing them; assembly requires the actual native
host.

The producer retains full vendor and dependency trees, including their
notices, materializes dependency links into regular artifact files, rejects
escaping links, and uses bundled Node/npm with `npm ci --ignore-scripts`.
It emits a ZIP, `module.json`, `runtime.json`, upstream receipts, and a
release-bound browser catalog. A fresh output directory prevents accidental
candidate replacement. The ZIP normalizes ordering, timestamps and file modes.

On macOS, the producer preserves Chrome's relative framework links within
each app and checks the pinned upstream signature kind before and after copying:
unsigned Intel code, or linker ad-hoc arm64 code without a resource seal.
Native codesign verifies arm64 code hashes using `--strict --ignore-resources`.
Source receipts and generation integrity bind resources. The producer preserves
upstream signatures. The native macOS browser ZIP installer
permits only those app-contained Chrome links, creates them after all regular
files, and rejects dangling/cyclic/escaping links and archive writes through
aliases. Other modules and platforms retain regular-file/directory extraction.
Installed code integrity and sandbox startup require native final acceptance.

The browser doctor starts the receipt-bound Chrome through bundled Playwright,
with its sandbox enabled, checks the running version and closes the owned
processes. It uses a temporary profile rather than a project's session data.
This also diagnoses Windows GUI binaries that do not print `--version` to a
console. Both Playwright sessions and doctor explicitly enable the sandbox;
host library or sandbox errors are reported without silently weakening startup.

The browser manifest declares the local `loki_browser_files` helper; engine
tools retain dynamic official discovery. The helper transfers files from owned
per-project/per-engine/per-session directories and uses MCP resources or inline
images for reads. Upload staging returns an execution-host path for official
file-upload calls. Transfers are bounded to 4 MiB. Sharing is optional and is
not needed for this session-local file transfer.

Run `test_bundle_inputs.py`, archive/install checks, actual engine probes, native
library diagnostics, and license completeness checks during final validation.
The input recipe itself is a trust boundary; checksums embedded in downloaded
archives do not authenticate those archives.

`accept_full_workspace_candidate.py --candidate DIRECTORY` verifies the native
manager and core OCI receipt, loads the core image into the local Docker cache,
and starts an isolated full workspace deployment. It checks private stdio
discovery, actual create/read, cached invocation rejection after disable, and
owned shutdown. Failure preserves the private root for recovery. It certifies
workspace transport only; execution, Git, browser, sharing and endpoint flows
have separate acceptance requirements. The full native validation workflow runs
this check after preparation without publishing images or a release.

`publish.yml` publishes a stable release using three successful
same-source native validation runs. `prepare_release.py` verifies original
candidate lengths/digests and complete six-manager, five-project-browser and
two-Linux-full coverage. Native catalogs stay separate by mode and target.
The publisher preserves and verifies OCI manifest digests in GHCR, uploads
original ZIPs, native catalogs, acceptance records and `SHA256SUMS`, then
publishes the latest release. Other full-module and desktop SSH checks retain
their actual validation status; publication does not imply those checks passed.
