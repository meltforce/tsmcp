package health

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type mockChecker struct {
	ready bool
}

func (m *mockChecker) Ready() bool { return m.ready }

func TestHealthy(t *testing.T) {
	h := Handler(&mockChecker{ready: true})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	var resp response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("status = %q, want ok", resp.Status)
	}
	if !resp.TsnetConnected {
		t.Error("tsnet_connected should be true")
	}
}

func TestUnhealthy(t *testing.T) {
	h := Handler(&mockChecker{ready: false})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}

	var resp response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Status != "degraded" {
		t.Errorf("status = %q, want degraded", resp.Status)
	}
	if resp.TsnetConnected {
		t.Error("tsnet_connected should be false")
	}
}
