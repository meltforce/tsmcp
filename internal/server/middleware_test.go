package server

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOriginValidatorAllowed(t *testing.T) {
	mw := OriginValidator([]string{"https://claude.ai"}, slog.Default())
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	req.Header.Set("Origin", "https://claude.ai")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestOriginValidatorRejected(t *testing.T) {
	mw := OriginValidator([]string{"https://claude.ai"}, slog.Default())
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	req.Header.Set("Origin", "https://evil.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestOriginValidatorNoOrigin(t *testing.T) {
	mw := OriginValidator([]string{"https://claude.ai"}, slog.Default())
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 for missing Origin", w.Code)
	}
}

func TestOriginValidatorEmptyAllowlist(t *testing.T) {
	mw := OriginValidator(nil, slog.Default())
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	req.Header.Set("Origin", "https://anything.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 for disabled validation", w.Code)
	}
}

func TestRequestLogger(t *testing.T) {
	var called bool
	mw := RequestLogger(slog.Default())
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !called {
		t.Error("next handler was not called")
	}
}
