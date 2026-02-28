# tsmcp - MCP Tailnet Bridge

## Project
- Go reverse proxy exposing private MCP servers on Tailscale to Claude.ai
- Module: `github.com/meltforce/tsmcp`
- Repo: `github.com/meltforce/tsmcp` (private)
- Go 1.25+ (upgraded by tailscale.com dependency)
- Key deps: `tailscale.com` (tsnet), `gopkg.in/yaml.v3`

## Architecture
- **Pure resource server** — validates tokens from tsidp, doesn't issue tokens
- Single FQDN with path-based routing, each path = separate Claude connector
- tsnet for outbound-only Tailnet dialing (never listens on Tailnet)
- `httputil.ReverseProxy` auto-detects SSE and flushes; no custom streaming code
- Auth is optional: omitting the `auth:` config section preserves authless behavior

## Auth (Complete — OAuth discovery + token introspection + Claude.ai integration)
- Full MCP auth spec implemented and working with Claude.ai
- RFC 9728 `/.well-known/oauth-protected-resource` metadata endpoint — returns resource origin and authorization server
- Claude.ai discovers tsidp, redirects user's browser to authorize (Tailscale identity), gets opaque token
- Token validation via RFC 7662 introspection over tsnet: opaque tokens validated by calling tsidp's `/introspect` endpoint
- Introspection results cached (60s TTL or token exp, whichever is shorter)
- Per-handler auth wrapping: `/healthz` and `/.well-known/*` always unauthenticated
- 401 responses include `WWW-Authenticate: Bearer resource_metadata="<url>"` per MCP spec

### Auth security model
- tsidp rejects DCR (dynamic client registration) over Funnel — clients must be pre-registered from a tailnet node
- The `/authorize` endpoint requires Tailscale identity — user's browser must be on the tailnet
- Introspection goes through tsnet — ACLs control which nodes can validate tokens
- A stranger cannot complete the OAuth flow even though tsidp is publicly reachable via Funnel

### Auth package (`internal/auth/`)
- `MetadataHandler(resource, authorizationServers)` — serves RFC 9728 JSON
- `NewIntrospectionValidator(introspectionURL, clientID, clientSecret, resourceMetadataURL, transport, logger)` — creates validator; transport MUST be tsnet (tsidp resolves to Tailscale IP, unreachable from Docker default networking)
- `(*IntrospectionValidator).Middleware()` — returns `func(http.Handler) http.Handler` for per-route wrapping
- `(*IntrospectionValidator).Close()` — clears introspection cache
- `ClaimsFromContext(ctx)` — retrieves `*IntrospectionClaims` set by middleware
- `IntrospectionClaims` — struct with `Active`, `Sub`, `Aud` (Audience type: string or array), `Iss`, `Scope`, `ClientID`, `Username`, `TokenType`, `Exp`, `Iat`
- `Audience` — custom `[]string` type with `UnmarshalJSON` handling both string and array forms (tsidp returns array)

### Auth config (`config.AuthConfig`)
- `Auth *AuthConfig` in `Config` — pointer, nil when absent = authless mode
- Required fields: `issuer`, `audience`, `introspection_url`, `resource_metadata_url`
- Optional fields: `client_id`, `client_secret` (for introspection auth; tsidp allows unauthenticated introspection)

### Server wiring (`internal/server/`)
- `server.New()` signature: `(cfg, transport, checker, validator *auth.IntrospectionValidator, logger)`
- Metadata route registered only when `cfg.Auth != nil`; `resource` field derived from metadata URL origin
- MCP handlers wrapped with `validator.Middleware()` only when validator is not nil
- `/healthz` and `/.well-known/*` never wrapped — always unauthenticated

## Docker & CI/CD (Complete)
- **Dockerfile**: multi-stage `golang:1.25-alpine` → `alpine:3.20`, 48MB image
- **Docker Hub**: `meltforce/tsmcp` (edge tag on push to main, versioned on release)
- **docker-compose.yml**: `proxy-net` external network (Caddy reaches container by name), tsnet state volume, hardened (read-only, cap_drop ALL, no-new-privileges)
- **Deploy workflow** (`.github/workflows/deploy.yml`): build+push → Tailscale SSH → pull+restart
- **Release workflow** (`.github/workflows/release.yml`): tag push → latest + versioned tag + GitHub release
- CI pattern: Tailscale GitHub Action with `tag:ci`, direct `ssh root@host` over Tailscale SSH (no SSH keys)

## Deployment (on nihilist VPS)
- Docker image builds, CI/CD deploys, Caddy route at `mcp.meltforce.net`
- tsnet bridge joins tailnet as `mcp-bridge`, auth enabled with tsidp introspection
- Claude.ai connector configured and working with OAuth
- SSH: `root@nihilist`; container: `tsmcp`
- Config: `/opt/docker/stacks/tsmcp/config.yaml`

## Gotchas
- Go 1.25 ServeMux: `{$}` must be its own path segment after `/`, can't append to non-slash path. Paths without trailing slash already match exactly — just omit `{$}`.
- tailscale.com pulls in large dep tree and forced Go 1.25+
- Docker uses `proxy-net` external network — container binds 0.0.0.0:8900, Caddy reaches it by container name `tsmcp`
- Listen validation allows loopback (127.0.0.1) and unspecified (0.0.0.0, ::) but rejects arbitrary IPs
- tsidp issues **opaque access tokens**, not JWTs — token introspection (RFC 7662) is used instead of local JWT parsing
- **Introspection MUST use tsnet transport** — tsidp resolves to a Tailscale IP (`100.x.x.x`), unreachable from Docker's network. Pass the tsnet transport to `NewIntrospectionValidator`, never `nil` in production.
- tsidp `aud` claim is a JSON array, not a string — the `Audience` type handles both forms
- tsidp is publicly reachable via Tailscale Funnel (public DNS → Tailscale edge IP, MagicDNS → Tailscale IP) but rejects DCR over Funnel
- RFC 9728 `resource` field must be the server origin (e.g. `https://mcp.meltforce.net`), not the metadata URL path
- tsidp supports `client_secret_post` and `client_secret_basic` auth methods; we use `client_secret_basic`
- ACL rule required: bridge node → tsidp on tcp:443

## Structure
```
internal/
  config/     — YAML config loading + validation (AuthConfig, ServerConfig, TailnetConfig, EndpointConfig)
  auth/       — Token introspection middleware + RFC 9728 metadata handler + Audience type
  proxy/      — Reverse proxy handler (SSE-aware) + Tailnet transport
  tsbridge/   — Tailscale network bridge (tsnet)
  health/     — Health check endpoint (/healthz)
  server/     — HTTP server assembly, middleware (origin validation, request logging)
```
