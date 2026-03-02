package auth

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestASMetadataHandler(t *testing.T) {
	// Mock AS metadata server
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/oauth-authorization-server" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 "https://idp.example.com",
			"authorization_endpoint": "https://idp.example.com/authorize",
			"token_endpoint":         "https://idp.example.com/token",
			"registration_endpoint":  "https://idp.example.com/register",
			"response_types_supported": []string{"code"},
		})
	}))
	defer upstream.Close()

	h := ASMetadataHandler(upstream.URL, "https://mcp.example.com", http.DefaultTransport, slog.Default())

	req := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// token_endpoint and registration_endpoint should be rewritten
	if got["token_endpoint"] != "https://mcp.example.com/token" {
		t.Errorf("token_endpoint = %v, want https://mcp.example.com/token", got["token_endpoint"])
	}
	if got["registration_endpoint"] != "https://mcp.example.com/register" {
		t.Errorf("registration_endpoint = %v, want https://mcp.example.com/register", got["registration_endpoint"])
	}
	// authorization_endpoint should stay pointing at the issuer
	if got["authorization_endpoint"] != "https://idp.example.com/authorize" {
		t.Errorf("authorization_endpoint = %v, want https://idp.example.com/authorize", got["authorization_endpoint"])
	}
	// issuer should be preserved
	if got["issuer"] != "https://idp.example.com" {
		t.Errorf("issuer = %v, want https://idp.example.com", got["issuer"])
	}
}

func TestASMetadataHandlerCaches(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":         "https://idp.example.com",
			"token_endpoint": "https://idp.example.com/token",
		})
	}))
	defer upstream.Close()

	h := ASMetadataHandler(upstream.URL, "https://mcp.example.com", http.DefaultTransport, slog.Default())

	// First request populates cache
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("first request: status = %d", w.Code)
	}

	// Second request should use cache
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("second request: status = %d", w.Code)
	}

	if calls != 1 {
		t.Errorf("upstream called %d times, want 1 (cached)", calls)
	}
}

func TestASMetadataHandlerUpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()

	h := ASMetadataHandler(upstream.URL, "https://mcp.example.com", http.DefaultTransport, slog.Default())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", w.Code)
	}
}

func TestOAuthProxyHandler(t *testing.T) {
	// Mock tsidp token endpoint
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		// Verify Authorization header is preserved
		if auth := r.Header.Get("Authorization"); auth == "" {
			t.Error("Authorization header was stripped")
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "opaque-token",
			"token_type":   "bearer",
			"received_body": string(body),
		})
	}))
	defer upstream.Close()

	target, _ := url.Parse(upstream.URL + "/token")
	h := OAuthProxyHandler(target, http.DefaultTransport, slog.Default())

	reqBody := "grant_type=authorization_code&code=abc123"
	req := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Basic Y2xpZW50OnNlY3JldA==")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got["access_token"] != "opaque-token" {
		t.Errorf("access_token = %v, want opaque-token", got["access_token"])
	}
}

func TestOAuthProxyHandlerUpstreamDown(t *testing.T) {
	// Point to a closed server
	target, _ := url.Parse("http://127.0.0.1:1/token")
	h := OAuthProxyHandler(target, http.DefaultTransport, slog.Default())

	req := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(""))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", w.Code)
	}
}

func TestOAuthAuthorizeRedirectHandler(t *testing.T) {
	h := OAuthAuthorizeRedirectHandler("https://idp.example.com", slog.Default())

	tests := []struct {
		name     string
		path     string
		wantLoc  string
	}{
		{
			name:    "no query params",
			path:    "/authorize",
			wantLoc: "https://idp.example.com/authorize",
		},
		{
			name:    "with query params",
			path:    "/authorize?response_type=code&client_id=abc&redirect_uri=https%3A%2F%2Fexample.com%2Fcb",
			wantLoc: "https://idp.example.com/authorize?response_type=code&client_id=abc&redirect_uri=https%3A%2F%2Fexample.com%2Fcb",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)

			if w.Code != http.StatusFound {
				t.Errorf("status = %d, want 302", w.Code)
			}
			if loc := w.Header().Get("Location"); loc != tt.wantLoc {
				t.Errorf("Location = %q, want %q", loc, tt.wantLoc)
			}
		})
	}
}
