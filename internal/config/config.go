package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server    ServerConfig     `yaml:"server"`
	Tailnet   TailnetConfig    `yaml:"tailnet"`
	Endpoints []EndpointConfig `yaml:"endpoints"`
}

type ServerConfig struct {
	Listen         string   `yaml:"listen"`
	AllowedOrigins []string `yaml:"allowed_origins"`
}

type TailnetConfig struct {
	Hostname   string `yaml:"hostname"`
	StateDir   string `yaml:"state_dir"`
	AuthkeyEnv string `yaml:"authkey_env"`
}

type EndpointConfig struct {
	Path        string `yaml:"path"`
	Target      string `yaml:"target"`
	Description string `yaml:"description"`
	Enabled     *bool  `yaml:"enabled,omitempty"`
}

func (e *EndpointConfig) IsEnabled() bool {
	if e.Enabled == nil {
		return true
	}
	return *e.Enabled
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	return &cfg, nil
}

func (c *Config) Validate() error {
	if err := c.validateListen(); err != nil {
		return err
	}
	if err := c.validateTailnet(); err != nil {
		return err
	}
	if err := c.validateEndpoints(); err != nil {
		return err
	}
	return nil
}

func (c *Config) validateListen() error {
	if c.Server.Listen == "" {
		return fmt.Errorf("server.listen is required")
	}

	host, _, err := net.SplitHostPort(c.Server.Listen)
	if err != nil {
		return fmt.Errorf("server.listen must be host:port: %w", err)
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("server.listen host must be an IP address, got %q", host)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("server.listen must use a loopback address, got %q", host)
	}

	return nil
}

func (c *Config) validateTailnet() error {
	if c.Tailnet.Hostname == "" {
		return fmt.Errorf("tailnet.hostname is required")
	}
	if c.Tailnet.StateDir == "" {
		return fmt.Errorf("tailnet.state_dir is required")
	}
	if c.Tailnet.AuthkeyEnv == "" {
		return fmt.Errorf("tailnet.authkey_env is required")
	}
	return nil
}

func (c *Config) validateEndpoints() error {
	if len(c.Endpoints) == 0 {
		return fmt.Errorf("at least one endpoint is required")
	}

	paths := make(map[string]bool)
	for i, ep := range c.Endpoints {
		if ep.Path == "" {
			return fmt.Errorf("endpoints[%d].path is required", i)
		}
		if !strings.HasPrefix(ep.Path, "/") {
			return fmt.Errorf("endpoints[%d].path must start with /", i)
		}
		if paths[ep.Path] {
			return fmt.Errorf("endpoints[%d].path %q is duplicated", i, ep.Path)
		}
		paths[ep.Path] = true

		if ep.Target == "" {
			return fmt.Errorf("endpoints[%d].target is required", i)
		}
		u, err := url.Parse(ep.Target)
		if err != nil {
			return fmt.Errorf("endpoints[%d].target is not a valid URL: %w", i, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("endpoints[%d].target must use http or https scheme, got %q", i, u.Scheme)
		}
		if u.Host == "" {
			return fmt.Errorf("endpoints[%d].target must have a host", i)
		}
	}

	return nil
}
