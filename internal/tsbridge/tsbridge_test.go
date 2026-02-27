package tsbridge

import (
	"log/slog"
	"testing"

	"github.com/meltforce/tsmcp/internal/config"
)

func TestNewBridge(t *testing.T) {
	cfg := config.TailnetConfig{
		Hostname:   "test-bridge",
		StateDir:   "/tmp/test-tsnet",
		AuthkeyEnv: "TS_AUTHKEY",
	}

	logger := slog.Default()
	b := New(cfg, logger)

	if b == nil {
		t.Fatal("expected non-nil bridge")
	}
	if b.server.Hostname != "test-bridge" {
		t.Errorf("hostname = %q, want test-bridge", b.server.Hostname)
	}
}

func TestReadyFlag(t *testing.T) {
	cfg := config.TailnetConfig{
		Hostname:   "test-bridge",
		StateDir:   "/tmp/test-tsnet",
		AuthkeyEnv: "TS_AUTHKEY",
	}

	b := New(cfg, slog.Default())

	if b.Ready() {
		t.Error("bridge should not be ready before Start")
	}

	// Simulate ready state
	b.ready.Store(true)
	if !b.Ready() {
		t.Error("bridge should be ready after setting flag")
	}

	b.ready.Store(false)
	if b.Ready() {
		t.Error("bridge should not be ready after clearing flag")
	}
}
