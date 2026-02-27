package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/meltforce/tsmcp/internal/auth"
	"github.com/meltforce/tsmcp/internal/config"
	"github.com/meltforce/tsmcp/internal/proxy"
)

type mockHealthChecker struct {
	ready bool
}

func (m *mockHealthChecker) Ready() bool { return m.ready }

func testConfig() *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Listen:         "127.0.0.1:0",
			AllowedOrigins: []string{"https://claude.ai"},
		},
		Tailnet: config.TailnetConfig{
			Hostname:   "test",
			StateDir:   "/tmp/test",
			AuthkeyEnv: "TS_AUTHKEY",
		},
		Endpoints: []config.EndpointConfig{
			{
				Path:        "/mcp/test",
				Target:      "http://localhost:9999/mcp",
				Description: "Test endpoint",
			},
		},
	}
}

func TestServerCreation(t *testing.T) {
	cfg := testConfig()
	transport := proxy.NewTailnetTransport(proxy.NewDirectDialer())
	checker := &mockHealthChecker{ready: true}

	srv, err := New(cfg, transport, checker, nil, slog.Default())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if srv.Addr != "127.0.0.1:0" {
		t.Errorf("addr = %q, want 127.0.0.1:0", srv.Addr)
	}
	if srv.WriteTimeout != 0 {
		t.Errorf("write timeout = %v, want 0", srv.WriteTimeout)
	}
}

func TestHealthzEndpoint(t *testing.T) {
	cfg := testConfig()
	transport := proxy.NewTailnetTransport(proxy.NewDirectDialer())
	checker := &mockHealthChecker{ready: true}

	srv, err := New(cfg, transport, checker, nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"status":"ok"`) {
		t.Errorf("body = %q, want ok status", w.Body.String())
	}
}

func TestMCPEndpointRouting(t *testing.T) {
	// Start a fake upstream
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer upstream.Close()

	upstreamURL, _ := url.Parse(upstream.URL)

	cfg := testConfig()
	cfg.Endpoints[0].Target = upstream.URL

	transport := proxy.NewTailnetTransport(proxy.NewDirectDialer())
	checker := &mockHealthChecker{ready: true}

	srv, err := New(cfg, transport, checker, nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	// POST to the endpoint should proxy
	req := httptest.NewRequest(http.MethodPost, "/mcp/test", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("POST status = %d, want 200", w.Code)
	}

	// Unknown path should 404
	req = httptest.NewRequest(http.MethodPost, "/mcp/unknown", strings.NewReader(`{}`))
	w = httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("unknown path status = %d, want 404", w.Code)
	}

	_ = upstreamURL
}

func TestOriginValidationIntegration(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := testConfig()
	cfg.Endpoints[0].Target = upstream.URL

	transport := proxy.NewTailnetTransport(proxy.NewDirectDialer())
	checker := &mockHealthChecker{ready: true}

	srv, err := New(cfg, transport, checker, nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	// Allowed origin
	req := httptest.NewRequest(http.MethodPost, "/mcp/test", strings.NewReader(`{}`))
	req.Header.Set("Origin", "https://claude.ai")
	w := httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("allowed origin: status = %d, want 200", w.Code)
	}

	// Rejected origin
	req = httptest.NewRequest(http.MethodPost, "/mcp/test", strings.NewReader(`{}`))
	req.Header.Set("Origin", "https://evil.com")
	w = httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("rejected origin: status = %d, want 403", w.Code)
	}
}

func TestDisabledEndpoint(t *testing.T) {
	disabled := false
	cfg := testConfig()
	cfg.Endpoints[0].Enabled = &disabled

	transport := proxy.NewTailnetTransport(proxy.NewDirectDialer())
	checker := &mockHealthChecker{ready: true}

	srv, err := New(cfg, transport, checker, nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/mcp/test", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("disabled endpoint: status = %d, want 404", w.Code)
	}
}

// --- Auth integration tests ---

func setupTestJWKS(t *testing.T) (jwksURL string, key *rsa.PrivateKey, kid string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating RSA key: %v", err)
	}

	n := base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes())

	jwks := map[string]any{
		"keys": []map[string]any{{
			"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "test-key-1",
			"n": n, "e": e,
		}},
	}
	jwksJSON, _ := json.Marshal(jwks)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(jwksJSON)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, key, "test-key-1"
}

func signToken(t *testing.T, key *rsa.PrivateKey, kid string, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = kid
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("signing token: %v", err)
	}
	return signed
}

const (
	testIssuer   = "https://idp.example.com"
	testAudience = "https://mcp.example.com"
	testMetadata = "https://mcp.example.com/.well-known/oauth-protected-resource"
)

func testAuthConfig() *config.AuthConfig {
	return &config.AuthConfig{
		Issuer:              testIssuer,
		Audience:            testAudience,
		JWKSURL:             "https://placeholder", // overridden per-test
		ResourceMetadataURL: testMetadata,
	}
}

func TestMetadataEndpointRegistered(t *testing.T) {
	cfg := testConfig()
	cfg.Auth = testAuthConfig()

	transport := proxy.NewTailnetTransport(proxy.NewDirectDialer())
	checker := &mockHealthChecker{ready: true}

	srv, err := New(cfg, transport, checker, nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource", nil)
	w := httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got["resource"] != testMetadata {
		t.Errorf("resource = %v", got["resource"])
	}
}

func TestMetadataEndpointAbsentWithoutAuth(t *testing.T) {
	cfg := testConfig()
	// Auth is nil

	transport := proxy.NewTailnetTransport(proxy.NewDirectDialer())
	checker := &mockHealthChecker{ready: true}

	srv, err := New(cfg, transport, checker, nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource", nil)
	w := httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestHealthzBypassesAuth(t *testing.T) {
	jwksURL, _, _ := setupTestJWKS(t)
	cfg := testConfig()
	cfg.Auth = testAuthConfig()
	cfg.Auth.JWKSURL = jwksURL

	v, err := auth.NewJWTValidator(context.Background(), jwksURL, testIssuer, testAudience, testMetadata, nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()

	transport := proxy.NewTailnetTransport(proxy.NewDirectDialer())
	checker := &mockHealthChecker{ready: true}

	srv, err := New(cfg, transport, checker, v, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	// No token — healthz should still return 200
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestMCPEndpointRequiresAuth(t *testing.T) {
	jwksURL, _, _ := setupTestJWKS(t)
	cfg := testConfig()
	cfg.Auth = testAuthConfig()
	cfg.Auth.JWKSURL = jwksURL

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	cfg.Endpoints[0].Target = upstream.URL

	v, err := auth.NewJWTValidator(context.Background(), jwksURL, testIssuer, testAudience, testMetadata, nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()

	transport := proxy.NewTailnetTransport(proxy.NewDirectDialer())
	checker := &mockHealthChecker{ready: true}

	srv, err := New(cfg, transport, checker, v, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	// No token — should get 401
	req := httptest.NewRequest(http.MethodPost, "/mcp/test", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestMCPEndpointWithValidToken(t *testing.T) {
	jwksURL, key, kid := setupTestJWKS(t)
	cfg := testConfig()
	cfg.Auth = testAuthConfig()
	cfg.Auth.JWKSURL = jwksURL

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer upstream.Close()
	cfg.Endpoints[0].Target = upstream.URL

	v, err := auth.NewJWTValidator(context.Background(), jwksURL, testIssuer, testAudience, testMetadata, nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()

	transport := proxy.NewTailnetTransport(proxy.NewDirectDialer())
	checker := &mockHealthChecker{ready: true}

	srv, err := New(cfg, transport, checker, v, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	token := signToken(t, key, kid, jwt.MapClaims{
		"iss": testIssuer,
		"aud": testAudience,
		"exp": time.Now().Add(time.Hour).Unix(),
		"sub": "user@example.com",
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp/test", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestAuthlessModePreserved(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer upstream.Close()

	cfg := testConfig()
	cfg.Endpoints[0].Target = upstream.URL
	// Auth is nil, validator is nil

	transport := proxy.NewTailnetTransport(proxy.NewDirectDialer())
	checker := &mockHealthChecker{ready: true}

	srv, err := New(cfg, transport, checker, nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	// No token — should still work (Phase 1 behavior)
	req := httptest.NewRequest(http.MethodPost, "/mcp/test", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}
