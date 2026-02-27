package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMetadataResponse(t *testing.T) {
	h := MetadataHandler("https://mcp.example.com", []string{"https://idp.example.com"})
	req := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var got protectedResourceMetadata
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got.Resource != "https://mcp.example.com" {
		t.Errorf("resource = %q", got.Resource)
	}
	if len(got.AuthorizationServers) != 1 || got.AuthorizationServers[0] != "https://idp.example.com" {
		t.Errorf("authorization_servers = %v", got.AuthorizationServers)
	}
	if len(got.BearerMethodsSupported) != 1 || got.BearerMethodsSupported[0] != "header" {
		t.Errorf("bearer_methods_supported = %v", got.BearerMethodsSupported)
	}
}

func TestMetadataContentType(t *testing.T) {
	h := MetadataHandler("https://mcp.example.com", []string{"https://idp.example.com"})
	req := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestMetadataCacheControl(t *testing.T) {
	h := MetadataHandler("https://mcp.example.com", []string{"https://idp.example.com"})
	req := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if cc := w.Header().Get("Cache-Control"); cc != "public, max-age=3600" {
		t.Errorf("Cache-Control = %q, want public, max-age=3600", cc)
	}
}
