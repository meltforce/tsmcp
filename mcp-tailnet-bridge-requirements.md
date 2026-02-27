# MCP Tailnet Bridge — Requirements Document

**Version**: 0.1.0-draft  
**Date**: 2026-02-27  
**Author**: Linus (concept), Claude (documentation)  
**Status**: Draft / Pre-Implementation

---

## 1. Executive Summary

The MCP Tailnet Bridge is a Go-based reverse proxy that exposes private MCP (Model Context Protocol) servers running inside a Tailscale network to the public internet via a hardened HTTPS endpoint. It enables Claude.ai (and other MCP clients) to access internal MCP servers through the standard Custom Connectors / Remote MCP workflow — eliminating the need for local JSON configuration files, Claude Desktop, or publicly exposing internal services directly.

No existing project covers this exact use case. The closest prior art — `jaxxstorm/tailscale-mcp-proxy` — only bridges Claude Desktop (stdio) to Tailnet HTTP servers. The reverse direction (public internet → Tailnet) with proper OAuth remains an open gap, explicitly identified by Tailscale engineer Lee Briggs in the August 2025 Tailscale blog post on MCP connectivity.

---

## 2. Problem Statement

### 2.1 Current Pain Points

- **Claude.ai requires publicly reachable MCP servers**: The web/mobile app cannot connect to servers behind NAT, VPN, or private networks.
- **Local MCP configuration is cumbersome**: Claude Desktop's JSON config (`claude_desktop_config.json`) requires manual editing, process management, and doesn't work in the browser.
- **Exposing internal services directly is unacceptable**: Running MCP servers on the public internet with OAuth bolted on expands the attack surface unnecessarily.
- **Tailscale Funnel is insufficient**: Funnel provides public exposure but lacks OAuth integration, multi-endpoint routing, and has bandwidth limitations.

### 2.2 Target Scenario

A homelab operator runs multiple MCP servers (e.g., health data, NAS access, infrastructure management) on machines inside their Tailscale network. They want to use these servers seamlessly from Claude.ai in a browser or mobile app, with the same UX as first-party connectors like Gmail or Google Calendar.

---

## 3. Goals & Non-Goals

### 3.1 Goals

- **G1**: Provide a single public HTTPS endpoint that Claude.ai can connect to as a Custom Connector.
- **G2**: Implement the MCP authorization spec (OAuth 2.1 + DCR/CIMD) so Claude.ai can authenticate without manual token management.
- **G3**: Route authenticated MCP requests to internal servers via Tailscale's `tsnet` library, with the bridge acting as a dedicated, isolated Tailnet node.
- **G4**: Enable fine-grained access control using Tailscale ACLs and tags, restricting the bridge to only whitelisted internal MCP server endpoints.
- **G5**: Support multiple internal MCP servers behind a single public endpoint with path-based routing.
- **G6**: Correctly proxy MCP Streamable HTTP transport, including SSE streaming for long-running tool calls.

### 3.2 Non-Goals

- **NG1**: The bridge does not implement MCP server logic — it is a pure transport proxy.
- **NG2**: Multi-tenancy / multi-user support is out of scope for v1. The bridge serves a single operator.
- **NG3**: Implementing a full-featured identity provider. The bridge acts as its own minimal OAuth authorization server or delegates to an external IdP.
- **NG4**: Support for MCP stdio transport. Only HTTP-based transports are proxied.
- **NG5**: Running as a Caddy plugin. The bridge runs as a standalone binary behind Caddy.

---

## 4. Architecture Overview

```
┌──────────────────────────────────────────────────────────┐
│                     Public Internet                       │
│                                                          │
│  Claude.ai ──HTTPS──▶ Caddy (TLS + Rate Limit)          │
│                          │                               │
│                     localhost:8900                        │
│                          │                               │
│              ┌───────────▼────────────────┐              │
│              │    mcp-tailnet-bridge       │              │
│              │                            │              │
│              │  ┌──────────────────────┐  │              │
│              │  │  OAuth 2.1 Server    │  │              │
│              │  │  (DCR + PKCE)        │  │              │
│              │  └──────────────────────┘  │              │
│              │  ┌──────────────────────┐  │              │
│              │  │  MCP Proxy Engine    │  │              │
│              │  │  (Streamable HTTP)   │  │              │
│              │  └──────────────────────┘  │              │
│              │  ┌──────────────────────┐  │              │
│              │  │  tsnet.Server        │  │              │
│              │  │  Node: "mcp-bridge"  │  │              │
│              │  └──────────────────────┘  │              │
│              └───────────┬────────────────┘              │
│                          │                               │
└──────────────────────────┼───────────────────────────────┘
                           │ WireGuard (Tailnet)
┌──────────────────────────┼───────────────────────────────┐
│                    Tailscale Network                      │
│                          │                               │
│           ┌──────────────┼──────────────┐                │
│           ▼              ▼              ▼                 │
│    mcp-server-a    mcp-server-b    mcp-server-c          │
│    (homelab:3000)  (homelab:3001)  (nas:3002)            │
│    Health Data     Infra Mgmt      File Access           │
└──────────────────────────────────────────────────────────┘
```

### 4.1 Component Responsibilities

| Component | Role |
|---|---|
| **Caddy** (existing) | TLS termination, rate limiting, geo-blocking, reverse proxy to bridge |
| **mcp-tailnet-bridge** | OAuth server, MCP transport proxy, tsnet Tailnet node |
| **tsnet.Server** | Dedicated Tailnet identity with own ACL tags, no host Tailscale dependency |
| **Tailscale ACLs** | Restrict bridge to specific internal server:port combinations |

### 4.2 Why Standalone Go Binary (Not Caddy Plugin)

| Criterion | Caddy Plugin | Standalone Go Binary |
|---|---|---|
| tsnet lifecycle management | Conflicts with Caddy's process model | Full control |
| MCP-specific streaming (SSE) | Requires Caddy middleware hacks | Native `http.Flusher` control |
| OAuth 2.1 + DCR | Would need custom Caddy modules | Standard Go libraries |
| Deployment | Caddy rebuild on every change | Independent systemd service |
| Caddy update resilience | API breakage risk | Decoupled |

**Decision**: Standalone Go binary behind Caddy.

---

## 5. Detailed Requirements

### 5.1 MCP Transport Proxy

#### 5.1.1 Streamable HTTP (Primary)

The bridge MUST implement a spec-compliant MCP Streamable HTTP proxy per the MCP specification (2025-03-26 / 2025-06-18).

- **R-T1**: Single HTTP endpoint path (e.g., `/mcp/{route}`) supporting POST, GET, and DELETE methods.
- **R-T2**: POST requests with `Content-Type: application/json` MUST be forwarded to the target MCP server and the response returned to the client.
- **R-T3**: When the upstream MCP server returns `Content-Type: text/event-stream`, the bridge MUST stream SSE events to the client without buffering. This requires explicit `http.Flusher` usage and disabling response buffering.
- **R-T4**: The `Mcp-Session-Id` header MUST be forwarded bidirectionally for session management.
- **R-T5**: GET requests with `Accept: text/event-stream` (server-to-client notification streams) MUST be proxied as long-lived SSE connections.
- **R-T6**: DELETE requests for session termination MUST be forwarded.
- **R-T7**: The bridge MUST validate the `Origin` header on incoming requests to prevent DNS rebinding attacks.

#### 5.1.2 Legacy SSE (Backwards Compatibility)

- **R-T8**: Claude.ai currently supports both SSE and Streamable HTTP transports. The bridge SHOULD support legacy SSE (`/sse` + `/messages` dual-endpoint pattern) as a fallback if the upstream MCP server uses it.

#### 5.1.3 JSON-RPC

- **R-T9**: The bridge MUST NOT inspect, modify, or validate JSON-RPC message content. It is a transparent transport proxy.
- **R-T10**: Exception: The bridge MAY log method names (e.g., `tools/call`, `initialize`) for audit purposes without modifying the payload.

### 5.2 OAuth 2.1 Authorization Server

Claude.ai connects to remote MCP servers via the Custom Connectors feature. The OAuth requirements are defined by the MCP Authorization specification (draft) and Claude.ai's specific implementation.

#### 5.2.1 Discovery Endpoints

- **R-A1**: The bridge MUST serve OAuth 2.0 Protected Resource Metadata (RFC 9728) at `/.well-known/oauth-protected-resource` (and/or at the path-specific variant). This document MUST include the `authorization_servers` field pointing to the bridge's own authorization server, and SHOULD include `scopes_supported`.
- **R-A2**: The bridge MUST serve OAuth 2.0 Authorization Server Metadata (RFC 8414) at `/.well-known/oauth-authorization-server`. This document MUST include endpoints for authorization, token, and optionally registration.
- **R-A3**: The bridge MUST return `401 Unauthorized` with a `WWW-Authenticate: Bearer resource_metadata="..."` header when an unauthenticated request is received.

#### 5.2.2 Client Registration

Claude.ai supports three client registration approaches. The bridge MUST support at least one.

- **R-A4 (DCR)**: The bridge SHOULD implement Dynamic Client Registration (RFC 7591). Claude.ai supports DCR and has historically used it. The registration endpoint MUST accept POST requests with client metadata and return a `client_id` (and optionally `client_secret`).
- **R-A5 (CIMD)**: The bridge SHOULD support Client ID Metadata Documents (OIDC CIMD, draft-ietf-oauth-client-id-metadata-document-00) as the MCP spec's preferred approach. When Claude.ai presents a URL-format `client_id`, the bridge's authorization server SHOULD fetch and validate the metadata document at that URL.
- **R-A6 (Pre-registration)**: The bridge SHOULD support pre-registered client credentials. Claude.ai allows users to specify a custom Client ID and Client Secret in the "Advanced settings" when adding a connector.
- **R-A7**: DCR clients MUST be persisted across bridge restarts (in a local database or file).
- **R-A8**: The bridge MUST support DCR client invalidation: returning HTTP 401 with `error=invalid_client` from the token endpoint to signal Claude.ai to re-register.

#### 5.2.3 Authorization Flow

- **R-A9**: The bridge MUST implement the Authorization Code Grant with PKCE (RFC 7636). PKCE is mandatory per OAuth 2.1.
- **R-A10**: The bridge MUST accept Claude.ai's callback URL `https://claude.ai/api/mcp/auth_callback` (and `https://claude.com/api/mcp/auth_callback`) as valid redirect URIs.
- **R-A11**: The authorization endpoint MUST present a consent/login screen (can be minimal — e.g., a single passphrase or TOTP challenge for the single operator).
- **R-A12**: The bridge MUST issue short-lived JWT access tokens (recommended TTL: 1 hour) with refresh token support.
- **R-A13**: The bridge MUST support token refresh. Claude.ai implements token expiry and refresh.
- **R-A14**: Refresh tokens for public clients MUST be rotated on each use per OAuth 2.1.
- **R-A15**: The bridge SHOULD implement the `resource` parameter (RFC 8707 Resource Indicators) in authorization and token requests. Note: Claude.ai may not currently send this parameter, so validation SHOULD be lenient.

#### 5.2.4 Token Validation

- **R-A16**: The bridge MUST validate Bearer tokens on every MCP request (`Authorization: Bearer <token>`).
- **R-A17**: Invalid or expired tokens MUST result in HTTP 401.
- **R-A18**: Insufficient scope MUST result in HTTP 403 with appropriate `WWW-Authenticate` header.

### 5.3 Tailnet Integration (tsnet)

- **R-N1**: The bridge MUST use `tsnet.Server` to join the Tailnet as its own dedicated node (hostname: configurable, e.g., `mcp-bridge`).
- **R-N2**: The bridge MUST NOT require a Tailscale installation on the host machine. `tsnet` operates independently.
- **R-N3**: The bridge MUST support Tailscale auth keys (for initial setup) and persist its node state in a configurable state directory.
- **R-N4**: The bridge SHOULD support Tailscale OAuth client secrets (for zero-expiry node management in production).
- **R-N5**: When forwarding requests to internal MCP servers, the bridge MUST use `tsnet`'s HTTP client (dialing through the Tailnet) rather than the host's network stack.
- **R-N6**: The bridge MUST NOT listen on the Tailnet interface for incoming connections. It only initiates outbound connections to internal MCP servers.

### 5.4 Routing & Configuration

- **R-C1**: The bridge MUST support path-based routing from public endpoint paths to internal Tailnet MCP server URLs.
- **R-C2**: Configuration MUST be file-based (YAML or TOML).
- **R-C3**: Each route MUST specify at minimum: public path prefix, internal target URL (Tailnet hostname + port + path).
- **R-C4**: Routes SHOULD support optional metadata (description, enabled/disabled toggle).
- **R-C5**: The bridge SHOULD support hot-reload of routing configuration without restart (via SIGHUP or file watch).

Example configuration:

```yaml
server:
  listen: "127.0.0.1:8900"

tailnet:
  hostname: "mcp-bridge"
  state_dir: "/var/lib/mcp-bridge/tsnet"
  authkey_env: "TS_AUTHKEY"  # or authkey_file

auth:
  issuer: "https://mcp.example.com"
  signing_key_file: "/var/lib/mcp-bridge/signing.key"
  access_token_ttl: "1h"
  refresh_token_ttl: "30d"
  # Single-operator authentication
  operator_password_hash: "$argon2id$..."  # or TOTP secret

endpoints:
  - path: "/mcp/health"
    target: "http://freeresp.mcp-bridge.ts.net:3001/mcp"
    description: "FreeReps Health Data"
  - path: "/mcp/infra"
    target: "http://homelab-mcp.mcp-bridge.ts.net:3000/mcp"
    description: "Homelab Infrastructure"
  - path: "/mcp/files"
    target: "http://nas.mcp-bridge.ts.net:3002/mcp"
    description: "NAS File Access"
```

### 5.5 Security Requirements

#### 5.5.1 Network Isolation

- **R-S1**: The tsnet node MUST use a dedicated Tailscale ACL tag (e.g., `tag:mcp-proxy`).
- **R-S2**: Tailscale ACLs MUST restrict the bridge to only the specific host:port combinations of configured MCP servers. No blanket access.
- **R-S3**: The bridge MUST bind its public-facing listener to `127.0.0.1` only (Caddy handles external traffic).

#### 5.5.2 Authentication Hardening

- **R-S4**: The operator login (authorization endpoint) MUST support at least one of: strong passphrase (Argon2id-hashed), TOTP, or WebAuthn.
- **R-S5**: Failed login attempts MUST be rate-limited (e.g., exponential backoff after 5 failures).
- **R-S6**: The bridge MUST NOT store secrets (signing keys, auth keys) in the configuration file. Environment variables or external files only.

#### 5.5.3 Token Security

- **R-S7**: JWT signing keys MUST be at least 256-bit (RS256 or EdDSA).
- **R-S8**: Access tokens MUST include `iss`, `sub`, `aud`, `exp`, `iat`, `jti` claims.
- **R-S9**: The bridge MUST maintain a JTI (JWT ID) denylist for revoked tokens.
- **R-S10**: All token storage (DCR client database, refresh tokens) MUST be encrypted at rest.

#### 5.5.4 Transport Security

- **R-S11**: The bridge MUST refuse non-TLS connections when running behind Caddy (enforced by Caddy config, not the bridge itself).
- **R-S12**: Communication between the bridge and internal MCP servers over Tailnet uses WireGuard encryption (inherent to Tailscale). No additional TLS layer required.
- **R-S13**: The bridge SHOULD support an optional mTLS mode between Caddy and itself for defense-in-depth.

#### 5.5.5 Audit & Logging

- **R-S14**: Every proxied MCP request MUST be logged with: timestamp, source IP (from Caddy), endpoint, MCP method name, token hash (truncated), response status.
- **R-S15**: OAuth events (registration, authorization, token issuance, refresh, revocation) MUST be logged.
- **R-S16**: Logs MUST NOT contain access tokens, refresh tokens, or request/response bodies.

### 5.6 Operational Requirements

- **R-O1**: The bridge MUST run as a systemd service with proper service hardening (`NoNewPrivileges`, `ProtectSystem`, `PrivateTmp`, etc.).
- **R-O2**: The bridge MUST expose a health check endpoint (e.g., `/healthz`) that reports tsnet connectivity status.
- **R-O3**: The bridge MUST handle graceful shutdown (drain active SSE connections, close tsnet).
- **R-O4**: The bridge SHOULD expose Prometheus metrics: request count/latency per endpoint, active SSE connections, OAuth token operations, tsnet dial errors.
- **R-O5**: The bridge MUST be deployable as a single static Go binary with no runtime dependencies.

---

## 6. Tailscale ACL Configuration

The following ACL snippet demonstrates the required Tailscale policy:

```json
{
  "tagOwners": {
    "tag:mcp-proxy": ["autogroup:admin"]
  },
  "acls": [
    {
      "action": "accept",
      "src": ["tag:mcp-proxy"],
      "dst": [
        "homelab-mcp:3000",
        "freeresp:3001",
        "nas:3002"
      ]
    }
  ]
}
```

For enhanced security with Tailscale Grants (optional, for per-tool ACLs):

```json
{
  "grants": [
    {
      "src": ["tag:mcp-proxy"],
      "dst": ["tag:mcp-server"],
      "app": {
        "meltforce.org/cap/mcp-bridge": [{
          "endpoints": ["health", "infra", "files"]
        }]
      }
    }
  ]
}
```

---

## 7. Claude.ai Integration Specifics

Based on Claude.ai's documented behavior and known implementation details:

| Aspect | Claude.ai Behavior |
|---|---|
| **Transport** | Supports both SSE and Streamable HTTP |
| **Auth Spec** | Supports 2025-03-26 and 2025-06-18 MCP auth specs |
| **Client Registration** | Supports DCR (RFC 7591). CIMD support status unclear. Also supports pre-registered client ID/secret via "Advanced settings". |
| **OAuth Callback URL** | `https://claude.ai/api/mcp/auth_callback` (also allowlist `https://claude.com/api/mcp/auth_callback`) |
| **OAuth Client Name** | `Claude` |
| **Resource Indicators (RFC 8707)** | Spec requires it, but Claude.ai may not send the `resource` parameter as of early 2026. Bridge should be lenient. |
| **Token Refresh** | Supported and recommended |
| **Authless Mode** | Supported — useful for initial testing |
| **Connector Setup** | Settings → Connectors → "Add custom connector" → Enter MCP server URL → Optional OAuth Client ID/Secret |
| **IP Addresses** | Claude.ai uses specific IP ranges for MCP connections. Can be allowlisted for additional security. See Claude Help Center for current list. |

---

## 8. Endpoint Map

The bridge exposes the following HTTP endpoints on `127.0.0.1:8900` (behind Caddy):

### 8.1 OAuth / Discovery

| Method | Path | Purpose |
|---|---|---|
| GET | `/.well-known/oauth-protected-resource` | RFC 9728 Protected Resource Metadata |
| GET | `/.well-known/oauth-authorization-server` | RFC 8414 Authorization Server Metadata |
| POST | `/oauth/register` | DCR endpoint (RFC 7591) |
| GET | `/oauth/authorize` | Authorization endpoint (shows consent screen) |
| POST | `/oauth/token` | Token endpoint (code exchange, refresh) |
| POST | `/oauth/revoke` | Token revocation |

### 8.2 MCP Proxy

| Method | Path | Purpose |
|---|---|---|
| POST | `/mcp/{route}` | MCP JSON-RPC requests (Streamable HTTP) |
| GET | `/mcp/{route}` | SSE notification stream (Streamable HTTP) |
| DELETE | `/mcp/{route}` | Session termination |

### 8.3 Operations

| Method | Path | Purpose |
|---|---|---|
| GET | `/healthz` | Health check (tsnet status, upstream reachability) |
| GET | `/metrics` | Prometheus metrics |

---

## 9. Implementation Plan

### Phase 1: Minimal Viable Bridge

- Authless MCP proxy (no OAuth) with tsnet backend
- Single endpoint routing
- Streamable HTTP proxy with SSE streaming
- Basic config file
- Health check endpoint

**Validation**: Connect Claude.ai as authless custom connector → call a tool on an internal MCP server.

### Phase 2: OAuth 2.1

- Protected Resource Metadata + Authorization Server Metadata endpoints
- DCR endpoint
- Authorization Code + PKCE flow with single-operator login (passphrase)
- JWT access tokens + refresh tokens
- Token validation middleware

**Validation**: Connect Claude.ai with OAuth → complete authorization flow → call tools.

### Phase 3: Hardening

- TOTP / WebAuthn for operator login
- Rate limiting on auth endpoints
- Audit logging
- Token encryption at rest
- Systemd hardening
- Prometheus metrics

### Phase 4: Polish

- Multi-endpoint routing with hot-reload
- SSE connection draining on shutdown
- Pre-registered client support (for Claude.ai "Advanced settings")
- CIMD support
- Comprehensive test suite

---

## 10. Technology Stack

| Component | Choice | Rationale |
|---|---|---|
| Language | Go 1.23+ | tsnet is Go-native; excellent HTTP/SSE stdlib |
| Tailnet | `tailscale.com/tsnet` | Dedicated node without Tailscale installation |
| HTTP Router | `net/http` (stdlib) | Minimal dependencies, full SSE control |
| OAuth/JWT | `golang-jwt/jwt/v5` + custom | Lightweight, no framework bloat |
| Config | `gopkg.in/yaml.v3` | Standard Go YAML |
| Logging | `log/slog` (stdlib) | Structured logging, zero dependencies |
| Metrics | `prometheus/client_golang` | Industry standard |
| Password Hashing | `golang.org/x/crypto/argon2` | State of the art |
| TOTP | `pquerna/otp` | Well-maintained |
| Storage | SQLite (`modernc.org/sqlite`) | Pure Go, no CGo, encrypted at rest |

---

## 11. Open Questions

| # | Question | Impact | Notes |
|---|---|---|---|
| 1 | Does Claude.ai currently support CIMD, or only DCR + pre-registration? | Determines if CIMD implementation is useful in v1 | The MCP spec prefers CIMD, but Claude.ai docs only mention DCR |
| 2 | What specific IP ranges does Claude.ai use for MCP connections? | Enables IP allowlisting on Caddy | Documented in Claude Help Center, needs verification |
| 3 | Does Claude.ai send the `resource` parameter (RFC 8707)? | Affects token audience validation strictness | Reports suggest it doesn't as of Jan 2026 |
| 4 | Should the bridge support multiplexing (multiple internal servers exposed as a single MCP server with merged tool lists)? | Architectural decision for v2 | Would simplify Claude.ai setup (one connector instead of N) but adds complexity |
| 5 | Is there value in supporting Tailscale Grants for per-tool ACLs? | Enhanced security model | `jaxxstorm/tailscale-mcp` demonstrates this pattern |
| 6 | Should the OAuth authorization server be extractable as a separate component? | Enables using an external IdP (e.g., Authelia, Authentik) instead | Simplifies the bridge but adds deployment complexity |

---

## 12. Prior Art & References

| Project / Resource | Relevance |
|---|---|
| [jaxxstorm/tailscale-mcp-proxy](https://github.com/jaxxstorm/tailscale-mcp-proxy) | Go MCP proxy with Tailscale awareness (stdio→HTTP, opposite direction) |
| [jaxxstorm/tailscale-mcp](https://github.com/jaxxstorm/tailscale-mcp) | MCP server for Tailscale API with Grants-based ACLs |
| [boinkor-net/tsnsrv](https://github.com/boinkor-net/tsnsrv) | tsnet-based reverse proxy, good reference for tsnet lifecycle |
| [Tailscale Blog: MCP Connectivity](https://tailscale.com/blog/model-for-mcp-connectivity-lee-briggs) | Problem statement and architectural thinking from Tailscale |
| [MCP Auth Spec (draft)](https://modelcontextprotocol.io/specification/draft/basic/authorization) | Canonical OAuth requirements for MCP |
| [MCP Transport Spec](https://modelcontextprotocol.io/specification/2025-03-26/basic/transports) | Streamable HTTP transport specification |
| [Claude.ai Remote MCP Docs](https://support.claude.com/en/articles/11503834-building-custom-connectors-via-remote-mcp-servers) | Claude.ai-specific OAuth and transport requirements |
| [WorkOS MCP Auth Guide](https://workos.com/blog/mcp-auth-developer-guide) | Excellent deep dive on DCR, CIMD, JWT validation |
| [Ory Hydra + MCP](https://dev.to/this-is-learning/securing-mcp-servers-with-oauth2-ory-hydra-claude-code-chatgpt-58hm) | Production war stories with Claude.ai OAuth quirks |
| [Cloudflare Workers OAuth Provider](https://github.com/cloudflare/workers-oauth-provider) | Reference DCR implementation |
| [tsnet Go Package](https://pkg.go.dev/tailscale.com/tsnet) | API reference for tsnet |
