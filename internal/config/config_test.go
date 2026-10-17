package config_test

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/syncronix-labs/aegora/internal/config"
	"gopkg.in/yaml.v3"
)

func TestLoad_Valid(t *testing.T) {
	cfg, err := config.Load("testdata/valid.yaml")
	if err != nil {
		t.Fatalf("config.Load() unexpected error: %v", err)
	}

	expected := &config.Config{
		Server: config.ServerConfig{
			Addr:         "127.0.0.1:8080",
			ReadTimeout:  config.Duration(15 * time.Second),
			WriteTimeout: config.Duration(30 * time.Second),
			IdleTimeout:  config.Duration(60 * time.Second),
		},
		Routes: []config.RouteConfig{
			{
				Name:       "api",
				PathPrefix: "/api/v1",
				Targets: []string{
					"http://127.0.0.1:8081",
					"http://127.0.0.1:8082",
				},
				StripPrefix: "/api/v1",
			},
			{
				Name:        "auth",
				PathPrefix:  "/auth",
				Targets:     []string{"https://auth.example.com"},
				StripPrefix: "",
			},
		},
		Observability: config.ObservabilityConfig{
			LogLevel:    "debug",
			MetricsAddr: "127.0.0.1:9090",
		},
	}

	if !reflect.DeepEqual(cfg, expected) {
		t.Fatalf("config mismatch:\ngot:  %+v\nwant: %+v", cfg, expected)
	}

	if got := cfg.Server.ReadTimeout.Duration(); got != 15*time.Second {
		t.Fatalf("expected ReadTimeout 15s, got %v", got)
	}
	if got := cfg.Server.WriteTimeout.Duration(); got != 30*time.Second {
		t.Fatalf("expected WriteTimeout 30s, got %v", got)
	}
	if got := cfg.Server.IdleTimeout.Duration(); got != 60*time.Second {
		t.Fatalf("expected IdleTimeout 60s, got %v", got)
	}
	if got := cfg.Server.ReadTimeout.String(); got != "15s" {
		t.Fatalf("expected ReadTimeout string %q, got %q", "15s", got)
	}
}

func TestLoad_ParseLevelFailures(t *testing.T) {
	tests := []struct {
		name        string
		fixture     string
		wantErrText string
	}{
		{
			name:        "invalid duration in YAML",
			fixture:     "testdata/invalid-duration.yaml",
			wantErrText: "invalid duration",
		},
		{
			name:        "unknown field in YAML",
			fixture:     "testdata/invalid-unknown-field.yaml",
			wantErrText: "extra_typo_field",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := config.Load(tt.fixture)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErrText)
			}
			if !strings.Contains(err.Error(), tt.wantErrText) {
				t.Fatalf("expected error to contain %q, got: %v", tt.wantErrText, err)
			}
		})
	}
}

func parseValidFixture(t *testing.T) *config.Config {
	t.Helper()

	data, err := os.ReadFile("testdata/valid.yaml")
	if err != nil {
		t.Fatalf("failed to read testdata/valid.yaml: %v", err)
	}

	var cfg config.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("failed to unmarshal testdata/valid.yaml: %v", err)
	}

	return &cfg
}

func TestValidate_MutatedNegativeCases(t *testing.T) {
	tests := []struct {
		name           string
		mutate         func(c *config.Config)
		wantFieldInErr string
	}{
		{
			name: "malformed server addr without port",
			mutate: func(c *config.Config) {
				c.Server.Addr = "invalid_addr_no_port"
			},
			wantFieldInErr: "server.addr",
		},
		{
			name: "zero read timeout",
			mutate: func(c *config.Config) {
				c.Server.ReadTimeout = 0
			},
			wantFieldInErr: "server.read_timeout",
		},
		{
			name: "negative write timeout",
			mutate: func(c *config.Config) {
				c.Server.WriteTimeout = -1
			},
			wantFieldInErr: "server.write_timeout",
		},
		{
			name: "zero idle timeout",
			mutate: func(c *config.Config) {
				c.Server.IdleTimeout = 0
			},
			wantFieldInErr: "server.idle_timeout",
		},
		{
			name: "zero routes configured",
			mutate: func(c *config.Config) {
				c.Routes = nil
			},
			wantFieldInErr: "routes",
		},
		{
			name: "empty route name",
			mutate: func(c *config.Config) {
				c.Routes[0].Name = ""
			},
			wantFieldInErr: "routes[0].name",
		},
		{
			name: "duplicate route name",
			mutate: func(c *config.Config) {
				c.Routes = append(c.Routes, config.RouteConfig{
					Name:        c.Routes[0].Name,
					PathPrefix:  "/dup",
					Targets:     []string{"http://127.0.0.1:8080"},
					StripPrefix: "",
				})
			},
			wantFieldInErr: "routes[2].name",
		},
		{
			name: "route with no targets",
			mutate: func(c *config.Config) {
				c.Routes[0].Targets = nil
			},
			wantFieldInErr: "routes[0].targets",
		},
		{
			name: "invalid target URL",
			mutate: func(c *config.Config) {
				c.Routes[0].Targets = []string{"/relative/url"}
			},
			wantFieldInErr: "routes[0].targets[0]",
		},
		{
			name: "strip_prefix not starting with slash",
			mutate: func(c *config.Config) {
				c.Routes[0].StripPrefix = "api/v1"
			},
			wantFieldInErr: "routes[0].strip_prefix",
		},
		{
			name: "bad log level",
			mutate: func(c *config.Config) {
				c.Observability.LogLevel = "verbose"
			},
			wantFieldInErr: "observability.log_level",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := parseValidFixture(t)
			tt.mutate(cfg)

			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantFieldInErr)
			}
			if !strings.Contains(err.Error(), tt.wantFieldInErr) {
				t.Fatalf("expected error to contain %q, got: %v", tt.wantFieldInErr, err)
			}
		})
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := config.Load("testdata/nonexistent-file.yaml")
	if err == nil {
		t.Fatal("expected error for non-existent file, got nil")
	}
	if !strings.Contains(err.Error(), "config: read file") {
		t.Fatalf("expected error to mention reading file, got: %v", err)
	}
}

func TestLoad_EnvOverrides(t *testing.T) {
	t.Setenv("AEGORA_SERVER_ADDR", "127.0.0.1:9999")
	t.Setenv("AEGORA_SERVER_READ_TIMEOUT", "45s")
	t.Setenv("AEGORA_SERVER_WRITE_TIMEOUT", "55s")
	t.Setenv("AEGORA_SERVER_IDLE_TIMEOUT", "120s")
	t.Setenv("AEGORA_OBSERVABILITY_LOG_LEVEL", "warn")
	t.Setenv("AEGORA_OBSERVABILITY_METRICS_ADDR", "127.0.0.1:9191")

	cfg, err := config.Load("testdata/valid.yaml")
	if err != nil {
		t.Fatalf("unexpected error loading with env overrides: %v", err)
	}

	if cfg.Server.Addr != "127.0.0.1:9999" {
		t.Errorf("server.addr: got %q, want %q", cfg.Server.Addr, "127.0.0.1:9999")
	}
	if cfg.Server.ReadTimeout.Duration() != 45*time.Second {
		t.Errorf("server.read_timeout: got %v, want %v", cfg.Server.ReadTimeout.Duration(), 45*time.Second)
	}
	if cfg.Server.WriteTimeout.Duration() != 55*time.Second {
		t.Errorf("server.write_timeout: got %v, want %v", cfg.Server.WriteTimeout.Duration(), 55*time.Second)
	}
	if cfg.Server.IdleTimeout.Duration() != 120*time.Second {
		t.Errorf("server.idle_timeout: got %v, want %v", cfg.Server.IdleTimeout.Duration(), 120*time.Second)
	}
	if cfg.Observability.LogLevel != "warn" {
		t.Errorf("observability.log_level: got %q, want %q", cfg.Observability.LogLevel, "warn")
	}
	if cfg.Observability.MetricsAddr != "127.0.0.1:9191" {
		t.Errorf("observability.metrics_addr: got %q, want %q", cfg.Observability.MetricsAddr, "127.0.0.1:9191")
	}
}

func TestLoad_EnvOverride_InvalidDuration(t *testing.T) {
	t.Setenv("AEGORA_SERVER_READ_TIMEOUT", "invalid-duration")

	_, err := config.Load("testdata/valid.yaml")
	if err == nil {
		t.Fatal("expected error for invalid duration env override, got nil")
	}
	if !strings.Contains(err.Error(), "AEGORA_SERVER_READ_TIMEOUT") {
		t.Fatalf("expected error to mention env var name, got: %v", err)
	}
}

func TestDuration_UnmarshalYAML(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    time.Duration
		wantErr bool
	}{
		{
			name:    "valid 30s",
			input:   "30s",
			want:    30 * time.Second,
			wantErr: false,
		},
		{
			name:    "valid 1m30s",
			input:   "1m30s",
			want:    90 * time.Second,
			wantErr: false,
		},
		{
			name:    "valid 500ms",
			input:   "500ms",
			want:    500 * time.Millisecond,
			wantErr: false,
		},
		{
			name:    "invalid bogus duration",
			input:   "bogus",
			wantErr: true,
		},
		{
			name:    "invalid mapping duration",
			input:   "{foo: bar}",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d config.Duration
			err := yaml.Unmarshal([]byte(tt.input), &d)
			if (err != nil) != tt.wantErr {
				t.Fatalf("yaml.Unmarshal(%q) error = %v, wantErr = %v", tt.input, err, tt.wantErr)
			}
			if !tt.wantErr && d.Duration() != tt.want {
				t.Fatalf("yaml.Unmarshal(%q) = %v, want %v", tt.input, d.Duration(), tt.want)
			}
		})
	}
}

func TestLoad_SampleDevConfig(t *testing.T) {
	cfg, err := config.Load("../../configs/aegora.yaml")
	if err != nil {
		t.Fatalf("failed to load configs/aegora.yaml: %v", err)
	}

	if cfg.Server.Addr != "127.0.0.1:8080" {
		t.Errorf("unexpected server.addr: %s", cfg.Server.Addr)
	}
	if len(cfg.Routes) != 2 {
		t.Errorf("expected 2 routes, got %d", len(cfg.Routes))
	}
}
