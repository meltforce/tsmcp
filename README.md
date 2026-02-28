# tsmcp — MCP Tailnet Bridge

A Go reverse proxy that exposes private [MCP](https://modelcontextprotocol.io/) servers on your [Tailscale](https://tailscale.com/) network to [Claude.ai](https://claude.ai) via a single public FQDN.

## How It Works

```
Claude.ai ──HTTPS──▶ Caddy ──HTTP──▶ tsmcp ──Tailnet──▶ MCP Server A
                                        │
                                        ├──Tailnet──▶ MCP Server B
                                        │
                                        └──Tailnet──▶ MCP Server C
```

1. Claude.ai sends MCP requests (JSON-RPC over HTTP + SSE) to your public domain
2. Caddy terminates TLS and forwards to tsmcp
3. tsmcp routes by path, dials the target MCP server over Tailscale via [tsnet](https://pkg.go.dev/tailscale.com/tsnet), and proxies the response
4. SSE streams are flushed immediately — no buffering

Each path in the config maps to a separate Claude.ai custom connector. One deployment serves multiple MCP servers.

## Features

- **Path-based routing** — single domain, multiple MCP servers
- **SSE streaming** — automatic flush for Server-Sent Events
- **Optional OAuth auth** — validate opaque tokens via RFC 7662 introspection against tsidp
- **RFC 9728 metadata** — `/.well-known/oauth-protected-resource` endpoint
- **Origin validation** — restrict to `claude.ai` / `claude.com`
- **Health check** — `/healthz` with tsnet readiness
- **Structured logging** — JSON via `slog`
- **Hardened Docker** — read-only root, cap_drop ALL, unprivileged user

## Deployment Guide

### Prerequisites

- A VPS or server (the "host") with Docker installed
- A [Tailscale](https://tailscale.com/) account with at least one MCP server on the tailnet
- A domain name pointed at the host (e.g., `mcp.example.com`)
- [Caddy](https://caddyserver.com/) (or another reverse proxy) for TLS termination
- [tsidp](https://tailscale.com/kb/1240/sso-custom-oidc/) enabled on your tailnet (only needed for auth)

### Step 1: Generate a Tailscale auth key

Go to the [Tailscale admin console](https://login.tailscale.com/admin/settings/keys) and generate a reusable auth key. This allows tsmcp's embedded tsnet node to join your tailnet.

- **Reusable**: Yes (survives container restarts)
- **Ephemeral**: Optional (node auto-removes when container stops)
- **Tags**: Optional (e.g., `tag:mcp-bridge`)

Save the key — you'll need it for the config.

### Step 2: Create the config file

```yaml
server:
  listen: "0.0.0.0:8900"
  allowed_origins:
    - "https://claude.ai"
    - "https://claude.com"

tailnet:
  hostname: "mcp-bridge"
  state_dir: "/var/lib/mcp-bridge/tsnet"
  authkey_env: "TS_AUTHKEY"

endpoints:
  - path: "/mcp/my-server"
    target: "https://my-mcp-server.my-tailnet.ts.net/mcp"
    description: "My MCP Server"
```

The `target` is the Tailscale FQDN (or MagicDNS name) of the MCP server on your tailnet. tsmcp dials it over Tailscale — the MCP server does not need to be publicly accessible.

### Step 3: Create the Docker Compose file

```yaml
services:
  mcp-bridge:
    image: meltforce/tsmcp:edge
    container_name: tsmcp
    restart: unless-stopped
    environment:
      - TS_AUTHKEY=tskey-auth-...
    volumes:
      - ./config.yaml:/etc/mcp-bridge/config.yaml:ro
      - tsnet-state:/var/lib/mcp-bridge/tsnet
    networks:
      - proxy-net
    cap_drop:
      - ALL
    cap_add:
      - NET_RAW
    security_opt:
      - no-new-privileges:true
    read_only: true
    tmpfs:
      - /tmp:noexec,nosuid,size=64m
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://127.0.0.1:8900/healthz"]
      interval: 10s
      timeout: 3s
      start_period: 30s
      retries: 3

volumes:
  tsnet-state:

networks:
  proxy-net:
    external: true
```

Notes:
- `tsnet-state` volume persists the Tailscale node state across restarts
- `proxy-net` is an external Docker network shared with Caddy so it can reach tsmcp by container name
- `TS_AUTHKEY` is only needed on first start; after tsnet saves state, the node re-authenticates automatically
- `NET_RAW` capability is required by tsnet's userspace networking

Start it:

```bash
docker network create proxy-net  # if it doesn't exist yet
docker compose up -d
```

### Step 4: Configure Caddy

Add a route for your domain. The `flush_interval -1` is critical for SSE streaming:

```
mcp.example.com {
    reverse_proxy tsmcp:8900 {
        flush_interval -1
    }
}
```

Caddy handles TLS automatically via Let's Encrypt.

### Step 5: Add the Claude.ai connector

In [Claude.ai](https://claude.ai) settings → Integrations → Add custom integration:

- **URL**: `https://mcp.example.com/mcp/my-server`
- **Name**: Whatever you want

If auth is **disabled** (no `auth:` section in config), the connector will work immediately.

If auth is **enabled**, Claude.ai will discover the OAuth flow via the `/.well-known/oauth-protected-resource` endpoint and redirect you to tsidp to authorize. See the Auth section below.

### Step 6: Verify

```bash
# Health check
curl https://mcp.example.com/healthz
# → {"status":"ok","tsnet_connected":true}

# Container logs
docker logs tsmcp --tail 20
```

## Enabling Auth (Optional)

Auth uses Tailscale's identity provider (tsidp) to authenticate users via OAuth. tsmcp validates tokens by calling tsidp's [introspection endpoint](https://www.rfc-editor.org/rfc/rfc7662) (RFC 7662).

### How the flow works

```
Claude.ai ──▶ GET /.well-known/oauth-protected-resource
          ◀── 200 { "resource": "...", "authorization_servers": ["..."] }

Claude.ai ──▶ [discovers tsidp, redirects user to authorize]
          ◀── [user authorizes, Claude.ai gets opaque access token]

Claude.ai ──▶ POST /mcp/my-server
               Authorization: Bearer <opaque-token>

tsmcp     ──▶ POST https://idp.your-tailnet.ts.net/introspect
               token=<opaque-token>
          ◀── {"active": true, "sub": "user@example.com", ...}

tsmcp     ──▶ [proxies request to MCP server]
```

### Enable tsidp

tsidp must be enabled on your tailnet. Check if it's available:

```bash
curl -s https://idp.YOUR-TAILNET.ts.net/.well-known/openid-configuration | jq .
```

If this returns OIDC discovery metadata, tsidp is active. Note the `introspection_endpoint` URL.

### Add auth to config

```yaml
auth:
  issuer: "https://idp.your-tailnet.ts.net"
  audience: "https://mcp.example.com"
  introspection_url: "https://idp.your-tailnet.ts.net/introspect"
  resource_metadata_url: "https://mcp.example.com/.well-known/oauth-protected-resource"
```

| Field | Required | Description |
|-------|----------|-------------|
| `issuer` | Yes | tsidp issuer URL (your tailnet's IDP). |
| `audience` | Yes | Your bridge's public URL. |
| `introspection_url` | Yes | tsidp introspection endpoint. Must be `http` or `https`. |
| `client_id` | No | Client ID for authenticating introspection requests (if tsidp requires it). |
| `client_secret` | No | Client secret for introspection auth. |
| `resource_metadata_url` | Yes | Public URL of the RFC 9728 metadata endpoint. |

Notes:
- `client_id` and `client_secret` are only needed if tsidp requires authentication for introspection calls. Tailscale's tsidp currently allows unauthenticated introspection.
- The `introspection_url` must be reachable from inside the container. Since tsidp resolves to a Tailscale IP, tsmcp routes introspection requests through its embedded tsnet node automatically.
- Active introspection results are cached for 60 seconds (or until token expiry, whichever is shorter) to reduce round-trips.

### Unauthenticated routes

These routes never require auth, even when the `auth:` section is configured:
- `GET /healthz`
- `GET /.well-known/oauth-protected-resource`

### Unauthorized responses

Requests without a valid token get:

```
HTTP/1.1 401 Unauthorized
WWW-Authenticate: Bearer resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource"
```

## Configuration Reference

### `server`

| Field | Required | Description |
|-------|----------|-------------|
| `listen` | Yes | Bind address. Must be loopback (`127.0.0.1`) or unspecified (`0.0.0.0`). |
| `allowed_origins` | No | List of allowed Origin headers. Empty = allow all (dev mode). |

### `tailnet`

| Field | Required | Description |
|-------|----------|-------------|
| `hostname` | Yes | Tailscale node name for the bridge. |
| `state_dir` | Yes | Directory for tsnet persistent state. |
| `authkey_env` | Yes | Environment variable containing the Tailscale auth key. |

### `endpoints`

| Field | Required | Description |
|-------|----------|-------------|
| `path` | Yes | URL path for this endpoint (e.g., `/mcp/health`). Must start with `/`. |
| `target` | Yes | Upstream MCP server URL. Must be `http` or `https`. |
| `description` | No | Human-readable description for logging. |
| `enabled` | No | Set to `false` to disable. Default: `true`. |

## Health Check

```bash
curl http://127.0.0.1:8900/healthz
```

```json
{"status":"ok","tsnet_connected":true}
```

Returns `200` when tsnet is connected, `503` when degraded.

## Development

```bash
# Run tests
go test ./...

# Run with debug logging
go run . -config config.yaml -debug

# Build
go build -o tsmcp .
```

## Project Structure

```
├── main.go                           # Entry point, lifecycle management
├── internal/
│   ├── config/                       # YAML config loading + validation
│   ├── auth/                         # Token introspection + RFC 9728 metadata
│   ├── proxy/                        # Reverse proxy handler + transport
│   ├── tsbridge/                     # Tailscale network bridge (tsnet)
│   ├── health/                       # Health check endpoint
│   └── server/                       # HTTP server assembly + middleware
├── Dockerfile                        # Multi-stage build (48MB image)
├── docker-compose.yml                # Production deployment
└── .github/workflows/                # CI/CD (deploy + release)
```
