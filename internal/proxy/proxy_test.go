package proxy

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func newTestProxy(t *testing.T, upstream *httptest.Server) http.Handler {
	t.Helper()
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := NewTailnetTransport(NewDirectDialer())
	return NewHandler(target, transport, slog.Default())
}

func TestProxyJSONResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		sessionID := r.Header.Get("Mcp-Session-Id")
		w.Header().Set("Content-Type", "application/json")
		if sessionID != "" {
			w.Header().Set("Mcp-Session-Id", sessionID)
		}
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer upstream.Close()

	handler := newTestProxy(t, upstream)
	req := httptest.NewRequest(http.MethodPost, "/mcp/test", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Session-Id", "test-session-123")

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}
	if sid := w.Header().Get("Mcp-Session-Id"); sid != "test-session-123" {
		t.Errorf("mcp-session-id = %q, want test-session-123", sid)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"result"`) {
		t.Errorf("body = %q, want JSON-RPC result", body)
	}
}

func TestProxySSEResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected flusher")
		}
		w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n"))
		flusher.Flush()
	}))
	defer upstream.Close()

	handler := newTestProxy(t, upstream)
	req := httptest.NewRequest(http.MethodPost, "/mcp/test", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("content-type = %q, want text/event-stream", ct)
	}
	if xab := w.Header().Get("X-Accel-Buffering"); xab != "no" {
		t.Errorf("X-Accel-Buffering = %q, want no", xab)
	}
}

func TestProxyGETSSEStream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if accept := r.Header.Get("Accept"); !strings.Contains(accept, "text/event-stream") {
			t.Errorf("accept = %q, want text/event-stream", accept)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("event: endpoint\ndata: /mcp/test\n\n"))
	}))
	defer upstream.Close()

	handler := newTestProxy(t, upstream)
	req := httptest.NewRequest(http.MethodGet, "/mcp/test", nil)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Mcp-Session-Id", "session-456")

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestProxyDELETESession(t *testing.T) {
	var gotSessionID string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		gotSessionID = r.Header.Get("Mcp-Session-Id")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	handler := newTestProxy(t, upstream)
	req := httptest.NewRequest(http.MethodDelete, "/mcp/test", nil)
	req.Header.Set("Mcp-Session-Id", "session-to-delete")

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if gotSessionID != "session-to-delete" {
		t.Errorf("session id = %q, want session-to-delete", gotSessionID)
	}
}

func TestProxyStripsAuthorizationHeader(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer upstream.Close()

	handler := newTestProxy(t, upstream)
	req := httptest.NewRequest(http.MethodPost, "/mcp/test", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer secret-token")
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if gotAuth != "" {
		t.Errorf("upstream received Authorization = %q, want empty (should be stripped)", gotAuth)
	}
}

func TestProxyUpstreamError(t *testing.T) {
	// Use a server that immediately closes connections
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("expected hijacker")
		}
		conn, _, _ := hj.Hijack()
		conn.Close()
	}))
	defer upstream.Close()

	handler := newTestProxy(t, upstream)
	req := httptest.NewRequest(http.MethodPost, "/mcp/test", strings.NewReader(`{}`))

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", w.Code)
	}
	body, _ := io.ReadAll(w.Body)
	if !strings.Contains(string(body), "Bad Gateway") {
		t.Errorf("body = %q, want 'Bad Gateway'", body)
	}
}
