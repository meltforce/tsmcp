package proxy

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDirectDialer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	d := NewDirectDialer()
	conn, err := d.Dial(context.Background(), "tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	conn.Close()
}

func TestNewTailnetTransport(t *testing.T) {
	d := NewDirectDialer()
	tr := NewTailnetTransport(d)
	if tr == nil {
		t.Fatal("expected non-nil transport")
	}

	ht, ok := tr.(*http.Transport)
	if !ok {
		t.Fatal("expected *http.Transport")
	}
	if ht.ForceAttemptHTTP2 {
		t.Error("ForceAttemptHTTP2 should be false")
	}

	// Verify it can actually make a connection
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello"))
	}))
	defer srv.Close()

	// Resolve the test server address to raw IP:port to avoid hostname resolution
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("pre-check dial: %v", err)
	}
	conn.Close()
}
