# tsmcp - MCP Tailnet Bridge

## Project
- Go reverse proxy exposing private MCP servers on Tailscale to Claude.ai
- Module: `github.com/meltforce/tsmcp`
- Repo: `github.com/meltforce/tsmcp` (private)
- Go 1.25+ (upgraded by tailscale.com dependency)
- Key deps: `tailscale.com` (tsnet), `gopkg.in/yaml.v3`, `golang-jwt/jwt/v5`, `MicahParks/keyfunc/v3`

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

## Phase 2 (Partial — OAuth discovery works, token validation needs rework)
- Optional auth via `auth:` config section (omit for authless mode)
- RFC 9728 `/.well-known/oauth-protected-resource` metadata endpoint — working, correctly returns resource origin and authorization server
- OAuth discovery chain works end-to-end: Claude.ai discovers tsidp, redirects user, completes authorization, gets token
- **Blocker**: tsidp issues opaque access tokens, not JWTs. Current JWKS-based JWT validation fails with "token contains an invalid number of segments". Need to replace with token introspection (see Phase 3).
- Per-handler auth wrapping: `/healthz` and `/.well-known/*` always unauthenticated
- 401 responses include `WWW-Authenticate: Bearer resource_metadata="<url>"` per MCP spec
- 57 unit tests, all passing

### Auth package (`internal/auth/`)
- `MetadataHandler(resource, authorizationServers)` — serves RFC 9728 JSON
- `NewJWTValidator(ctx, jwksURL, issuer, audience, resourceMetadataURL, transport, logger)` — creates validator with background JWKS refresh; transport routes fetches through tsnet (nil = default HTTP client, used in tests)
- `(*JWTValidator).Middleware()` — returns `func(http.Handler) http.Handler` for per-route wrapping
- `(*JWTValidator).Close()` — cancels background refresh goroutine via context
- `ClaimsFromContext(ctx)` — retrieves `jwt.Claims` set by middleware

### Auth config (`config.AuthConfig`)
- `Auth *AuthConfig` in `Config` — pointer, nil when absent = authless mode
- Fields: `issuer`, `audience`, `jwks_url`, `resource_metadata_url` (all required when present)

### Server wiring (`internal/server/`)
- `server.New()` signature: `(cfg, transport, checker, jwtValidator *auth.JWTValidator, logger)`
- Metadata route registered only when `cfg.Auth != nil`; `resource` field derived from metadata URL origin
- MCP handlers wrapped with `jwtValidator.Middleware()` only when validator is not nil
- `/healthz` and `/.well-known/*` never wrapped — always unauthenticated

## Phase 3 (Next — Token Introspection)
tsidp issues opaque access tokens, so local JWT validation won't work. Replace with OAuth 2.0 Token Introspection (RFC 7662).

### What needs to change
- **New**: `TokenIntrospector` in `internal/auth/` — calls tsidp's `/introspect` endpoint to validate opaque tokens
  - tsidp introspection endpoint: `https://idp.leo-royal.ts.net/introspect`
  - tsidp supports `client_secret_post` and `client_secret_basic` auth methods
  - Must use tsnet transport (same reason as JWKS — tsidp resolves to Tailscale IP)
  - Should cache introspection results briefly to avoid per-request round-trips
- **Replace**: `JWTValidator` middleware with introspection-based middleware
- **Config**: replace `jwks_url` with `introspection_endpoint` and add `client_id`/`client_secret` for authenticating introspection requests
- **Remove or keep**: `keyfunc/v3` and JWKS code — can be removed if JWT validation is fully replaced
- **Keep**: `MetadataHandler`, `ClaimsFromContext`, middleware pattern, transport plumbing — all still needed

### Key decisions to make
- Client credentials for introspection: tsidp requires the client to authenticate when calling `/introspect`. Need to register a client with tsidp or use existing credentials.
- Caching strategy: introspect every request vs. cache by token hash with short TTL
- Whether to also validate the ID token (which IS a JWT) as a secondary check

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
- **Blocked**: Auth rejected — need token introspection (Phase 3) before enabling auth in production
- **Container currently stopped** — restart after Phase 3 is implemented
- SSH: `root@nihilist`; container: `tsmcp`

## Gotchas
- Go 1.25 ServeMux: `{$}` must be its own path segment after `/`, can't append to non-slash path. Paths without trailing slash already match exactly — just omit `{$}`.
- tailscale.com pulls in large dep tree and forced Go 1.25+
- Docker uses `proxy-net` external network — container binds 0.0.0.0:8900, Caddy reaches it by container name `tsmcp`
- Listen validation allows loopback (127.0.0.1) and unspecified (0.0.0.0, ::) but rejects arbitrary IPs
- `keyfunc/v3`: `Keyfunc` is an interface with no `Cancel()` method — stop background refresh by cancelling the context passed to `NewDefaultOverrideCtx`
- `keyfunc/v3`: `NewDefaultOverrideCtx` does the initial JWKS fetch synchronously — if the JWKS URL is unreachable at startup, `NewJWTValidator` returns an error
- JWKS endpoint (tsidp) resolves to a Tailscale IP — inside Docker with userspace tsnet, Go's default HTTP client can't reach it. `NewJWTValidator` takes the tsnet transport so JWKS fetches dial through tsnet, same as proxy requests.
- tsidp issues **opaque access tokens**, not JWTs — local JWT parsing fails. Token introspection is required.
- tsidp (`idp.leo-royal.ts.net`) is publicly reachable via Tailscale Funnel
- RFC 9728 `resource` field must be the server origin (e.g. `https://mcp.meltforce.net`), not the metadata URL path — Claude.ai uses it as the base for OAuth endpoint discovery

## Structure
```
internal/
  config/     — YAML config loading + validation (AuthConfig, ServerConfig, TailnetConfig, EndpointConfig)
  auth/       — JWT middleware + RFC 9728 metadata handler
  proxy/      — Reverse proxy handler (SSE-aware) + Tailnet transport
  tsbridge/   — Tailscale network bridge (tsnet)
  health/     — Health check endpoint (/healthz)
  server/     — HTTP server assembly, middleware (origin validation, request logging)
```
