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
- **Optional JWT auth** — validate tokens from Tailscale's identity provider (tsidp)
- **RFC 9728 metadata** — `/.well-known/oauth-protected-resource` endpoint
- **Origin validation** — restrict to `claude.ai` / `claude.com`
- **Health check** — `/healthz` with tsnet readiness
- **Structured logging** — JSON via `slog`
- **Hardened Docker** — read-only root, cap_drop ALL, unprivileged user

## Quick Start

### 1. Create a config file

```yaml
server:
  listen: "127.0.0.1:8900"
  allowed_origins:
    - "https://claude.ai"
    - "https://claude.com"

tailnet:
  hostname: "mcp-bridge"
  state_dir: "/var/lib/mcp-bridge/tsnet"
  authkey_env: "TS_AUTHKEY"

# Optional: JWT auth via Tailscale identity provider.
# Omit this section entirely for authless mode.
# auth:
#   issuer: "https://login.tailscale.com/oidc"
#   audience: "https://mcp.example.com"
#   jwks_url: "https://login.tailscale.com/oidc/jwks"
#   resource_metadata_url: "https://mcp.example.com/.well-known/oauth-protected-resource"

endpoints:
  - path: "/mcp/health"
    target: "http://freeresp:3001/mcp"
    description: "Health Data MCP Server"

  - path: "/mcp/infra"
    target: "http://homelab-mcp:3000/mcp"
    description: "Infrastructure Management"
```

### 2. Run with Docker Compose

```yaml
# docker-compose.yml
services:
  mcp-bridge:
    image: meltforce/tsmcp:edge
    container_name: tsmcp
    restart: unless-stopped
    environment:
      - TS_AUTHKEY=${TS_AUTHKEY}
    volumes:
      - ./config.yaml:/etc/mcp-bridge/config.yaml:ro
      - tsnet-state:/var/lib/mcp-bridge/tsnet
    networks:
      - proxy-net
    security_opt:
      - no-new-privileges:true
    cap_drop:
      - ALL
    cap_add:
      - NET_RAW
    read_only: true
    tmpfs:
      - /tmp:noexec,nosuid,size=64m

volumes:
  tsnet-state:

networks:
  proxy-net:
    external: true
```

```bash
export TS_AUTHKEY="tskey-auth-..."
docker compose up -d
```

### 3. Add a Caddy route

```
mcp.example.com {
    reverse_proxy tsmcp:8900 {
        flush_interval -1
    }
}
```

### 4. Add as Claude.ai custom connector

In Claude.ai settings, add a custom MCP connector pointing to `https://mcp.example.com/mcp/health` (or whichever endpoint path you configured).

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

### `auth` (optional)

Omit entirely for authless mode. When present, all four fields are required.

| Field | Required | Description |
|-------|----------|-------------|
| `issuer` | Yes | Expected `iss` claim (tsidp OIDC URL). |
| `audience` | Yes | Expected `aud` claim (bridge's public URL). |
| `jwks_url` | Yes | JWKS endpoint for public key verification. Must be `http` or `https`. |
| `resource_metadata_url` | Yes | Public URL of the RFC 9728 metadata endpoint. |

### `endpoints`

| Field | Required | Description |
|-------|----------|-------------|
| `path` | Yes | URL path for this endpoint (e.g., `/mcp/health`). Must start with `/`. |
| `target` | Yes | Upstream MCP server URL. Must be `http` or `https`. |
| `description` | No | Human-readable description for logging. |
| `enabled` | No | Set to `false` to disable. Default: `true`. |

## Auth Flow

When the `auth` section is configured:

```
Client ──▶ GET /.well-known/oauth-protected-resource
       ◀── 200 { "resource": "...", "authorization_servers": ["..."] }

Client ──▶ [obtains token from tsidp]

Client ──▶ POST /mcp/health
            Authorization: Bearer <jwt>
       ◀── 200 (proxied response)
```

Without a valid token:

```
Client ──▶ POST /mcp/health
       ◀── 401 Unauthorized
            WWW-Authenticate: Bearer resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource"
```

Routes that never require auth:
- `GET /healthz`
- `GET /.well-known/oauth-protected-resource`

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
├── config.yaml.example               # Configuration template
├── internal/
│   ├── config/                       # YAML config loading + validation
│   ├── auth/                         # JWT middleware + RFC 9728 metadata
│   ├── proxy/                        # Reverse proxy handler + transport
│   ├── tsbridge/                     # Tailscale network bridge (tsnet)
│   ├── health/                       # Health check endpoint
│   └── server/                       # HTTP server assembly + middleware
├── Dockerfile                        # Multi-stage build (48MB image)
├── docker-compose.yml                # Production deployment
└── .github/workflows/                # CI/CD (deploy + release)
```
