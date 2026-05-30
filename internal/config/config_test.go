package config

import (
	"log/slog"
	"os"
	"strings"
	"testing"
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
