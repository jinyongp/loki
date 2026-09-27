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

The installer writes:

```text
%LOCALAPPDATA%\Loki\<distribution-name>\
├── connection.json
├── mcp-token
└── ownership.json
```

Inspect only the non-secret connection metadata:

```powershell
Get-Content "$env:LOCALAPPDATA\Loki\loki-mcp\connection.json" -Raw
```

The MCP URL is in `local_origin.url`. The token itself is stored in
`mcp-token`; do not commit it, paste it into issue reports, or put it in a URL.

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
- **private VPN** — useful when both sides can join the same private network;
- **SSH or application tunnel** — useful for operator-controlled machines and
  temporary access;
- **reverse proxy** — useful when you intentionally operate a stable remote MCP
  endpoint;
- **managed tunnel service** — useful when inbound networking cannot be opened
  directly.

Whichever option you use, the ingress must forward Streamable HTTP requests to
Loki and preserve or deliberately re-establish Loki's bearer authentication.

Prefer private ingress unless public exposure is explicitly required. Do not
treat changing Loki from `127.0.0.1` to `0.0.0.0` as a substitute for an
access-control design.

## Provider-specific examples

Provider integrations belong here as examples of the general remote-client
model, not as part of Loki's core connection contract.

### OpenAI products / ChatGPT

For OpenAI products that support Secure MCP Tunnel, the tunnel is a
client-specific ingress option. Loki remains on loopback and `tunnel-client`
runs on the machine that can already reach Loki.

```text
OpenAI client
    |
    | OpenAI Secure MCP Tunnel
    v
tunnel-client
    |
    | http://127.0.0.1:<port>/mcp
    v
Loki
```

Typical setup:

1. Create the tunnel in the OpenAI product/platform flow.
2. Run the current OpenAI `tunnel-client` on the Loki host.
3. Point it at the Loki URL from `connection.json`.
4. Keep Loki bearer authentication enabled and configure the client connection
   to send the Loki bearer token.
5. Verify the tunnel, then register or scan the MCP tools in the OpenAI client.

For PowerShell, load the Loki URL without copying it manually:

```powershell
$connection = Get-Content "$env:LOCALAPPDATA\Loki\loki-mcp\connection.json" -Raw | ConvertFrom-Json
$env:MCP_SERVER_URL = $connection.local_origin.url
```

OpenAI's UI, tunnel-client syntax, and plan permissions can change independently
of Loki. Use the current OpenAI documentation for the provider-specific steps:

- Secure MCP Tunnel:
  https://developers.openai.com/api/docs/guides/secure-mcp-tunnels
- ChatGPT developer mode and custom MCP apps:
  https://help.openai.com/en/articles/12584461-developer-mode-and-mcp-apps-in-chatgpt

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
