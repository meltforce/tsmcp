package server

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

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

	srv, err := New(cfg, transport, checker, slog.Default())
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

	srv, err := New(cfg, transport, checker, slog.Default())
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

	srv, err := New(cfg, transport, checker, slog.Default())
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

	srv, err := New(cfg, transport, checker, slog.Default())
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

	srv, err := New(cfg, transport, checker, slog.Default())
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
