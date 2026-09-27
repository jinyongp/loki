# Connect an MCP client

Loki exposes one local Streamable HTTP MCP origin. The endpoint is loopback-only
by default and uses bearer-token authentication.

The connection model is intentionally simple:

```text
MCP client
    |
    | Streamable HTTP + Authorization: Bearer ...
    v
Loki local origin
```

A client on the same machine can connect directly. A hosted client such as
ChatGPT needs a private tunnel or another operator-managed ingress path.

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

## Local MCP clients

If the client runs on the same machine and supports Streamable HTTP, configure:

- server URL: the `local_origin.url` from Loki;
- transport: Streamable HTTP;
- authentication: `Authorization: Bearer <token>`, using the token from the
  generated token file.

No public DNS, TLS certificate, or tunnel is required for this case.

## ChatGPT

ChatGPT does not connect directly to localhost MCP servers. For a Loki instance
running on a developer machine, the simplest private path is **OpenAI Secure MCP
Tunnel**.

The tunnel keeps Loki on loopback. `tunnel-client` runs on the machine that can
already reach Loki and opens an outbound HTTPS connection to OpenAI.

```text
ChatGPT
    |
    | OpenAI Secure MCP Tunnel
    v
tunnel-client
    |
    | http://127.0.0.1:<port>/mcp
    v
Loki
```

### Setup outline

1. Create a tunnel in OpenAI Platform tunnel settings.
2. Install the current OpenAI `tunnel-client` on the Windows machine running
   Loki.
3. Point the tunnel client at the Loki URL from `connection.json`.
4. Keep Loki's bearer authentication enabled. Configure the ChatGPT custom app
   so MCP requests carry `Authorization: Bearer <Loki token>`; Secure MCP
   Tunnel forwards connector authorization to the configured MCP server origin.
5. Run `tunnel-client doctor`, then keep `tunnel-client run` running.
6. In ChatGPT developer mode, create a custom app using the tunnel, scan the
   tools, and test the draft app.

The relevant tunnel-client inputs are:

```text
CONTROL_PLANE_API_KEY   OpenAI tunnel runtime key
CONTROL_PLANE_TUNNEL_ID OpenAI tunnel ID
MCP_SERVER_URL          Loki local_origin.url
```

For PowerShell, the Loki URL can be loaded without copying it manually:

```powershell
$connection = Get-Content "$env:LOCALAPPDATA\Loki\loki-mcp\connection.json" -Raw | ConvertFrom-Json
$env:MCP_SERVER_URL = $connection.local_origin.url
```

Use `tunnel-client help quickstart` for the current tunnel-client setup syntax.
OpenAI's product UI and plan permissions can change independently of Loki, so
use the current OpenAI documentation for tunnel creation and ChatGPT developer
mode:

- Secure MCP Tunnel:
  https://developers.openai.com/api/docs/guides/secure-mcp-tunnels
- ChatGPT developer mode and custom MCP apps:
  https://help.openai.com/en/articles/12584461-developer-mode-and-mcp-apps-in-chatgpt

## Other remote clients

Secure MCP Tunnel is specific to supported OpenAI products. Other hosted MCP
clients need an operator-managed path that can reach Loki, for example:

- a private VPN;
- an SSH or application tunnel;
- a reverse proxy;
- another managed tunnel service.

Keep the Loki origin private unless public exposure is intentional. Any ingress
must preserve bearer authentication and forward MCP requests to the local
Streamable HTTP origin.

Loki deliberately does not create DNS, TLS certificates, public tunnels, VPN
routes, OAuth providers, or public MCP endpoints on the user's behalf.

## Troubleshooting

If a client cannot connect:

1. Run `loki host doctor` on Linux or the system-scoped equivalent inside the
   Windows appliance.
2. Confirm the URL in `connection.json` matches the running Loki port.
3. Confirm the client or tunnel host can reach that URL.
4. Confirm the request carries the Loki bearer token.
5. For ChatGPT, confirm `tunnel-client` is healthy before scanning or invoking
   tools.
