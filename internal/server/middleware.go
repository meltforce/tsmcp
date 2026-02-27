package server

import (
	"log/slog"
	"net/http"
	"time"
)

// OriginValidator returns middleware that rejects requests with a mismatched Origin header.
// Requests without an Origin header are allowed (server-to-server, curl).
// An empty allowedOrigins list disables validation (dev mode).
func OriginValidator(allowedOrigins []string, logger *slog.Logger) func(http.Handler) http.Handler {
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		allowed[o] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")

			// No Origin header → allow (server-to-server, curl)
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}

			// Empty allowlist → validation disabled (dev mode)
			if len(allowed) == 0 {
				next.ServeHTTP(w, r)
				return
			}

			if !allowed[origin] {
				logger.Warn("origin rejected",
					"origin", origin,
					"remote_addr", r.RemoteAddr,
				)
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// RequestLogger returns middleware that logs every request.
func RequestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			next.ServeHTTP(w, r)
			logger.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"remote_addr", r.RemoteAddr,
				"user_agent", r.UserAgent(),
				"duration", time.Since(start).String(),
			)
		})
	}
}
