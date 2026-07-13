package config

import (
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func TestLoadConfigDefaults(t *testing.T) {
	// Clear env vars to test defaults
	for _, kv := range os.Environ() {
		k := kv[:indexByte(kv, '=')]
		os.Unsetenv(k)
	}
	cfg := LoadConfig()
	if cfg.Port != "8080" {
		t.Errorf("expected default Port=8080, got %s", cfg.Port)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("expected default LogLevel=info, got %s", cfg.LogLevel)
	}
	if cfg.AllowRegister {
		t.Error("expected default AllowRegister=false")
	}
	if cfg.WorkerConcurrency != 1 {
		t.Errorf("expected default WorkerConcurrency=1, got %d", cfg.WorkerConcurrency)
	}
	if cfg.CIAllowDockerSocket {
		t.Error("expected default CIAllowDockerSocket=false")
	}
	if cfg.CIDockerSocketPath != "/var/run/docker.sock" {
		t.Errorf("unexpected default CIDockerSocketPath: %s", cfg.CIDockerSocketPath)
	}
	if !strings.Contains(cfg.DBPath, ".data/db/gitman.sqlite") {
		t.Errorf("unexpected DBPath: %s", cfg.DBPath)
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	t.Setenv("GITMAN_PORT", "9090")
	t.Setenv("GITMAN_LOG_LEVEL", "warn")
	t.Setenv("GITMAN_ALLOW_REGISTER", "true")
	t.Setenv("GITMAN_WORKER_CONCURRENCY", "4")
	t.Setenv("GITMAN_SECRET_KEY", "testkey")
	t.Setenv("GITMAN_INTERNAL_URL", "http://example.com")
	t.Setenv("GITMAN_CI_ALLOW_DOCKER_SOCKET", "true")
	t.Setenv("GITMAN_CI_DOCKER_SOCKET_PATH", "/tmp/custom-docker.sock")
	cfg := LoadConfig()
	if cfg.Port != "9090" {
		t.Errorf("expected Port=9090, got %s", cfg.Port)
	}
	if cfg.LogLevel != "warn" {
		t.Errorf("expected LogLevel=warn, got %s", cfg.LogLevel)
	}
	if !cfg.AllowRegister {
		t.Error("expected AllowRegister=true")
	}
	if cfg.WorkerConcurrency != 4 {
		t.Errorf("expected WorkerConcurrency=4, got %d", cfg.WorkerConcurrency)
	}
	if cfg.SecretKey != "testkey" {
		t.Errorf("expected SecretKey=testkey, got %s", cfg.SecretKey)
	}
	if !cfg.CIAllowDockerSocket {
		t.Error("expected CIAllowDockerSocket=true")
	}
	if cfg.CIDockerSocketPath != "/tmp/custom-docker.sock" {
		t.Errorf("unexpected CIDockerSocketPath: %s", cfg.CIDockerSocketPath)
	}
}

func TestGetEnvInt(t *testing.T) {
	t.Setenv("NUM", "42")
	if n := getEnvInt("NUM", 10); n != 42 {
		t.Errorf("expected 42, got %d", n)
	}
	t.Setenv("NEG", "-5")
	if n := getEnvInt("NEG", 10); n != 10 {
		t.Errorf("expected fallback 10 for negative, got %d", n)
	}
	if n := getEnvInt("NOT_EXIST", 5); n != 5 {
		t.Errorf("expected fallback 5, got %d", n)
	}
}

func TestParseLogLevel(t *testing.T) {
	tests := []struct {
		level string
		want  slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"warn", slog.LevelWarn},
		{"error", slog.LevelError},
		{"info", slog.LevelInfo},
		{"", slog.LevelInfo},
		{"unknown", slog.LevelInfo},
	}
	for _, tt := range tests {
		if got := ParseLogLevel(tt.level); got != tt.want {
			t.Errorf("ParseLogLevel(%q) = %v, want %v", tt.level, got, tt.want)
		}
	}
}

func TestPublicURLDefaultsToConfiguredPort(t *testing.T) {
	t.Setenv("GITMAN_PORT", "9090")
	t.Setenv("GITMAN_SERVER_HOST", "git.internal")
	os.Unsetenv("GITMAN_PUBLIC_URL")
	cfg := LoadConfig()
	if cfg.PublicURL != "http://git.internal:9090" {
		t.Fatalf("unexpected public URL: %s", cfg.PublicURL)
	}
}

func TestValidateEnvironmentRejectsInvalidExplicitValues(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "integer", key: "GITMAN_WORKER_CONCURRENCY", value: "0"},
		{name: "byte limit", key: "GITMAN_CI_LOG_MAX_BYTES", value: "-1"},
		{name: "boolean", key: "GITMAN_TRUST_PROXY_HEADERS", value: "sometimes"},
		{name: "duration", key: "GITMAN_CI_TIMEOUT", value: "0s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.key, tt.value)
			if err := ValidateEnvironment(); err == nil || !strings.Contains(err.Error(), tt.key) {
				t.Fatalf("expected %s validation error, got %v", tt.key, err)
			}
		})
	}
}

func TestValidateEnvironmentAcceptsSupportedDurationForms(t *testing.T) {
	t.Setenv("GITMAN_CI_TIMEOUT", "90")
	t.Setenv("GITMAN_CI_LEASE_TIMEOUT", "2m")
	t.Setenv("GITMAN_CI_HEARTBEAT_INTERVAL", "15s")
	if err := ValidateEnvironment(); err != nil {
		t.Fatal(err)
	}
}

func TestConfigValidateRejectsUnsafeValues(t *testing.T) {
	base := LoadConfig()
	base.CILeaseTimeout = 2 * time.Minute
	base.CIHeartbeatInterval = 15 * time.Second
	if err := base.Validate(); err != nil {
		t.Fatalf("default config rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "URL credentials", mutate: func(c *Config) { c.PublicURL = "https://user@example.com" }},
		{name: "relative Docker socket", mutate: func(c *Config) { c.CIDockerSocketPath = "docker.sock" }},
		{name: "memory limit", mutate: func(c *Config) { c.MemoryLimit = "unlimited" }},
		{name: "CPU limit", mutate: func(c *Config) { c.CPULimit = "NaN" }},
		{name: "network whitespace", mutate: func(c *Config) { c.CINetwork = "bad network" }},
		{name: "relative path mapping", mutate: func(c *Config) {
			c.CIWorkerPathPrefix = "data"
			c.CIHostPathPrefix = "host-data"
		}},
		{name: "heartbeat ratio", mutate: func(c *Config) { c.CIHeartbeatInterval = time.Minute }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := *base
			tt.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
