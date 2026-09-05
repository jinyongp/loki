# Loki MCP

Loki is a workspace-scoped MCP server and isolated execution environment for
repository automation. It provides consolidated workspace, Git, browser,
artifact, Agent Skill, project workflow, and secret-backed action interfaces.

## Source and runtime separation

This repository is the canonical source tree. A deployed Loki instance keeps
installed code and machine state outside the repository:

- `/opt/loki-mcp`: installed Python environment and bundled assets
- `/etc/loki`: machine configuration, authentication material, and public keys
- `/var/lib/loki`: encrypted runtime, project, browser, and signing state
- `/run/loki`: Unix sockets and other ephemeral runtime files
- `/srv/workspace/loki`: repositories exposed to the agent

Never commit files copied from `/etc/loki` or `/var/lib/loki`. In particular,
Cloudflare tunnel tokens, MCP bearer tokens, signing private keys, secret store
keys, and imported dotenv files belong only to the target machine.

## Development

Loki requires Python 3.12 or newer.

```sh
python3 -m venv .venv
. .venv/bin/activate
python -m pip install -e '.[test]'
pytest -q
```

## Deployment

Deployment targets a dedicated WSL/Linux environment. The installer preserves
existing `/etc/loki/config.toml` and runtime state. Review the example in
`config/loki-mcp.toml`, configure the target machine under `/etc/loki`, and run
the integrated verifier as root:

```sh
scripts/verify-and-deploy-loki.sh "$PWD"
```

Public artifact and preview hosting are disabled in the repository defaults.
They become active only when the target machine supplies its own host and
Cloudflare Access configuration.

The MCP service waits for the signing and runtime Unix sockets before starting.
Each startup attempt waits up to 30 seconds and retries after 3 seconds on
failure, including a readiness timeout. Slow socket creation therefore recovers
automatically. The installer includes this policy in `loki-mcp.service`.

Run the isolated startup regression tests as root in a development distro with
systemd: `python3 -m unittest discover -s tests -p test_mcp_startup.py -v`.
These tests use temporary units and sockets, with shorter retry timeouts.

## Repository visibility

No license is granted by this repository. Keep the GitHub repository private
unless a distribution license has been selected and the bundled Agent Skills
have been reviewed for redistribution.
