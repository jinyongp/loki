# Retained Python deployment

This directory contains the previous Python Loki server and everything used only
to install, operate, test, or recover that deployment. It is retained until the
Go replacement has been deployed and its cutover and recovery checks have passed.
Moving these sources does not deploy either implementation, restart a service,
or change installed files, credentials, or workspace data.

Current development and ordinary self-hosting use the Go implementation at the
[repository root](../../README.md). Do not add new product features here. Limit
changes to maintenance required for the retained deployment and eventual removal.

## Contents and ownership

The complete retirement unit is `legacy/python/`:

| Path | Retained purpose |
| --- | --- |
| `pyproject.toml`, `src/loki_mcp/` | Python MCP server, runtime, CLI support, policy, and state handling |
| `browser_sidecar/` | Separate Python browser package and its package metadata |
| `tests/` | Python regression tests and the synthetic `reference/python_reference.py` helper |
| `scripts/` | Python deployment, backup/recovery, host launchers, maintenance commands, and live verification |
| `scripts/verify-preview-server.mjs` | Fixture used only by the legacy browser/preview checks |
| `systemd/` | Units and timers for the old Python deployment, including Cloudflare integration |
| `config/loki-mcp.toml` | Python deployment configuration template |

Two shared inputs deliberately remain outside this directory: repository-root
`bundled_skills/` and `config/loki-gitconfig`. Both implementations use them.
The legacy installer reads these inputs from the repository root rather than
copying them into a second source tree. There are no compatibility symlinks or
forwarding launchers at the retired root paths.

Go-side migration and compatibility support is not part of the retirement unit.
In particular, keep `cmd/loki/migrate_vault*`, `internal/secret/migration*`,
`internal/state/` and its synthetic `testdata/python-v1.json`, and
`internal/config/testdata/python-v047.toml`. These fixtures do not import or run
the archived server. The Go systemd candidate under `packaging/native/`, current
`scripts/`, and Go/Compose configuration also remain current assets.

## Local validation

Run these commands from the repository root. Python 3.12 or newer is required.
The layout check uses only the standard library and does not run an installer:

```sh
python3 legacy/python/tests/test_layout.py -v
python3 legacy/python/tests/test_backup_recovery.py -v
```

For the full Python suite, use a separate virtual environment. The root is no
longer a Python package; install the explicit legacy package instead:

```sh
python3 -m venv .venv
.venv/bin/python -m pip install './legacy/python[test]'
.venv/bin/python -m pytest -q legacy/python/tests
```

The browser-debug unit tests locate the adjacent `browser_sidecar/src` directly.
Running the actual browser service additionally requires the sidecar package and
its dependencies; unit validation does not start Chromium. The systemd startup
tests require root in a disposable systemd environment and skip otherwise. Do not
run them against an operational host merely to validate this relocation.

Both Python packages can still be built independently:

```sh
.venv/bin/python -m pip wheel --no-deps --wheel-dir .tmp/legacy-wheels ./legacy/python
.venv/bin/python -m pip wheel --no-deps --wheel-dir .tmp/legacy-wheels ./legacy/python/browser_sidecar
```

## Existing-host maintenance only

The following entrypoints are privileged deployment operations, not validation
commands. Use them only during explicitly approved maintenance of the existing
Python installation. They keep their installed `/opt/loki-mcp`, `/opt/loki-browser`,
`/etc/loki`, `/var/lib/loki`, and `loki-*.service` paths and names.

The source argument is the **whole repository root**, not `legacy/python`:

```sh
sudo ./legacy/python/scripts/install-loki-mcp.sh /absolute/loki-checkout
sudo ./legacy/python/scripts/verify-and-deploy-loki.sh /absolute/loki-checkout
sudo ./legacy/python/scripts/install-cloudflared.sh /absolute/loki-checkout
```

When the argument is omitted, these scripts locate the repository root relative
to their own path, independently of the current directory. Keep the full checkout
available because the installer also needs the shared inputs described above.
Do not invoke the old root `scripts/install-loki-mcp.sh` or install the repository
root with pip. Update any operator-maintained job that still uses those source
entrypoints before the next Python maintenance operation.

The legacy backup entrypoint is now
`scripts/backup-python-deployment.sh` relative to this directory. It briefly stops
services and accesses protected deployment state; it must not be run as a source
layout check. Its recovery tests substitute all host-mutating commands.

## Removal after Go cutover

Retain this entire directory while the Python deployment or Python rollback path
is still needed. A built candidate, passing unit tests, or a published release is
not sufficient evidence to delete it.

Before removal, record that the Go replacement is actually serving the intended
MCP endpoint, required workspace and command workflows pass, and any enabled
browser, signing, GitHub, or external endpoint integrations work. Verify restart
behavior and the migration/recovery path using protected backups. Confirm that no
service, scheduled job, or maintenance procedure still requires the Python
packages or these source entrypoints. The operator must accept the cutover and
retire the Python recovery path explicitly.

Once those conditions are satisfied:

1. Delete `legacy/python/` as one source-tree change, including this README and
   its Python-only tests and fixtures.
2. Remove its navigation and retention references from the root README and
   `docs/installation-distribution-plan.md`.
3. Run the Go tests and release acceptance again. Current Go packaging must not
   need the deleted directory; `internal/packaging/source_boundary_test.go`
   guards that separation and remains after removal.

Do not delete shared skills, Git configuration, Go compatibility fixtures, or
Go migration support as part of this cleanup. Retiring installed Python services,
virtual environments, host configuration, and credentials is a separate approved
host operation, not a side effect of deleting repository files. Never remove the
workspace, secrets, or protected backups as part of source cleanup.
