package config

import (
	"os"
	"path/filepath"
	"strings"
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
  listen: "192.168.1.1:8900"
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

func TestAllowAllInterfaces(t *testing.T) {
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
	if err != nil {
		t.Fatalf("unexpected error for all-interfaces address: %v", err)
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

func TestAuthConfigAbsent(t *testing.T) {
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
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Auth != nil {
		t.Error("auth should be nil when absent from config")
	}
}

func TestAuthConfigValid(t *testing.T) {
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
auth:
  issuer: "https://idp.leo-royal.ts.net"
  audience: "https://mcp.meltforce.net"
  introspection_url: "https://idp.leo-royal.ts.net/introspect"
  client_id: "my-client"
  client_secret: "my-secret"
  resource_metadata_url: "https://mcp.meltforce.net/.well-known/oauth-protected-resource"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Auth == nil {
		t.Fatal("auth should not be nil")
	}
	if cfg.Auth.Issuer != "https://idp.leo-royal.ts.net" {
		t.Errorf("issuer = %q", cfg.Auth.Issuer)
	}
	if cfg.Auth.Audience != "https://mcp.meltforce.net" {
		t.Errorf("audience = %q", cfg.Auth.Audience)
	}
	if cfg.Auth.IntrospectionURL != "https://idp.leo-royal.ts.net/introspect" {
		t.Errorf("introspection_url = %q", cfg.Auth.IntrospectionURL)
	}
	if cfg.Auth.ClientID != "my-client" {
		t.Errorf("client_id = %q", cfg.Auth.ClientID)
	}
	if cfg.Auth.ClientSecret != "my-secret" {
		t.Errorf("client_secret = %q", cfg.Auth.ClientSecret)
	}
	if cfg.Auth.ResourceMetadataURL != "https://mcp.meltforce.net/.well-known/oauth-protected-resource" {
		t.Errorf("resource_metadata_url = %q", cfg.Auth.ResourceMetadataURL)
	}
}

func TestRejectMissingAuthFields(t *testing.T) {
	base := `
server:
  listen: "127.0.0.1:8900"
tailnet:
  hostname: "mcp-bridge"
  state_dir: "/tmp/tsnet"
  authkey_env: "TS_AUTHKEY"
endpoints:
  - path: "/mcp/test"
    target: "http://test:3000/mcp"
auth:
`
	allFields := `
  issuer: "https://idp.example.com"
  audience: "https://mcp.example.com"
  introspection_url: "https://idp.example.com/introspect"
  resource_metadata_url: "https://mcp.example.com/.well-known/oauth-protected-resource"
`
	tests := []struct {
		name   string
		remove string
	}{
		{name: "missing issuer", remove: "issuer"},
		{name: "missing audience", remove: "audience"},
		{name: "missing introspection_url", remove: "introspection_url"},
		{name: "missing resource_metadata_url", remove: "resource_metadata_url"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Build config with one field removed
			cfg := base
			for _, line := range strings.Split(allFields, "\n") {
				if line == "" || strings.Contains(line, tt.remove+":") {
					continue
				}
				cfg += line + "\n"
			}
			path := writeConfig(t, cfg)
			_, err := Load(path)
			if err == nil {
				t.Fatalf("expected error for %s", tt.name)
			}
		})
	}
}

func TestRejectInvalidIntrospectionURL(t *testing.T) {
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
auth:
  issuer: "https://idp.example.com"
  audience: "https://mcp.example.com"
  introspection_url: "not-a-url"
  client_id: "my-client"
  client_secret: "my-secret"
  resource_metadata_url: "https://mcp.example.com/.well-known/oauth-protected-resource"
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid introspection URL")
	}
}

func TestRejectFTPIntrospectionURL(t *testing.T) {
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
auth:
  issuer: "https://idp.example.com"
  audience: "https://mcp.example.com"
  introspection_url: "ftp://idp.example.com/introspect"
  client_id: "my-client"
  client_secret: "my-secret"
  resource_metadata_url: "https://mcp.example.com/.well-known/oauth-protected-resource"
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for FTP introspection URL")
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
