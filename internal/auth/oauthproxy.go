package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"
)

// ASMetadataHandler fetches the authorization server metadata from the issuer
// and rewrites token_endpoint and registration_endpoint to point to the resource
// origin (this server), so Claude.ai's requests land here and get proxied.
// The authorization_endpoint is left pointing at the issuer since the browser
// must reach tsidp directly for Tailscale identity.
func ASMetadataHandler(issuerURL, resourceOrigin string, transport http.RoundTripper, logger *slog.Logger) http.Handler {
	var (
		mu         sync.Mutex
		cached     []byte
		cachedAt   time.Time
		cacheTTL   = 1 * time.Hour
		metadataEP = issuerURL + "/.well-known/oauth-authorization-server"
	)

	client := &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if cached != nil && time.Since(cachedAt) < cacheTTL {
			body := cached
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "public, max-age=3600")
			w.Write(body)
			return
		}
		mu.Unlock()

		resp, err := client.Get(metadataEP)
		if err != nil {
			logger.Error("failed to fetch AS metadata", "url", metadataEP, "error", err)
			http.Error(w, "Bad Gateway", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit
		if err != nil {
			logger.Error("failed to read AS metadata response", "error", err)
			http.Error(w, "Bad Gateway", http.StatusBadGateway)
			return
		}
		if resp.StatusCode != http.StatusOK {
			logger.Error("AS metadata fetch failed", "status", resp.StatusCode, "body", string(body))
			http.Error(w, "Bad Gateway", http.StatusBadGateway)
			return
		}

		var metadata map[string]any
		if err := json.Unmarshal(body, &metadata); err != nil {
			logger.Error("failed to parse AS metadata", "error", err)
			http.Error(w, "Bad Gateway", http.StatusBadGateway)
			return
		}

		// Rewrite endpoints to point to this server
		metadata["token_endpoint"] = resourceOrigin + "/token"
		metadata["registration_endpoint"] = resourceOrigin + "/register"

		rewritten, err := json.Marshal(metadata)
		if err != nil {
			logger.Error("failed to marshal rewritten AS metadata", "error", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		mu.Lock()
		cached = rewritten
		cachedAt = time.Now()
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Write(rewritten)
	})
}

// OAuthProxyHandler creates a reverse proxy that forwards requests to the
// given target URL using the provided transport (typically tsnet).
// Unlike proxy.NewHandler, it does NOT strip the Authorization header,
// which is needed for client_secret_basic auth on /token and /register.
func OAuthProxyHandler(target *url.URL, transport http.RoundTripper, logger *slog.Logger) http.Handler {
	rp := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.URL.Path = target.Path
			r.Out.URL.RawPath = target.RawPath
			r.Out.Host = target.Host
			r.SetXForwarded()
			// Deliberately NOT stripping Authorization — needed for client_secret_basic
		},
		Transport:     transport,
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.Error("oauth proxy error",
				"method", r.Method,
				"path", r.URL.Path,
				"target", target.String(),
				"error", err,
			)
			http.Error(w, "Bad Gateway", http.StatusBadGateway)
		},
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.Info("proxying oauth request",
			"method", r.Method,
			"path", r.URL.Path,
			"target", target.String(),
		)
		rp.ServeHTTP(w, r)
	})
}

// OAuthAuthorizeRedirectHandler returns a 302 redirect to the issuer's
// /authorize endpoint, preserving all query parameters. The browser must
// reach tsidp directly for Tailscale identity verification.
func OAuthAuthorizeRedirectHandler(issuerURL string, logger *slog.Logger) http.Handler {
	authorizeBase := issuerURL + "/authorize"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := authorizeBase
		if r.URL.RawQuery != "" {
			target = fmt.Sprintf("%s?%s", authorizeBase, r.URL.RawQuery)
		}
		logger.Info("redirecting to authorization server",
			"target", target,
		)
		http.Redirect(w, r, target, http.StatusFound)
	})
}
