package tsbridge

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync/atomic"
	"time"

	"github.com/meltforce/tsmcp/internal/config"
	"tailscale.com/tsnet"
)

type Bridge struct {
	server *tsnet.Server
	logger *slog.Logger
	ready  atomic.Bool
}

func New(cfg config.TailnetConfig, logger *slog.Logger) *Bridge {
	s := &tsnet.Server{
		Hostname: cfg.Hostname,
		Dir:      cfg.StateDir,
		AuthKey:  os.Getenv(cfg.AuthkeyEnv),
		Logf:     func(format string, args ...any) { logger.Debug(fmt.Sprintf(format, args...)) },
	}

	return &Bridge{
		server: s,
		logger: logger,
	}
}

func (b *Bridge) Start(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	status, err := b.server.Up(ctx)
	if err != nil {
		return fmt.Errorf("tsnet startup: %w", err)
	}

	b.ready.Store(true)

	var ips []string
	for _, addr := range status.TailscaleIPs {
		ips = append(ips, addr.String())
	}
	b.logger.Info("tsnet connected", "hostname", b.server.Hostname, "ips", ips)

	return nil
}

func (b *Bridge) Close() error {
	b.ready.Store(false)
	return b.server.Close()
}

func (b *Bridge) Ready() bool {
	return b.ready.Load()
}

func (b *Bridge) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	return b.server.Dial(ctx, network, address)
}
