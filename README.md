# tsmcp — MCP Tailnet Bridge

A Go reverse proxy that exposes private [MCP](https://modelcontextprotocol.io/) servers on your [Tailscale](https://tailscale.com/) network to [Claude.ai](https://claude.ai) via a single public FQDN — with OAuth authentication powered by Tailscale's identity provider (tsidp).

## How It Works

```
                                                    ┌──Tailnet──▶ MCP Server A
Claude.ai ──HTTPS──▶ Caddy ──HTTP──▶ tsmcp ────────┤
     │                                 │            └──Tailnet──▶ MCP Server B
     │                                 │
     │                                 └──Tailnet──▶ tsidp (/introspect)
     │                                                 ▲
     └──────────HTTPS (Funnel)─────────────────────────┘
              (OAuth: discovery, authorize, token)
```

Two separate connections to tsidp serve different purposes:

- **Claude.ai → tsidp** (over HTTPS/Funnel): OAuth discovery, user authorization, and token exchange. Claude.ai talks to tsidp directly — tsmcp is not involved in this leg.
- **tsmcp → tsidp** (over Tailnet via tsnet): Token introspection (RFC 7662). On each authenticated request, tsmcp validates the opaque token by calling tsidp's `/introspect` endpoint over the tailnet.

Each path in the config maps to a separate Claude.ai custom connector. One deployment serves multiple MCP servers.

## Prerequisites

- **Tailscale account** with at least one MCP server on the tailnet
- **tsidp** running on your tailnet with [Funnel enabled](https://tailscale.com/kb/1240/sso-custom-oidc/) — Claude.ai must reach it over the public internet for OAuth
- **VPS or server** with a **public IP**, Docker installed, and joined to your Tailscale tailnet
- **Domain name** pointed at the host (e.g., `mcp.example.com`)
- **Caddy** (or another reverse proxy) for TLS termination — must support SSE flush
- **Tailscale ACLs** allowing the bridge node to reach tsidp and your MCP servers (see [example ACL](#example-tailscale-acl) below)

## Setup Guide

### 1. Register a client with tsidp

From a machine on your tailnet, register an OAuth client for Claude.ai:

```bash
curl -s -X POST https://idp.YOUR-TAILNET.ts.net/register \
  -H "Content-Type: application/json" \
  -d '{
    "redirect_uris": ["https://claude.ai/api/mcp/auth_callback"],
    "client_name": "Claude.ai MCP",
    "grant_types": ["authorization_code", "refresh_token"],
    "response_types": ["code"],
    "token_endpoint_auth_method": "client_secret_basic"
  }' | python3 -m json.tool
```

**This must be done from a tailnet node** — tsidp rejects dynamic client registration over Funnel. Save the returned `client_id` and `client_secret`.

### 2. Generate a Tailscale auth key

Go to the [Tailscale admin console](https://login.tailscale.com/admin/settings/keys) and generate an auth key. This allows tsmcp's embedded tsnet node to join your tailnet.

- **Reusable**: Optional — tsnet state is persisted to a Docker volume, so the node re-authenticates automatically after the first start
- **Ephemeral**: No — the bridge node must remain registered so ACL rules and tsidp access continue to work
- **Tags**: e.g., `tag:tsmcp` (for ACL rules)

### 3. Create the config file

```yaml
server:
  listen: "0.0.0.0:8900"
  allowed_origins:
    - "https://claude.ai"
    - "https://claude.com"

tailnet:
  hostname: "tsmcp"
  state_dir: "/var/lib/tsmcp/tsnet"
  authkey_env: "TS_AUTHKEY"

auth:
  issuer: "https://idp.your-tailnet.ts.net"
  audience: "https://mcp.example.com"
  introspection_url: "https://idp.your-tailnet.ts.net/introspect"
  resource_metadata_url: "https://mcp.example.com/.well-known/oauth-protected-resource"

endpoints:
  - path: "/mcp/my-server"
    target: "https://my-mcp-server.your-tailnet.ts.net/mcp"
    description: "My MCP Server"
```

The `target` is the Tailscale FQDN (or MagicDNS name) of the MCP server on your tailnet. tsmcp dials it over Tailscale — the MCP server does not need to be publicly accessible.

The `auth` section is optional — omit it entirely to run without authentication.

### 4. Docker Compose

```yaml
services:
  tsmcp:
    image: meltforce/tsmcp:edge
    container_name: tsmcp
    restart: unless-stopped
    environment:
      - TS_AUTHKEY=tskey-auth-...
    volumes:
      - ./config.yaml:/etc/tsmcp/config.yaml:ro
      - tsnet-state:/var/lib/tsmcp/tsnet
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

- `tsnet-state` volume persists the Tailscale node state across restarts
- `proxy-net` is an external Docker network shared with Caddy so it can reach tsmcp by container name
- `TS_AUTHKEY` is only needed on first start; after tsnet saves state, the node re-authenticates automatically
- `NET_RAW` capability is required by tsnet's userspace networking

```bash
docker network create proxy-net  # if it doesn't exist yet
docker compose up -d
```

### 5. Configure Caddy

Add a route for your domain. The `flush_interval -1` is critical for SSE streaming:

```
mcp.example.com {
    reverse_proxy tsmcp:8900 {
        flush_interval -1
    }

    log {
        output file /var/log/caddy/mcp-access.log {
            roll_size 10MiB
            roll_keep 3
            roll_keep_for 168h
        }
        format json
    }

    header {
        Strict-Transport-Security "max-age=63072000; includeSubDomains; preload"
        X-Content-Type-Options "nosniff"
        -Server
    }
}
```

Caddy handles TLS automatically via Let's Encrypt. The `header` block adds HSTS, prevents MIME-type sniffing, and strips the `Server` header.

### 6. Add the Claude.ai connector

In [Claude.ai](https://claude.ai) settings → Integrations → Add custom integration:

- **URL**: `https://mcp.example.com/mcp/my-server`
- **Client ID**: the `client_id` from Step 1
- **Client Secret**: the `client_secret` from Step 1

Claude.ai will:
1. Hit the MCP endpoint, get a 401
2. Discover tsidp via `/.well-known/oauth-protected-resource`
3. Fetch tsidp's authorization server metadata
4. Redirect your browser to tsidp to authorize (authenticated by your Tailscale identity)
5. Exchange the code for an access token
6. Call the MCP endpoint with the token

### 7. Verify

```bash
# Health check
curl https://mcp.example.com/healthz
# → {"status":"ok","tsnet_connected":true}

# Resource metadata
curl https://mcp.example.com/.well-known/oauth-protected-resource
# → {"resource":"https://mcp.example.com","authorization_servers":["https://idp.your-tailnet.ts.net"],...}

# Unauthenticated MCP request (should get 401)
curl -X POST https://mcp.example.com/mcp/my-server \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' \
  -w "\nHTTP %{http_code}\n"
# → HTTP 401

# Container logs
docker logs tsmcp --tail 20
```

## Auth Flow in Detail

tsmcp implements the [MCP Authorization specification](https://modelcontextprotocol.io/specification/draft/basic/authorization) using Tailscale's identity provider (tsidp) as the OAuth authorization server. Here's the full flow:

### Discovery

```
Claude.ai ──POST──▶ https://mcp.example.com/mcp/my-server
           ◀── 401 Unauthorized
                WWW-Authenticate: Bearer resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource"

Claude.ai ──GET──▶ https://mcp.example.com/.well-known/oauth-protected-resource
           ◀── 200
                {
                  "resource": "https://mcp.example.com",
                  "authorization_servers": ["https://idp.your-tailnet.ts.net"]
                }

Claude.ai ──GET──▶ https://idp.your-tailnet.ts.net/.well-known/oauth-authorization-server
           ◀── 200
                {
                  "authorization_endpoint": "https://idp.your-tailnet.ts.net/authorize",
                  "token_endpoint": "https://idp.your-tailnet.ts.net/token",
                  "introspection_endpoint": "https://idp.your-tailnet.ts.net/introspect",
                  ...
                }
```

1. Claude.ai hits the MCP endpoint without a token, gets a `401` with the resource metadata URL
2. Claude.ai fetches the [RFC 9728](https://datatracker.ietf.org/doc/html/rfc9728) Protected Resource Metadata, which points to tsidp as the authorization server
3. Claude.ai fetches tsidp's [RFC 8414](https://datatracker.ietf.org/doc/html/rfc8414) Authorization Server Metadata to find the OAuth endpoints

### Authorization

```
Claude.ai ──redirect──▶ https://idp.your-tailnet.ts.net/authorize?
                           client_id=...&redirect_uri=https://claude.ai/api/mcp/auth_callback&
                           code_challenge=...&code_challenge_method=S256&state=...

   [User's browser authenticates via Tailscale identity]

tsidp ──redirect──▶ https://claude.ai/api/mcp/auth_callback?code=...&state=...

Claude.ai ──POST──▶ https://idp.your-tailnet.ts.net/token
                      grant_type=authorization_code&code=...&code_verifier=...
           ◀── { "access_token": "<opaque>", "token_type": "Bearer", "expires_in": 300 }
```

4. Claude.ai redirects the user's browser to tsidp's authorize endpoint
5. **tsidp authenticates the user via their Tailscale identity** — the browser connects to tsidp over Tailscale, and tsidp identifies the user by their tailnet node. This is the key security boundary: only users on your tailnet can authorize.
6. tsidp redirects back to Claude.ai with an authorization code
7. Claude.ai exchanges the code for an opaque access token (5-minute TTL)

### Authenticated Request

```
Claude.ai ──POST──▶ https://mcp.example.com/mcp/my-server
                      Authorization: Bearer <opaque-token>

tsmcp ──POST──▶ https://idp.your-tailnet.ts.net/introspect  (via tsnet)
                  token=<opaque-token>
       ◀── { "active": true, "sub": "user@github", "uid": "...", ... }

tsmcp ──proxy──▶ https://my-mcp-server.your-tailnet.ts.net/mcp  (via tsnet)
       ◀── MCP response (JSON-RPC / SSE)
```

8. Claude.ai sends the MCP request with the Bearer token
9. tsmcp validates the token by calling tsidp's [RFC 7662](https://www.rfc-editor.org/rfc/rfc7662) introspection endpoint **over Tailscale** (via tsnet) — this is critical because tsidp resolves to a Tailscale IP that isn't reachable from Docker's network otherwise
10. If the token is active, tsmcp proxies the request to the upstream MCP server over Tailscale
11. Introspection results are cached (60s or until token expiry, whichever is shorter)

### Security Model

- **tsidp rejects dynamic client registration (DCR) over Funnel** — clients must be pre-registered from a tailnet node
- **The authorize endpoint requires Tailscale identity** — the user's browser must connect to tsidp over the tailnet. tsidp identifies users by their Tailscale node identity, not by a login form.
- **Tokens are opaque** — tsidp issues opaque access tokens (not JWTs), so they can only be validated via introspection
- **Introspection goes through tsnet** — tsmcp dials tsidp over the tailnet, so ACLs control which nodes can validate tokens
- A stranger cannot complete the OAuth flow: they can discover tsidp (via resource metadata), but they cannot register a client or authorize because those operations require tailnet access

### Example Tailscale ACL

The bridge node needs to reach both tsidp (for token introspection) and the MCP servers (for proxying). tsidp also needs an app grant so that clients can register and users can authorize:

```jsonc
// Tag definitions
"tagOwners": {
    "tag:tsmcp": ["autogroup:admin"],  // MCP bridge
    "tag:idp":   ["autogroup:admin"],  // Tailscale identity provider
    "tag:mcp":   ["autogroup:admin"],  // MCP servers
},

"acls": [
    // Allow the bridge to reach MCP servers
    {
        "src": ["tag:tsmcp"],
        "dst": ["tag:mcp"],
        "ip":  ["tcp:443"],
    },

    // tsidp — admin access (admin UI + DCR)
    {
        "src": ["autogroup:admin"],
        "dst": ["tag:idp"],
        "app": {
            "tailscale.com/cap/tsidp": [{
                "allow_admin_ui": true,
                "allow_dcr":      true,
                "resources":      ["*"],
                "scopes":         ["openid", "email", "profile"],
                "users":          ["*"],
            }],
        },
    },

    // tsidp — all tailnet users (DCR only, no admin UI)
    {
        "src": ["*"],
        "dst": ["tag:idp"],
        "app": {
            "tailscale.com/cap/tsidp": [{
                "allow_admin_ui": false,
                "allow_dcr":      true,
                "resources":      ["*"],
                "scopes":         ["openid", "email", "profile"],
                "users":          ["*"],
            }],
        },
    },
]
```

## Features

- **Path-based routing** — single domain, multiple MCP servers
- **SSE streaming** — automatic flush for Server-Sent Events
- **OAuth auth** — validate opaque tokens via RFC 7662 introspection against tsidp
- **RFC 9728 metadata** — `/.well-known/oauth-protected-resource` for MCP auth discovery
- **Tailscale identity** — authentication is backed by Tailscale node identity, not passwords
- **Origin validation** — restrict to `claude.ai` / `claude.com`
- **Health check** — `/healthz` with tsnet readiness
- **Structured logging** — JSON via `slog`
- **Hardened Docker** — read-only root, cap_drop ALL, unprivileged user

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

### `auth` (optional — omit for authless mode)

| Field | Required | Description |
|-------|----------|-------------|
| `issuer` | Yes | tsidp issuer URL (your tailnet's IDP). |
| `audience` | Yes | Your bridge's public URL. |
| `introspection_url` | Yes | tsidp introspection endpoint. Must be `http` or `https`. |
| `client_id` | No | Client ID for authenticating introspection requests. |
| `client_secret` | No | Client secret for introspection auth. |
| `resource_metadata_url` | Yes | Public URL of the RFC 9728 metadata endpoint. |

Notes:
- `client_id` and `client_secret` are only needed if tsidp requires authentication for introspection calls. Tailscale's tsidp currently allows unauthenticated introspection.
- The `introspection_url` must be reachable from inside the container. Since tsidp resolves to a Tailscale IP, tsmcp routes introspection requests through its embedded tsnet node automatically.
- Active introspection results are cached for 60 seconds (or until token expiry, whichever is shorter) to reduce round-trips.

### `endpoints`

| Field | Required | Description |
|-------|----------|-------------|
| `path` | Yes | URL path for this endpoint (e.g., `/mcp/my-server`). Must start with `/`. |
| `target` | Yes | Upstream MCP server URL (Tailscale FQDN). Must be `http` or `https`. |
| `description` | No | Human-readable description for logging. |
| `enabled` | No | Set to `false` to disable. Default: `true`. |
| `upstream_token_env` | No | Environment variable holding a Bearer token to set on upstream requests. |

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
