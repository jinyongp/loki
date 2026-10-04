# 0.2 prerequisite probes

These are Linux x64 maintainer probes against pinned upstream servers, not the Loki 0.2 installer or product adapter. They use a synthetic loopback fixture and owned temporary browser profiles. Run from interactive WSL bash with Node 22.22.2 and the repository's Go toolchain. They do not configure the desktop app or use a logged-in browser.

## Prepare isolated artifacts

The tested scratch root is `/tmp/loki-tools-preflight`. To reproduce under another root, replace that path consistently and pass `--root` to each Python probe. Do not put dependencies or browser binaries in this source directory.

```sh
mkdir -p /tmp/loki-tools-preflight
cp scripts/maintainer/preflight/package.json scripts/maintainer/preflight/package-lock.json /tmp/loki-tools-preflight/
npm ci --prefix /tmp/loki-tools-preflight --ignore-scripts --no-audit --no-fund --cache /tmp/loki-tools-npm-cache
curl --fail --location --output /tmp/loki-tools-preflight/chrome.zip https://storage.googleapis.com/chrome-for-testing-public/154.0.8037.92/linux64/chrome-linux64.zip
printf '%s\n' 'ff43322f335e436b2f4dcdfeeec5db032299e335a7e8c1c618b326e100ce8732  /tmp/loki-tools-preflight/chrome.zip' | sha256sum --check
unzip -q /tmp/loki-tools-preflight/chrome.zip -d /tmp/loki-tools-preflight
chmod +x /tmp/loki-tools-preflight/chrome-linux64/chrome /tmp/loki-tools-preflight/chrome-linux64/chrome_crashpad_handler
```

The recorded Chrome digest is a measurement bound to this dossier, not a vendor signature. Download only the exact vendor URL above into this owned scratch directory. Product acquisition needs its own reviewed provenance/integrity contract.

Check native dependencies:

```sh
ldd /tmp/loki-tools-preflight/chrome-linux64/chrome
```

This Ubuntu 24.04 host lacked NSPR/NSS/ALSA. The probes use temporary libraries without changing installed system packages:

```sh
mkdir -p /tmp/loki-tools-preflight/native-deps/root
cd /tmp/loki-tools-preflight/native-deps
apt download libnspr4=2:4.35-1.1build1 libnss3=2:3.98-1ubuntu0.2 libasound2t64=1.2.11-1ubuntu0.3
for package in ./*.deb; do dpkg-deb --extract "$package" root; done
cd /home/jinyongp/workspace/jinyongp/loki
PLAYWRIGHT_BROWSERS_PATH=/tmp/loki-tools-preflight/pw-browsers node /tmp/loki-tools-preflight/node_modules/playwright/cli.js install ffmpeg
```

APT must have trusted Ubuntu package metadata for the exact versions. Hashes are in `docs/tools/evidence/metadata.json`. On another distribution, use its documented native prerequisites; these extracted Ubuntu libraries are not a portable product solution. The fixture subprocess receives this scratch library path and browser cache explicitly. It inherits a minimal PATH/display/locale environment, not provider tokens, and uses isolated XDG directories without changing HOME or CODEX_HOME.

## Run upstream checks

```sh
python3 scripts/maintainer/preflight/probe.py --engine playwright --all-caps --fixture
python3 scripts/maintainer/preflight/probe.py --engine devtools --all-caps --fixture
python3 scripts/maintainer/preflight/probe.py --engine devtools --catalog-only
python3 scripts/maintainer/preflight/protocol_probe.py
python3 scripts/maintainer/preflight/optional_probe.py
```

Each engine writes `<engine>-<profile>-report.json` under the scratch root; optional/protocol probes write their own reports. Fixture probes return nonzero for a failed check, exception or invalid protocol stdout. Expected native invalid-call errors count as passes. The protocol probe records cancellation observations rather than treating a missing canceled response as failure. The optional probe adds the Chrome WebMCP feature flag and exercises an unpacked extension, installable PWA and page-provided tools. Experimental DevTools UI has a separate flag and no passing full-profile claim.

Fresh per-run subdirectories prevent old outputs from satisfying file checks. Screenshots are represented by decoded size/hash in JSON, with original artifacts confined to the scratch tree. Heap snapshots, browser caches, videos and node_modules are not checked into the repository. Owned subprocess groups are closed on completion; existing user browsers/services are not stopped.

## Baseline and existing boundaries

```sh
go test -json ./...
go test -race ./internal/tools
go run ./tools/archcheck
LOKI_TEST_CHROME=/tmp/loki-tools-preflight/chrome-linux64/chrome LOKI_TEST_CHROME_LIBS=/tmp/loki-tools-preflight/native-deps/root/usr/lib/x86_64-linux-gnu LOKI_REQUIRE_BROWSER_TESTS=1 go test ./internal/integrations/browser ./internal/integrations/browser/internal/cdp -run '^TestChromium' -count=1
LOKI_DEVTOOLS_BINARY="$(command -v devtools)" go test ./internal/devtools -run '^TestRealProcessInheritsBrokerSecrets$' -count=1
```

The final command currently fails with protocol 6; keep that failure as an extraction requirement. A mock-based green baseline does not override it.

When a local Docker daemon is available, use the existing source-fixture acceptance script rather than an old installed Loki image:

```sh
scripts/verify/accept-oci-jobs.sh
```

It builds a pinned, temporary source fixture via a loopback registry, tests six cases and removes its owned registry/image resources. It may acquire pinned Docker base images. Do not point it at an installed appliance container. The dossier records one exploratory old-image gateway failure caused by an unsupported CLI flag; all six checks subsequently passed with the current-source fixture.

Pure contract cross-compilation:

```sh
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o /tmp/loki-tools-preflight/tools-windows-amd64.exe ./internal/tools
GOOS=windows GOARCH=arm64 CGO_ENABLED=0 go test -c -o /tmp/loki-tools-preflight/tools-windows-arm64.exe ./internal/tools
GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go test -c -o /tmp/loki-tools-preflight/tools-darwin-amd64 ./internal/tools
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go test -c -o /tmp/loki-tools-preflight/tools-darwin-arm64 ./internal/tools
```

These cross-built binaries are not executed here. Use the external runbook for real desktop/OS/product checks.

Additional baseline skips were exercised with current-source static test binaries inside an isolated root container. Build them under scratch storage:

```sh
mkdir -p /tmp/loki-tools-preflight/privileged-tests
CGO_ENABLED=0 go test -c -o /tmp/loki-tools-preflight/privileged-tests/execution.test ./internal/execution
CGO_ENABLED=0 go test -c -o /tmp/loki-tools-preflight/privileged-tests/signing.test ./internal/integrations/signing
CGO_ENABLED=0 go test -c -o /tmp/loki-tools-preflight/privileged-tests/github.test ./internal/integrations/github
docker run --rm --name loki-tools-preflight-privileged --network none --read-only --user 0:0 --cap-drop ALL --cap-add CHOWN --cap-add SETUID --cap-add SETGID --cap-add DAC_OVERRIDE --tmpfs /tmp:rw,nosuid,nodev,mode=1777 --tmpfs /var/tmp/loki/github:rw,nosuid,nodev,mode=2750,gid=10001 -v /tmp/loki-tools-preflight/privileged-tests:/fixtures:ro --entrypoint /bin/sh loki@sha256:f4379c91c587a357192795c6be89f11f5094f7a383757a23c8e8748a862905db -c '/fixtures/execution.test -test.v -test.run "^TestLinuxRunnerCanWriteStateButCannotReadVaultKey$" && /fixtures/signing.test -test.v -test.run "^TestAgentSocketRecoveryPreservesOccupiedPaths/wrong-owner$" && LOKI_GITHUB_COMMAND_ACCEPTANCE=1 /fixtures/github.test -test.v -test.run "^TestRealDelegatedGitHubCLIConfiguration$"'
```

This image contains gh 2.100.0. It is an installed immutable 0.1 runtime fixture, not a candidate deployment. Docker is only a maintainer requirement for these primitive checks, not a standalone browser dependency. No host credentials, user workspace, Docker socket or network are exposed to this temporary container.

Existing project E2E also passed with actual devtools/Node/pnpm, Playwright test 1.63.0 and an owned Chrome wrapper. The wrapper is needed because the test intentionally constructs a minimal child environment:

```sh
cat > /tmp/loki-tools-preflight/chrome-with-libs.sh <<'SH'
#!/bin/sh
LD_LIBRARY_PATH=/tmp/loki-tools-preflight/native-deps/root/usr/lib/x86_64-linux-gnu
export LD_LIBRARY_PATH
exec /tmp/loki-tools-preflight/chrome-linux64/chrome "$@"
SH
chmod 0755 /tmp/loki-tools-preflight/chrome-with-libs.sh
LOKI_E2E_DEVTOOLS="$(readlink -f "$(command -v devtools)")" LOKI_E2E_PNPM="$(readlink -f "$(command -v pnpm)")" LOKI_E2E_NODE="$(readlink -f "$(command -v node)")" LOKI_E2E_CHROMIUM=/tmp/loki-tools-preflight/chrome-with-libs.sh go test ./internal/e2e -run '^TestProjectExecutionContract$' -count=1 -timeout=5m
```

That existing E2E fixture disables the browser sandbox and invokes devtools directly. Its pass is separate from the upstream sandbox-enabled checks and does not fix the Loki adapter's protocol rejection. Source fixture runs verify frozen/offline dependency installation, concurrent projects and managed process/secret cleanup. Published-release transaction and OCI archive checks still need the exact release artifacts.

Check recorded evidence without downloads or global state changes:

```sh
python3 scripts/maintainer/preflight/verify_evidence.py
```
