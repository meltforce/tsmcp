package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadValidConfig(t *testing.T) {
	path := writeConfig(t, `
server:
  listen: "127.0.0.1:8900"
  allowed_origins:
    - "https://claude.ai"
tailnet:
  hostname: "mcp-bridge"
  state_dir: "/tmp/tsnet"
  authkey_env: "TS_AUTHKEY"
endpoints:
  - path: "/mcp/health"
    target: "http://freeresp:3001/mcp"
    description: "Health Data MCP Server"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Server.Listen != "127.0.0.1:8900" {
		t.Errorf("listen = %q, want 127.0.0.1:8900", cfg.Server.Listen)
	}
	if len(cfg.Endpoints) != 1 {
		t.Errorf("endpoints count = %d, want 1", len(cfg.Endpoints))
	}
	if !cfg.Endpoints[0].IsEnabled() {
		t.Error("endpoint should be enabled by default")
	}
}

func TestRejectNonLoopback(t *testing.T) {
	path := writeConfig(t, `
server:
  listen: "0.0.0.0:8900"
tailnet:
  hostname: "mcp-bridge"
  state_dir: "/tmp/tsnet"
  authkey_env: "TS_AUTHKEY"
endpoints:
  - path: "/mcp/test"
    target: "http://test:3000/mcp"
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for non-loopback address")
	}
}

func TestRejectMissingEndpoints(t *testing.T) {
	path := writeConfig(t, `
server:
  listen: "127.0.0.1:8900"
tailnet:
  hostname: "mcp-bridge"
  state_dir: "/tmp/tsnet"
  authkey_env: "TS_AUTHKEY"
endpoints: []
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for missing endpoints")
	}
}

func TestRejectInvalidTargetURL(t *testing.T) {
	path := writeConfig(t, `
server:
  listen: "127.0.0.1:8900"
tailnet:
  hostname: "mcp-bridge"
  state_dir: "/tmp/tsnet"
  authkey_env: "TS_AUTHKEY"
endpoints:
  - path: "/mcp/test"
    target: "not-a-url"
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid target URL")
	}
}

func TestRejectDuplicatePaths(t *testing.T) {
	path := writeConfig(t, `
server:
  listen: "127.0.0.1:8900"
tailnet:
  hostname: "mcp-bridge"
  state_dir: "/tmp/tsnet"
  authkey_env: "TS_AUTHKEY"
endpoints:
  - path: "/mcp/test"
    target: "http://test:3000/mcp"
  - path: "/mcp/test"
    target: "http://test2:3000/mcp"
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for duplicate paths")
	}
}

func TestEndpointEnabled(t *testing.T) {
	f := false
	tr := true

	ep := EndpointConfig{Enabled: nil}
	if !ep.IsEnabled() {
		t.Error("nil Enabled should default to true")
	}

	ep.Enabled = &f
	if ep.IsEnabled() {
		t.Error("false Enabled should be false")
	}

	ep.Enabled = &tr
	if !ep.IsEnabled() {
		t.Error("true Enabled should be true")
	}
}

func TestRejectMissingTailnetFields(t *testing.T) {
	tests := []struct {
		name   string
		config string
	}{
		{
			name: "missing hostname",
			config: `
server:
  listen: "127.0.0.1:8900"
tailnet:
  state_dir: "/tmp/tsnet"
  authkey_env: "TS_AUTHKEY"
endpoints:
  - path: "/mcp/test"
    target: "http://test:3000/mcp"
`,
		},
		{
			name: "missing state_dir",
			config: `
server:
  listen: "127.0.0.1:8900"
tailnet:
  hostname: "mcp-bridge"
  authkey_env: "TS_AUTHKEY"
endpoints:
  - path: "/mcp/test"
    target: "http://test:3000/mcp"
`,
		},
		{
			name: "missing authkey_env",
			config: `
server:
  listen: "127.0.0.1:8900"
tailnet:
  hostname: "mcp-bridge"
  state_dir: "/tmp/tsnet"
endpoints:
  - path: "/mcp/test"
    target: "http://test:3000/mcp"
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfig(t, tt.config)
			_, err := Load(path)
			if err == nil {
				t.Fatalf("expected error for %s", tt.name)
			}
		})
	}
}

func TestRejectNonIPHost(t *testing.T) {
	path := writeConfig(t, `
server:
  listen: "localhost:8900"
tailnet:
  hostname: "mcp-bridge"
  state_dir: "/tmp/tsnet"
  authkey_env: "TS_AUTHKEY"
endpoints:
  - path: "/mcp/test"
    target: "http://test:3000/mcp"
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for non-IP hostname")
	}
}

func TestIPv6Loopback(t *testing.T) {
	path := writeConfig(t, `
server:
  listen: "[::1]:8900"
tailnet:
  hostname: "mcp-bridge"
  state_dir: "/tmp/tsnet"
  authkey_env: "TS_AUTHKEY"
endpoints:
  - path: "/mcp/test"
    target: "http://test:3000/mcp"
`)
	_, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error for IPv6 loopback: %v", err)
	}
}
