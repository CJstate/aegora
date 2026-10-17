// Package config provides runtime configuration loading and validation
// for the Aegora gateway.
package config

import (
	"bytes"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration wraps time.Duration with YAML unmarshaling support
// for duration strings like "30s", "5m", "100ms".
type Duration time.Duration

// Duration returns the underlying time.Duration.
func (d Duration) Duration() time.Duration {
	return time.Duration(d)
}

// String returns the string representation of the duration.
func (d Duration) String() string {
	return time.Duration(d).String()
}

// UnmarshalYAML implements the yaml.Unmarshaler interface for Duration.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return fmt.Errorf("decode duration: %w", err)
	}

	dur, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}

	*d = Duration(dur)
	return nil
}

// Config represents the top-level configuration for Aegora.
type Config struct {
	Server        ServerConfig        `yaml:"server"`
	Routes        []RouteConfig       `yaml:"routes"`
	Observability ObservabilityConfig `yaml:"observability"`
	RateLimit     RateLimitConfig     `yaml:"rate_limit,omitempty"`
}

// RateLimitConfig configures gateway request rate limiting behavior.
type RateLimitConfig struct {
	Enabled         bool     `yaml:"enabled"`
	Limiter         string   `yaml:"limiter,omitempty"`
	MaxRequests     int      `yaml:"max_requests,omitempty"`
	Window          Duration `yaml:"window,omitempty"`
	RefillPerSecond float64  `yaml:"refill_per_second,omitempty"`
	Key             string   `yaml:"key,omitempty"`
}

// ServerConfig holds HTTP server network and timeout settings.
type ServerConfig struct {
	Addr         string   `yaml:"addr"`
	ReadTimeout  Duration `yaml:"read_timeout"`
	WriteTimeout Duration `yaml:"write_timeout"`
	IdleTimeout  Duration `yaml:"idle_timeout"`
}

// RouteConfig specifies upstream routing rules and target destinations.
type RouteConfig struct {
	Name        string            `yaml:"name"`
	PathPrefix  string            `yaml:"path_prefix"`
	PathRegex   string            `yaml:"path_regex,omitempty"`
	Methods     []string          `yaml:"methods,omitempty"`
	Headers     map[string]string `yaml:"headers,omitempty"`
	Targets     []string          `yaml:"targets"`
	StripPrefix string            `yaml:"strip_prefix,omitempty"`
	LB          string            `yaml:"lb,omitempty"`
}

// ObservabilityConfig contains telemetry and logging configuration.
type ObservabilityConfig struct {
	LogLevel    string `yaml:"log_level"`
	MetricsAddr string `yaml:"metrics_addr"`
}

// Load reads a YAML configuration file from path, decodes it strictly,
// applies environment variable overrides, and validates the resulting configuration.
// A missing or unreadable file returns an error.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read file: %w", err)
	}

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("config: parse yaml: %w", err)
	}

	if err := applyEnvOverrides(&cfg); err != nil {
		return nil, err
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// applyEnvOverrides applies environment variable overrides using an explicit allowlist.
// Environment variables take precedence over file-configured values.
func applyEnvOverrides(cfg *Config) error {
	if val, ok := os.LookupEnv("AEGORA_SERVER_ADDR"); ok {
		cfg.Server.Addr = val
	}

	if val, ok := os.LookupEnv("AEGORA_SERVER_READ_TIMEOUT"); ok {
		dur, err := time.ParseDuration(val)
		if err != nil {
			return fmt.Errorf("config: env AEGORA_SERVER_READ_TIMEOUT: invalid duration %q: %w", val, err)
		}
		cfg.Server.ReadTimeout = Duration(dur)
	}

	if val, ok := os.LookupEnv("AEGORA_SERVER_WRITE_TIMEOUT"); ok {
		dur, err := time.ParseDuration(val)
		if err != nil {
			return fmt.Errorf("config: env AEGORA_SERVER_WRITE_TIMEOUT: invalid duration %q: %w", val, err)
		}
		cfg.Server.WriteTimeout = Duration(dur)
	}

	if val, ok := os.LookupEnv("AEGORA_SERVER_IDLE_TIMEOUT"); ok {
		dur, err := time.ParseDuration(val)
		if err != nil {
			return fmt.Errorf("config: env AEGORA_SERVER_IDLE_TIMEOUT: invalid duration %q: %w", val, err)
		}
		cfg.Server.IdleTimeout = Duration(dur)
	}

	if val, ok := os.LookupEnv("AEGORA_OBSERVABILITY_LOG_LEVEL"); ok {
		cfg.Observability.LogLevel = val
	}

	if val, ok := os.LookupEnv("AEGORA_OBSERVABILITY_METRICS_ADDR"); ok {
		cfg.Observability.MetricsAddr = val
	}

	return nil
}

// Validate checks the configuration for correctness and enforces fail-fast rules.
// Every error identifies the offending field.
func (c *Config) Validate() error {
	if _, err := net.ResolveTCPAddr("tcp", c.Server.Addr); err != nil {
		return fmt.Errorf("config: server.addr: invalid address %q: %w", c.Server.Addr, err)
	}

	if c.Server.ReadTimeout <= 0 {
		return fmt.Errorf("config: server.read_timeout: must be positive, got %s", c.Server.ReadTimeout)
	}

	if c.Server.WriteTimeout <= 0 {
		return fmt.Errorf("config: server.write_timeout: must be positive, got %s", c.Server.WriteTimeout)
	}

	if c.Server.IdleTimeout <= 0 {
		return fmt.Errorf("config: server.idle_timeout: must be positive, got %s", c.Server.IdleTimeout)
	}

	if len(c.Routes) == 0 {
		return fmt.Errorf("config: routes: must configure at least one route")
	}

	seenNames := make(map[string]struct{}, len(c.Routes))
	for i := range c.Routes {
		route := &c.Routes[i]

		if strings.TrimSpace(route.Name) == "" {
			return fmt.Errorf("config: routes[%d].name: cannot be empty", i)
		}

		if _, exists := seenNames[route.Name]; exists {
			return fmt.Errorf("config: routes[%d].name: duplicate route name %q", i, route.Name)
		}
		seenNames[route.Name] = struct{}{}

		if len(route.Targets) == 0 {
			return fmt.Errorf("config: routes[%d].targets: must configure at least one target", i)
		}

		for j, target := range route.Targets {
			u, err := url.ParseRequestURI(target)
			if err != nil || u.Scheme == "" || u.Host == "" {
				return fmt.Errorf("config: routes[%d].targets[%d]: invalid URL %q", i, j, target)
			}
		}

		if route.StripPrefix != "" && !strings.HasPrefix(route.StripPrefix, "/") {
			return fmt.Errorf("config: routes[%d].strip_prefix: must start with %q, got %q", i, "/", route.StripPrefix)
		}

		if route.PathRegex != "" {
			if _, err := regexp.Compile(route.PathRegex); err != nil {
				return fmt.Errorf("config: routes[%d].path_regex: invalid regex %q: %w", i, route.PathRegex, err)
			}
		}

		if route.LB == "" {
			route.LB = "round_robin"
		} else if route.LB != "round_robin" && route.LB != "least_connections" {
			return fmt.Errorf("config: routes[%d].lb: must be one of round_robin, least_connections; got %q", i, route.LB)
		}
	}

	switch c.Observability.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("config: observability.log_level: must be one of debug, info, warn, error; got %q", c.Observability.LogLevel)
	}

	if c.RateLimit.Enabled {
		if c.RateLimit.MaxRequests <= 0 {
			return fmt.Errorf("config: rate_limit.max_requests: must be positive, got %d", c.RateLimit.MaxRequests)
		}

		if c.RateLimit.Limiter != "sliding_window" && c.RateLimit.Limiter != "token_bucket" {
			return fmt.Errorf("config: rate_limit.limiter: must be one of sliding_window, token_bucket; got %q", c.RateLimit.Limiter)
		}

		if c.RateLimit.Key != "ip" && c.RateLimit.Key != "api_key" {
			return fmt.Errorf("config: rate_limit.key: must be one of ip, api_key; got %q", c.RateLimit.Key)
		}

		if c.RateLimit.Limiter == "sliding_window" && c.RateLimit.Window <= 0 {
			return fmt.Errorf("config: rate_limit.window: must be positive for sliding_window, got %s", c.RateLimit.Window)
		}

		if c.RateLimit.Limiter == "token_bucket" && c.RateLimit.RefillPerSecond <= 0 {
			return fmt.Errorf("config: rate_limit.refill_per_second: must be positive for token_bucket, got %f", c.RateLimit.RefillPerSecond)
		}
	}

	return nil
}
