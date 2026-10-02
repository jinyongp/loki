# Connect an MCP client

Loki exposes one local Streamable HTTP MCP origin. The endpoint is loopback-only
by default and uses bearer-token authentication.

The important distinction is not which MCP client you use. It is whether that
client can reach the machine where Loki is listening.

```text
client can reach Loki host              client runs elsewhere
        |                                       |
        | direct Streamable HTTP                | tunnel / VPN / proxy
        v                                       v
http://127.0.0.1:<port>/mcp              Loki local origin
```

Loki deliberately owns only the local origin. It does not automatically create a
public endpoint, DNS name, TLS certificate, tunnel, VPN route, reverse proxy, or
provider account.

## Read the connection information

### Windows

Use the installed Windows frontend:

```powershell
loki connection
loki connection list --json
loki connection show local
```

The bare command lists the supported local and managed connection types without
starting or downloading a managed provider helper. `loki connection show local`
refreshes the protected Windows replica from the live appliance before reporting
its endpoint details. Per-distribution local-origin state remains at:

```text
%LOCALAPPDATA%\Loki\<distribution-name>\
├── connection.json
├── mcp-token
└── ownership.json
```

The MCP URL is in `local_origin.url`. The token itself remains in
`mcp-token`; do not commit it, paste it into issue reports, or put it in a URL.

`loki connection list` is the provider catalog and persisted-state view. Use
`loki connection show NAME` for details and `loki connection setup PROVIDER`
for managed-provider configuration.

### Linux

Use:

```sh
loki host connection
```

The command prints the local origin and the token-file path without printing the
token value.

## Direct connection

Use a direct connection when the MCP client can already reach the Loki host's
loopback endpoint. This normally means the client runs on the same machine, or
has a local companion process that makes that origin reachable.

Configure:

- server URL: `local_origin.url`;
- transport: Streamable HTTP;
- authentication: `Authorization: Bearer <token>`, using the generated token
  file.

No public DNS, TLS certificate, or externally reachable endpoint is required.

## Remote or hosted MCP clients

A remote or hosted MCP client cannot use `127.0.0.1` on your developer machine
as if it were its own network endpoint. Keep Loki on loopback and add an ingress
layer between the client and Loki.

Common choices are:

- **client-specific tunnel or local agent** — usually the simplest option when
  the MCP client vendor provides one;
- **private VPN** — useful when both sides can join the same private network.
  [Tailscale Serve](https://tailscale.com/docs/features/tailscale-serve) is one
  example;
- **SSH or application tunnel** — useful for operator-controlled machines and
  temporary access. See the
  [OpenSSH port-forwarding options](https://man.openbsd.org/ssh);
- **reverse proxy** — useful when you intentionally operate a stable remote MCP
  endpoint. See the
  [Caddy reverse-proxy quick start](https://caddyserver.com/docs/quick-starts/reverse-proxy)
  for one implementation example;
- **managed tunnel service** — useful when inbound networking cannot be opened
  directly. Examples include
  [Cloudflare Tunnel](https://developers.cloudflare.com/tunnel/get-started/)
  and [Tailscale Funnel](https://tailscale.com/docs/features/tailscale-funnel).

Whichever option you use, the ingress must forward Streamable HTTP requests to
Loki and preserve or deliberately re-establish Loki's bearer authentication.

Prefer private ingress unless public exposure is explicitly required. Do not
treat changing Loki from `127.0.0.1` to `0.0.0.0` as a substitute for an
access-control design.

## Provider-specific examples

Provider integrations belong here as examples of the general remote-client
model, not as part of Loki's core connection contract.

### OpenAI products / ChatGPT

For OpenAI products that support Secure MCP Tunnel, Loki can manage the reviewed
Windows `tunnel-client` helper while keeping Loki itself on loopback:

```text
OpenAI client
    |
    | OpenAI Secure MCP Tunnel
    v
release-bound tunnel-client
    |
    | http://127.0.0.1:<port>/mcp
    v
Loki
```

The user still owns the OpenAI-side tunnel and runtime credential. Loki does not
create or delete remote tunnels and does not store an OpenAI admin key.

1. Create or inspect the tunnel in
   [OpenAI Platform Tunnels](https://platform.openai.com/settings/organization/tunnels).
2. Create or select a runtime API key under
   [OpenAI organization API keys](https://platform.openai.com/settings/organization/api-keys).
3. Run:

   ```powershell
   loki connection setup openai
   ```

   The interactive setup prints the official OpenAI reference links, asks for
   the existing tunnel ID, and reads the runtime key without echo. The key is
   stored in Windows Credential Manager. Loki installs only the exact
   same-release helper mirror from its reviewed helper catalog; it never resolves
   an upstream `latest` helper at runtime.
4. Inspect the managed connection:

   ```powershell
   loki connection show openai
   ```

5. For ChatGPT, configure/enable the MCP app using the current
   [developer mode and MCP apps guide](https://help.openai.com/en/articles/12584461-developer-mode-and-mcp-apps-in-chatgpt),
   then scan the tools and test the connection.

For non-interactive setup, provide the name of an environment variable rather
than putting the runtime key in process arguments:

```powershell
$env:OPENAI_TUNNEL_RUNTIME_KEY = "<runtime-key>"
loki connection setup --tunnel-id "<existing-tunnel-id>" --runtime-key-env OPENAI_TUNNEL_RUNTIME_KEY openai
Remove-Item Env:OPENAI_TUNNEL_RUNTIME_KEY
```

Local lifecycle commands are:

```powershell
loki connection start openai
loki connection show openai
loki connection stop openai
loki connection remove openai
```

`remove` deletes only Loki-owned local runtime metadata and the Loki-scoped
runtime credential. It does not delete the remote OpenAI tunnel.

Loki isolates native tunnel-client state and profiles under
`%LOCALAPPDATA%\Programs\Loki\connections\<distribution>\openai` and
injects both the runtime key and Loki bearer authorization only into the helper
process environment. Neither secret is stored in adapter JSON, Scheduled Task
arguments, helper ownership state, or process arguments.

When a managed remote connection is enabled, Loki maintains one finite
`Loki Connections (<distribution>)` logon task for the distribution. The task
invokes the verified absolute `loki.exe`, waits boundedly for the appliance,
and restores only enabled owned adapters. OpenAI setup and startup also wait for
the local MCP listener to become reachable from Windows, with at most 30 checks
two seconds apart. This covers a delay in WSL localhost forwarding after the
appliance's internal services become healthy.

If startup fails, Task Scheduler retries it up to three times at one-minute
intervals. Loki upgrades the retry settings of a verified existing connection
task during connection reconciliation. Update apply, rollback, and restore
reconnect enabled adapters even when the local URL and token stay unchanged.
Disabled adapters remain disabled.

OpenAI's UI and permissions can change independently of Loki. Prefer the linked
OpenAI settings pages and official guides for account-side steps.

Other MCP client vendors may provide their own tunnel, desktop bridge, gateway,
or hosted connector. Prefer that vendor's supported mechanism when it preserves
the Loki authentication boundary cleanly.

## Troubleshooting

If a client cannot connect:

1. Run `loki host doctor` on Linux or the system-scoped equivalent inside the
   Windows appliance.
2. Confirm the URL in `connection.json` matches the running Loki port.
3. Decide whether the client is local/direct or remote/hosted.
4. For a direct client, confirm it can reach the Loki loopback URL.
5. For a remote client, confirm the chosen tunnel, VPN, proxy, or gateway can
   reach the Loki loopback URL from the Loki host.
6. Confirm MCP requests arrive with the Loki bearer token.
7. If using a provider-specific connector, verify that connector independently
   before debugging Loki.
