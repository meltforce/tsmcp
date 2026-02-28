package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/meltforce/tsmcp/internal/auth"
	"github.com/meltforce/tsmcp/internal/config"
	"github.com/meltforce/tsmcp/internal/proxy"
	"github.com/meltforce/tsmcp/internal/server"
	"github.com/meltforce/tsmcp/internal/tsbridge"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	debug := flag.Bool("debug", false, "enable debug logging")
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, *configPath, logger); err != nil {
		logger.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, configPath string, logger *slog.Logger) error {
	// Load config
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	logger.Info("config loaded", "listen", cfg.Server.Listen, "endpoints", len(cfg.Endpoints))

	// Start tsnet bridge
	bridge := tsbridge.New(cfg.Tailnet, logger)
	if err := bridge.Start(ctx); err != nil {
		return err
	}
	defer bridge.Close()

	// Create transport using tsnet dialer
	transport := proxy.NewTailnetTransport(bridge)

	// Create introspection validator if auth is configured
	var validator *auth.IntrospectionValidator
	if cfg.Auth != nil {
		validator = auth.NewIntrospectionValidator(
			cfg.Auth.IntrospectionURL, cfg.Auth.ClientID, cfg.Auth.ClientSecret,
			cfg.Auth.ResourceMetadataURL, cfg.Auth.Audience, cfg.Auth.Issuer, transport, logger,
		)
		defer validator.Close()
		logger.Info("token introspection enabled", "issuer", cfg.Auth.Issuer, "introspection_url", cfg.Auth.IntrospectionURL)
	}

	// Assemble HTTP server
	srv, err := server.New(cfg, transport, bridge, validator, logger)
	if err != nil {
		return err
	}

	// Start HTTP server
	errCh := make(chan error, 1)
	go func() {
		logger.Info("server starting", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	// Wait for shutdown signal or server error
	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-errCh:
		if err != nil {
			return err
		}
	}

	// Graceful shutdown
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	logger.Info("shutting down server")
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}

	logger.Info("shutdown complete")
	return nil
}
