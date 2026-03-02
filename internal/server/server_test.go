package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

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
	if srv.WriteTimeout != 120*time.Second {
		t.Errorf("write timeout = %v, want 120s", srv.WriteTimeout)
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

const (
	testIssuer   = "https://idp.example.com"
	testAudience = "https://mcp.example.com"
	testMetadata = "https://mcp.example.com/.well-known/oauth-protected-resource"
	testClientID = "test-client"
	testSecret   = "test-secret"
)

func setupIntrospectionServer(t *testing.T, active bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if active {
			json.NewEncoder(w).Encode(map[string]any{
				"active":     true,
				"sub":        "user@example.com",
				"client_id":  testClientID,
				"token_type": "bearer",
				"exp":        time.Now().Add(time.Hour).Unix(),
			})
		} else {
			json.NewEncoder(w).Encode(map[string]any{"active": false})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testAuthConfig(introspectionURL string) *config.AuthConfig {
	return &config.AuthConfig{
		Issuer:              testIssuer,
		Audience:            testAudience,
		IntrospectionURL:    introspectionURL,
		ClientID:            testClientID,
		ClientSecret:        testSecret,
		ResourceMetadataURL: testMetadata,
	}
}

func TestMetadataEndpointRegistered(t *testing.T) {
	introspSrv := setupIntrospectionServer(t, true)
	cfg := testConfig()
	cfg.Auth = testAuthConfig(introspSrv.URL)

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
	if got["resource"] != "https://mcp.example.com" {
		t.Errorf("resource = %v, want https://mcp.example.com", got["resource"])
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
	introspSrv := setupIntrospectionServer(t, true)
	cfg := testConfig()
	cfg.Auth = testAuthConfig(introspSrv.URL)

	v := auth.NewIntrospectionValidator(introspSrv.URL, testClientID, testSecret, testMetadata, "", "", nil, slog.Default())
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
	introspSrv := setupIntrospectionServer(t, true)
	cfg := testConfig()
	cfg.Auth = testAuthConfig(introspSrv.URL)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	cfg.Endpoints[0].Target = upstream.URL

	v := auth.NewIntrospectionValidator(introspSrv.URL, testClientID, testSecret, testMetadata, "", "", nil, slog.Default())
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
	introspSrv := setupIntrospectionServer(t, true)
	cfg := testConfig()
	cfg.Auth = testAuthConfig(introspSrv.URL)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer upstream.Close()
	cfg.Endpoints[0].Target = upstream.URL

	v := auth.NewIntrospectionValidator(introspSrv.URL, testClientID, testSecret, testMetadata, "", "", nil, slog.Default())
	defer v.Close()

	transport := proxy.NewTailnetTransport(proxy.NewDirectDialer())
	checker := &mockHealthChecker{ready: true}

	srv, err := New(cfg, transport, checker, v, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/mcp/test", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer opaque-test-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestOAuthRoutesRegistered(t *testing.T) {
	// Mock AS metadata endpoint
	asServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 testIssuer,
			"authorization_endpoint": testIssuer + "/authorize",
			"token_endpoint":         testIssuer + "/token",
			"registration_endpoint":  testIssuer + "/register",
		})
	}))
	defer asServer.Close()

	cfg := testConfig()
	cfg.Auth = testAuthConfig(asServer.URL) // introspection URL doesn't matter here
	cfg.Auth.Issuer = asServer.URL          // point issuer at our mock

	transport := proxy.NewTailnetTransport(proxy.NewDirectDialer())
	checker := &mockHealthChecker{ready: true}

	srv, err := New(cfg, transport, checker, nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	t.Run("AS metadata", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil)
		w := httptest.NewRecorder()
		srv.Handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		var got map[string]any
		json.Unmarshal(w.Body.Bytes(), &got)
		if got["token_endpoint"] != "https://mcp.example.com/token" {
			t.Errorf("token_endpoint = %v, want rewritten to resource origin", got["token_endpoint"])
		}
	})

	t.Run("openid-configuration alias", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/.well-known/openid-configuration", nil)
		w := httptest.NewRecorder()
		srv.Handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", w.Code)
		}
	})

	t.Run("authorize redirect", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/authorize?response_type=code&client_id=test", nil)
		w := httptest.NewRecorder()
		srv.Handler.ServeHTTP(w, req)

		if w.Code != http.StatusFound {
			t.Fatalf("status = %d, want 302", w.Code)
		}
		loc := w.Header().Get("Location")
		if !strings.HasPrefix(loc, asServer.URL+"/authorize") {
			t.Errorf("Location = %q, want prefix %s/authorize", loc, asServer.URL)
		}
		if !strings.Contains(loc, "response_type=code") {
			t.Error("query params not preserved in redirect")
		}
	})
}

func TestOAuthRoutesUnauthenticated(t *testing.T) {
	// Mock AS metadata + token endpoint
	asServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			json.NewEncoder(w).Encode(map[string]any{
				"issuer":         "https://idp.example.com",
				"token_endpoint": "https://idp.example.com/token",
			})
		case "/token":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"access_token": "tok",
				"token_type":   "bearer",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer asServer.Close()

	introspSrv := setupIntrospectionServer(t, true)
	cfg := testConfig()
	cfg.Auth = testAuthConfig(introspSrv.URL)
	cfg.Auth.Issuer = asServer.URL

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	cfg.Endpoints[0].Target = upstream.URL

	v := auth.NewIntrospectionValidator(introspSrv.URL, testClientID, testSecret, testMetadata, "", "", nil, slog.Default())
	defer v.Close()

	transport := proxy.NewTailnetTransport(proxy.NewDirectDialer())
	checker := &mockHealthChecker{ready: true}

	srv, err := New(cfg, transport, checker, v, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	// OAuth endpoints should work without Bearer token (MCP endpoints require it)
	t.Run("token endpoint no auth needed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader("grant_type=authorization_code"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		srv.Handler.ServeHTTP(w, req)

		if w.Code == http.StatusUnauthorized {
			t.Error("POST /token returned 401 — should be unauthenticated")
		}
	})

	t.Run("authorize no auth needed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/authorize", nil)
		w := httptest.NewRecorder()
		srv.Handler.ServeHTTP(w, req)

		if w.Code == http.StatusUnauthorized {
			t.Error("GET /authorize returned 401 — should be unauthenticated")
		}
	})

	t.Run("MCP still requires auth", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/mcp/test", strings.NewReader(`{}`))
		w := httptest.NewRecorder()
		srv.Handler.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("POST /mcp/test status = %d, want 401", w.Code)
		}
	})
}

func TestOAuthRoutesAbsentWithoutAuth(t *testing.T) {
	cfg := testConfig()
	// Auth is nil

	transport := proxy.NewTailnetTransport(proxy.NewDirectDialer())
	checker := &mockHealthChecker{ready: true}

	srv, err := New(cfg, transport, checker, nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	routes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/.well-known/oauth-authorization-server"},
		{http.MethodGet, "/.well-known/openid-configuration"},
		{http.MethodPost, "/token"},
		{http.MethodPost, "/register"},
		{http.MethodGet, "/authorize"},
	}

	for _, rt := range routes {
		req := httptest.NewRequest(rt.method, rt.path, nil)
		w := httptest.NewRecorder()
		srv.Handler.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("%s %s: status = %d, want 404", rt.method, rt.path, w.Code)
		}
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
