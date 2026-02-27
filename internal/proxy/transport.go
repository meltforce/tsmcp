package proxy

import (
	"context"
	"net"
	"net/http"
	"time"
)

// Dialer abstracts network dialing so the proxy can use tsnet or direct connections.
type Dialer interface {
	Dial(ctx context.Context, network, address string) (net.Conn, error)
}

// directDialer dials using the standard net package. Used for testing.
type directDialer struct{}

func (d *directDialer) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	var nd net.Dialer
	return nd.DialContext(ctx, network, address)
}

// NewDirectDialer returns a Dialer that uses the standard net package.
func NewDirectDialer() Dialer {
	return &directDialer{}
}

// NewTailnetTransport creates an http.RoundTripper that dials through the given Dialer.
func NewTailnetTransport(d Dialer) http.RoundTripper {
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return d.Dial(ctx, network, addr)
		},
		ForceAttemptHTTP2:   false,
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
}
