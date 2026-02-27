# tsmcp - MCP Tailnet Bridge

## Project
- Go reverse proxy exposing private MCP servers on Tailscale to Claude.ai
- Module: `github.com/meltforce/tsmcp`
- Repo: `github.com/meltforce/tsmcp` (private)
- Go 1.25+ (upgraded by tailscale.com dependency)
- Key deps: `tailscale.com` (tsnet), `gopkg.in/yaml.v3`

## Architecture
- **Pure resource server** — validates JWTs from tsidp, doesn't issue tokens
- Single FQDN with path-based routing, each path = separate Claude connector
- tsnet for outbound-only Tailnet dialing (never listens on Tailnet)
- `httputil.ReverseProxy` auto-detects SSE and flushes; no custom streaming code

## Phase 1 (Complete)
- Authless proxy: POST/GET/DELETE per MCP Streamable HTTP spec
- SSE streaming with `FlushInterval: -1`, `WriteTimeout: 0`
- YAML config with loopback/unspecified-only validation
- Origin header validation, structured slog/JSON logging
- Health check (`/healthz`) with tsnet readiness
- 29 unit tests, all passing

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
7. Add as authless custom connector in Claude.ai

## Phase 2 (Planned)
- OAuth: `/.well-known/oauth-protected-resource` (RFC 9728) pointing at tsidp
- JWT validation middleware (JWKS from tsidp, validate aud/exp/iss)
- New dep: `golang-jwt/jwt/v5`

## Gotchas
- Go 1.25 ServeMux: `{$}` must be its own path segment after `/`, can't append to non-slash path. Paths without trailing slash already match exactly — just omit `{$}`.
- tailscale.com pulls in large dep tree and forced Go 1.25+
- Docker uses `proxy-net` external network — container binds 0.0.0.0:8900, Caddy reaches it by container name `tsmcp`
- Listen validation allows loopback (127.0.0.1) and unspecified (0.0.0.0, ::) but rejects arbitrary IPs

## Structure
See `internal/{config,proxy,tsbridge,health,server}/` — each with `_test.go`
