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
- Auth is optional: omitting the `auth:` config section preserves authless (Phase 1) behavior

## Phase 1 (Complete)
- Authless proxy: POST/GET/DELETE per MCP Streamable HTTP spec
- SSE streaming with `FlushInterval: -1`, `WriteTimeout: 0`
- YAML config with loopback/unspecified-only validation
- Origin header validation, structured slog/JSON logging
- Health check (`/healthz`) with tsnet readiness

## Phase 2 (Complete — OAuth discovery + token introspection)
- Optional auth via `auth:` config section (omit for authless mode)
- RFC 9728 `/.well-known/oauth-protected-resource` metadata endpoint — returns resource origin and authorization server
- OAuth discovery chain works end-to-end: Claude.ai discovers tsidp, redirects user, completes authorization, gets token
- Token validation via RFC 7662 introspection: opaque tokens validated by calling tsidp's `/introspect` endpoint
- Introspection results cached (60s TTL or token exp, whichever is shorter) to avoid per-request round-trips
- Per-handler auth wrapping: `/healthz` and `/.well-known/*` always unauthenticated
- 401 responses include `WWW-Authenticate: Bearer resource_metadata="<url>"` per MCP spec

### Auth package (`internal/auth/`)
- `MetadataHandler(resource, authorizationServers)` — serves RFC 9728 JSON
- `NewIntrospectionValidator(introspectionURL, clientID, clientSecret, resourceMetadataURL, transport, logger)` — creates validator; transport routes introspection requests through tsnet (nil = default HTTP client, used in tests)
- `(*IntrospectionValidator).Middleware()` — returns `func(http.Handler) http.Handler` for per-route wrapping
- `(*IntrospectionValidator).Close()` — clears introspection cache
- `ClaimsFromContext(ctx)` — retrieves `*IntrospectionClaims` set by middleware
- `IntrospectionClaims` — struct with `Active`, `Sub`, `Aud`, `Iss`, `Scope`, `ClientID`, `Username`, `TokenType`, `Exp`, `Iat`

### Auth config (`config.AuthConfig`)
- `Auth *AuthConfig` in `Config` — pointer, nil when absent = authless mode
- Fields: `issuer`, `audience`, `introspection_url`, `client_id`, `client_secret`, `resource_metadata_url` (all required when present)

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
- CI pattern matches cast2md/FreeReps: Tailscale GitHub Action with `tag:ci`, direct `ssh root@host` over Tailscale SSH (no SSH keys)
- Uses `secrets.DEPLOY_HOST` and `vars.DEPLOY_PATH` — need to be set in repo settings

## Deployment (on nihilist VPS)
- **Done**: Docker image builds, CI/CD deploys, Caddy route at `mcp.meltforce.net`, tsnet bridge joins tailnet, Claude.ai connector configured
- **Done**: OAuth discovery flow works end-to-end (Claude.ai → resource metadata → tsidp authorize → token)
- **Next**: Register client with tsidp, configure `client_id`/`client_secret` in config, deploy with auth enabled
- SSH: `root@nihilist`; container: `tsmcp`

## Gotchas
- Go 1.25 ServeMux: `{$}` must be its own path segment after `/`, can't append to non-slash path. Paths without trailing slash already match exactly — just omit `{$}`.
- tailscale.com pulls in large dep tree and forced Go 1.25+
- Docker uses `proxy-net` external network — container binds 0.0.0.0:8900, Caddy reaches it by container name `tsmcp`
- Listen validation allows loopback (127.0.0.1) and unspecified (0.0.0.0, ::) but rejects arbitrary IPs
- tsidp issues **opaque access tokens**, not JWTs — token introspection (RFC 7662) is used instead of local JWT parsing
- Introspection endpoint (tsidp) resolves to a Tailscale IP — inside Docker with userspace tsnet, Go's default HTTP client can't reach it. `NewIntrospectionValidator` takes the tsnet transport so introspection requests dial through tsnet.
- tsidp (`idp.leo-royal.ts.net`) is publicly reachable via Tailscale Funnel
- RFC 9728 `resource` field must be the server origin (e.g. `https://mcp.meltforce.net`), not the metadata URL path — Claude.ai uses it as the base for OAuth endpoint discovery
- tsidp supports `client_secret_post` and `client_secret_basic` auth methods; we use `client_secret_basic`

## Structure
```
internal/
  config/     — YAML config loading + validation (AuthConfig, ServerConfig, TailnetConfig, EndpointConfig)
  auth/       — Token introspection middleware + RFC 9728 metadata handler
  proxy/      — Reverse proxy handler (SSE-aware) + Tailnet transport
  tsbridge/   — Tailscale network bridge (tsnet)
  health/     — Health check endpoint (/healthz)
  server/     — HTTP server assembly, middleware (origin validation, request logging)
```
