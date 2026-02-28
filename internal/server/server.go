package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/meltforce/tsmcp/internal/auth"
	"github.com/meltforce/tsmcp/internal/config"
	"github.com/meltforce/tsmcp/internal/health"
	"github.com/meltforce/tsmcp/internal/proxy"
)

// New creates the HTTP server with all routes and middleware wired up.
func New(cfg *config.Config, transport http.RoundTripper, checker health.Checker, validator *auth.IntrospectionValidator, logger *slog.Logger) (*http.Server, error) {
	mux := http.NewServeMux()

	// Health check — always unauthenticated
	mux.Handle("GET /healthz", health.Handler(checker))

	// OAuth Protected Resource Metadata — only when auth is configured
	if cfg.Auth != nil {
		// resource identifier = origin of the metadata URL (strip the well-known path)
		metaURL, err := url.Parse(cfg.Auth.ResourceMetadataURL)
		if err != nil {
			return nil, fmt.Errorf("parsing resource_metadata_url: %w", err)
		}
		resource := metaURL.Scheme + "://" + metaURL.Host

		mux.Handle("GET /.well-known/oauth-protected-resource",
			auth.MetadataHandler(resource, []string{cfg.Auth.Issuer}))
	}

	// MCP endpoint routes
	for _, ep := range cfg.Endpoints {
		if !ep.IsEnabled() {
			logger.Info("endpoint disabled", "path", ep.Path)
			continue
		}

		target, err := url.Parse(ep.Target)
		if err != nil {
			return nil, fmt.Errorf("parsing target for %s: %w", ep.Path, err)
		}

		var handler http.Handler = proxy.NewHandler(target, transport, logger)

		// Wrap with auth when validator is present
		if validator != nil {
			handler = validator.Middleware()(handler)
		}

		mux.Handle("POST "+ep.Path, handler)
		mux.Handle("GET "+ep.Path, handler)
		mux.Handle("DELETE "+ep.Path, handler)

		logger.Info("endpoint registered",
			"path", ep.Path,
			"target", ep.Target,
			"description", ep.Description,
		)
	}

	// Middleware chain: RequestLogger → OriginValidator → mux
	var handler http.Handler = mux
	handler = OriginValidator(cfg.Server.AllowedOrigins, logger)(handler)
	handler = RequestLogger(logger)(handler)

	return &http.Server{
		Addr:         cfg.Server.Listen,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  120 * time.Second,
	}, nil
}
