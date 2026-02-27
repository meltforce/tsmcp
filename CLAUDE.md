# tsmcp - MCP Tailnet Bridge

## Project
- Go reverse proxy exposing private MCP servers on Tailscale to Claude.ai
- Module: `github.com/meltforce/tsmcp`
- Repo: `github.com/meltforce/tsmcp` (private)
- Go 1.25+ (upgraded by tailscale.com dependency)
- Key deps: `tailscale.com` (tsnet), `gopkg.in/yaml.v3`, `golang-jwt/jwt/v5`, `MicahParks/keyfunc/v3`

## Architecture
- **Pure resource server** — validates JWTs from tsidp, doesn't issue tokens
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

## Phase 2 (Complete)
- Optional JWT validation via `auth:` config section (omit for authless mode)
- RFC 9728 `/.well-known/oauth-protected-resource` metadata endpoint
- JWKS-based JWT validation with background key refresh (`keyfunc/v3`)
- Per-handler auth wrapping: `/healthz` and `/.well-known/*` always unauthenticated
- 401 responses include `WWW-Authenticate: Bearer resource_metadata="<url>"` per MCP spec
- Claims accessible in handlers via `auth.ClaimsFromContext(ctx)`
- Validates: issuer, audience, expiration, signature (RS256/ES256/EdDSA)
- 57 unit tests, all passing (29 Phase 1 + 28 Phase 2)

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
- Metadata route registered only when `cfg.Auth != nil`
- MCP handlers wrapped with `jwtValidator.Middleware()` only when validator is not nil
- `/healthz` and `/.well-known/*` never wrapped — always unauthenticated

## Docker & CI/CD (Complete)
- **Dockerfile**: multi-stage `golang:1.25-alpine` → `alpine:3.20`, 48MB image
- **Docker Hub**: `meltforce/tsmcp` (edge tag on push to main, versioned on release)
- **docker-compose.yml**: `proxy-net` external network (Caddy reaches container by name), tsnet state volume, hardened (read-only, cap_drop ALL, no-new-privileges)
- **Deploy workflow** (`.github/workflows/deploy.yml`): build+push → Tailscale SSH → pull+restart
- **Release workflow** (`.github/workflows/release.yml`): tag push → latest + versioned tag + GitHub release
- CI pattern matches cast2md/FreeReps: Tailscale GitHub Action with `tag:ci`, direct `ssh root@host` over Tailscale SSH (no SSH keys)
- Uses `secrets.DEPLOY_HOST` and `vars.DEPLOY_PATH` — need to be set in repo settings

## Deployment TODO (not yet done)
1. Set GitHub repo secrets/vars: `DEPLOY_HOST` (Tailscale hostname of VPS), `DEPLOY_PATH` (path to docker-compose on VPS)
2. Org secrets already shared: `DOCKERHUB_USERNAME`, `DOCKERHUB_TOKEN`, `TS_OAUTH_CLIENT_ID`, `TS_AUDIENCE`
3. ACL: `tag:ci` needs SSH access to the VPS in Tailscale ACLs (same pattern as cast2md/FreeReps deploys)
4. Create `config.yaml` on VPS with real endpoint targets (freeresp, homelab-mcp, etc.)
5. Create `TS_AUTHKEY` for the bridge's own tsnet node (separate from CI — this is the bridge joining the tailnet)
6. Add Caddy route on VPS: `mcp.meltforce.org { reverse_proxy 127.0.0.1:8900 { flush_interval -1 } }`
7. Add as custom connector in Claude.ai (authless initially, then with OAuth)

## Gotchas
- Go 1.25 ServeMux: `{$}` must be its own path segment after `/`, can't append to non-slash path. Paths without trailing slash already match exactly — just omit `{$}`.
- tailscale.com pulls in large dep tree and forced Go 1.25+
- Docker uses `proxy-net` external network — container binds 0.0.0.0:8900, Caddy reaches it by container name `tsmcp`
- Listen validation allows loopback (127.0.0.1) and unspecified (0.0.0.0, ::) but rejects arbitrary IPs
- `keyfunc/v3`: `Keyfunc` is an interface with no `Cancel()` method — stop background refresh by cancelling the context passed to `NewDefaultOverrideCtx`
- `keyfunc/v3`: `NewDefaultOverrideCtx` does the initial JWKS fetch synchronously — if the JWKS URL is unreachable at startup, `NewJWTValidator` returns an error
- JWKS endpoint (tsidp) resolves to a Tailscale IP — inside Docker with userspace tsnet, Go's default HTTP client can't reach it. `NewJWTValidator` takes the tsnet transport so JWKS fetches dial through tsnet, same as proxy requests.

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
