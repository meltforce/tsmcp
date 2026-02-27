package proxy

import (
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// NewHandler creates an MCP reverse proxy handler for the given target.
func NewHandler(target *url.URL, transport http.RoundTripper, logger *slog.Logger) http.Handler {
	rp := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Host = target.Host
			r.SetXForwarded()
		},
		Transport:     transport,
		FlushInterval: -1, // flush every write — safety net for SSE
		ModifyResponse: func(resp *http.Response) error {
			ct := resp.Header.Get("Content-Type")
			if isSSE(ct) {
				resp.Header.Set("X-Accel-Buffering", "no")
				resp.Header.Set("Cache-Control", "no-cache")
			}
			logger.Info("upstream response",
				"status", resp.StatusCode,
				"content_type", ct,
				"mcp_session_id", resp.Header.Get("Mcp-Session-Id"),
			)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.Error("proxy error",
				"method", r.Method,
				"path", r.URL.Path,
				"error", err,
			)
			http.Error(w, "Bad Gateway", http.StatusBadGateway)
		},
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.Info("proxying request",
			"method", r.Method,
			"path", r.URL.Path,
			"target", target.String(),
			"mcp_session_id", r.Header.Get("Mcp-Session-Id"),
		)
		rp.ServeHTTP(w, r)
	})
}

func isSSE(contentType string) bool {
	return strings.HasPrefix(contentType, "text/event-stream")
}
