package config

import (
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLoadConfigDefaults(t *testing.T) {
	for _, kv := range os.Environ() {
		key, _, ok := strings.Cut(kv, "=")
		if ok && strings.HasPrefix(key, "GITMAN_") {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatalf("unset %s: %v", key, err)
			}
		}
	}
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
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
	if cfg.GitHTTPMaxConcurrent != 16 || cfg.GitHTTPMaxConcurrentPerIP != 4 {
		t.Errorf("unexpected Git HTTP concurrency defaults: total=%d per_ip=%d", cfg.GitHTTPMaxConcurrent, cfg.GitHTTPMaxConcurrentPerIP)
	}
	if cfg.GitHTTPTimeout != 30*time.Minute {
		t.Errorf("expected default GitHTTPTimeout=30m, got %s", cfg.GitHTTPTimeout)
	}
	if cfg.FileSearchMaxConcurrent != 8 || cfg.FileSearchMaxConcurrentPerIP != 2 || cfg.FileSearchMaxFiles != 100000 || cfg.FileSearchMaxBytes != 32*1024*1024 || cfg.FileSearchTimeout != 5*time.Second {
		t.Errorf("unexpected file search defaults: concurrent=%d per_ip=%d files=%d bytes=%d timeout=%s", cfg.FileSearchMaxConcurrent, cfg.FileSearchMaxConcurrentPerIP, cfg.FileSearchMaxFiles, cfg.FileSearchMaxBytes, cfg.FileSearchTimeout)
	}
	if cfg.RepoBrowseMaxConcurrent != 16 || cfg.RepoBrowseMaxConcurrentPerIP != 4 || cfg.RepoBrowseTimeout != 10*time.Second {
		t.Errorf("unexpected repo browse defaults: total=%d per_ip=%d timeout=%s", cfg.RepoBrowseMaxConcurrent, cfg.RepoBrowseMaxConcurrentPerIP, cfg.RepoBrowseTimeout)
	}
	if cfg.RepoStreamMaxConcurrent != 8 || cfg.RepoStreamMaxConcurrentPerIP != 2 || cfg.RepoStreamTimeout != 15*time.Minute {
		t.Errorf("unexpected repo stream defaults: total=%d per_ip=%d timeout=%s", cfg.RepoStreamMaxConcurrent, cfg.RepoStreamMaxConcurrentPerIP, cfg.RepoStreamTimeout)
	}
	if cfg.CIArtifactMaxEntries != 5000 || cfg.CIWorkspaceMaxEntries != 200000 || cfg.CICacheMaxEntries != 100000 {
		t.Errorf("unexpected CI entry defaults: artifacts=%d workspace=%d cache=%d", cfg.CIArtifactMaxEntries, cfg.CIWorkspaceMaxEntries, cfg.CICacheMaxEntries)
	}
	if cfg.CIStorageMinFreeBytes != 1024*1024*1024 || cfg.CIStorageMinFreeInodes != 10000 {
		t.Errorf("unexpected CI storage reserve defaults: bytes=%d inodes=%d", cfg.CIStorageMinFreeBytes, cfg.CIStorageMinFreeInodes)
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
	t.Setenv("GITMAN_CI_ALLOW_DOCKER_SOCKET", "true")
	t.Setenv("GITMAN_CI_DOCKER_SOCKET_PATH", "/tmp/custom-docker.sock")
	t.Setenv("GITMAN_GIT_HTTP_MAX_CONCURRENT", "7")
	t.Setenv("GITMAN_GIT_HTTP_MAX_CONCURRENT_PER_IP", "2")
	t.Setenv("GITMAN_GIT_HTTP_TIMEOUT", "45m")
	t.Setenv("GITMAN_FILE_SEARCH_MAX_CONCURRENT", "3")
	t.Setenv("GITMAN_FILE_SEARCH_MAX_CONCURRENT_PER_IP", "1")
	t.Setenv("GITMAN_FILE_SEARCH_MAX_FILES", "1234")
	t.Setenv("GITMAN_FILE_SEARCH_MAX_BYTES", "1048576")
	t.Setenv("GITMAN_FILE_SEARCH_TIMEOUT", "2s")
	t.Setenv("GITMAN_REPO_BROWSE_MAX_CONCURRENT", "5")
	t.Setenv("GITMAN_REPO_BROWSE_MAX_CONCURRENT_PER_IP", "2")
	t.Setenv("GITMAN_REPO_BROWSE_TIMEOUT", "3s")
	t.Setenv("GITMAN_REPO_STREAM_MAX_CONCURRENT", "4")
	t.Setenv("GITMAN_REPO_STREAM_MAX_CONCURRENT_PER_IP", "1")
	t.Setenv("GITMAN_REPO_STREAM_TIMEOUT", "12m")
	t.Setenv("GITMAN_CI_ARTIFACT_MAX_ENTRIES", "4321")
	t.Setenv("GITMAN_CI_WORKSPACE_MAX_ENTRIES", "54321")
	t.Setenv("GITMAN_CI_CACHE_MAX_ENTRIES", "32100")
	t.Setenv("GITMAN_CI_STORAGE_MIN_FREE_BYTES", "268435456")
	t.Setenv("GITMAN_CI_STORAGE_MIN_FREE_INODES", "2000")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
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
	if cfg.GitHTTPMaxConcurrent != 7 || cfg.GitHTTPMaxConcurrentPerIP != 2 || cfg.GitHTTPTimeout != 45*time.Minute {
		t.Errorf("unexpected Git HTTP overrides: max=%d per_ip=%d timeout=%s", cfg.GitHTTPMaxConcurrent, cfg.GitHTTPMaxConcurrentPerIP, cfg.GitHTTPTimeout)
	}
	if cfg.FileSearchMaxConcurrent != 3 || cfg.FileSearchMaxConcurrentPerIP != 1 || cfg.FileSearchMaxFiles != 1234 || cfg.FileSearchMaxBytes != 1048576 || cfg.FileSearchTimeout != 2*time.Second {
		t.Errorf("unexpected file search overrides: concurrent=%d per_ip=%d files=%d bytes=%d timeout=%s", cfg.FileSearchMaxConcurrent, cfg.FileSearchMaxConcurrentPerIP, cfg.FileSearchMaxFiles, cfg.FileSearchMaxBytes, cfg.FileSearchTimeout)
	}
	if cfg.RepoBrowseMaxConcurrent != 5 || cfg.RepoBrowseMaxConcurrentPerIP != 2 || cfg.RepoBrowseTimeout != 3*time.Second {
		t.Errorf("unexpected repo browse overrides: total=%d per_ip=%d timeout=%s", cfg.RepoBrowseMaxConcurrent, cfg.RepoBrowseMaxConcurrentPerIP, cfg.RepoBrowseTimeout)
	}
	if cfg.RepoStreamMaxConcurrent != 4 || cfg.RepoStreamMaxConcurrentPerIP != 1 || cfg.RepoStreamTimeout != 12*time.Minute {
		t.Errorf("unexpected repo stream overrides: total=%d per_ip=%d timeout=%s", cfg.RepoStreamMaxConcurrent, cfg.RepoStreamMaxConcurrentPerIP, cfg.RepoStreamTimeout)
	}
	if cfg.CIArtifactMaxEntries != 4321 || cfg.CIWorkspaceMaxEntries != 54321 || cfg.CICacheMaxEntries != 32100 {
		t.Errorf("unexpected CI entry overrides: artifacts=%d workspace=%d cache=%d", cfg.CIArtifactMaxEntries, cfg.CIWorkspaceMaxEntries, cfg.CICacheMaxEntries)
	}
	if cfg.CIStorageMinFreeBytes != 268435456 || cfg.CIStorageMinFreeInodes != 2000 {
		t.Errorf("unexpected CI storage reserve overrides: bytes=%d inodes=%d", cfg.CIStorageMinFreeBytes, cfg.CIStorageMinFreeInodes)
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
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
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
		{name: "Git HTTP concurrency", key: "GITMAN_GIT_HTTP_MAX_CONCURRENT", value: "0"},
		{name: "Git HTTP per-IP concurrency", key: "GITMAN_GIT_HTTP_MAX_CONCURRENT_PER_IP", value: "0"},
		{name: "Git HTTP timeout", key: "GITMAN_GIT_HTTP_TIMEOUT", value: "0s"},
		{name: "file search concurrency", key: "GITMAN_FILE_SEARCH_MAX_CONCURRENT", value: "0"},
		{name: "file search per-IP concurrency", key: "GITMAN_FILE_SEARCH_MAX_CONCURRENT_PER_IP", value: "0"},
		{name: "file search files", key: "GITMAN_FILE_SEARCH_MAX_FILES", value: "-1"},
		{name: "file search bytes", key: "GITMAN_FILE_SEARCH_MAX_BYTES", value: "0"},
		{name: "file search timeout", key: "GITMAN_FILE_SEARCH_TIMEOUT", value: "nope"},
		{name: "artifact entries", key: "GITMAN_CI_ARTIFACT_MAX_ENTRIES", value: "0"},
		{name: "workspace entries", key: "GITMAN_CI_WORKSPACE_MAX_ENTRIES", value: "-1"},
		{name: "cache entries", key: "GITMAN_CI_CACHE_MAX_ENTRIES", value: "nope"},
		{name: "storage free bytes", key: "GITMAN_CI_STORAGE_MIN_FREE_BYTES", value: "0"},
		{name: "storage free inodes", key: "GITMAN_CI_STORAGE_MIN_FREE_INODES", value: "0"},
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
	base, err := LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
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
		{name: "container user missing gid", mutate: func(c *Config) { c.CIContainerUser = "1000" }},
		{name: "container user symbolic", mutate: func(c *Config) { c.CIContainerUser = "git:git" }},
		{name: "relative path mapping", mutate: func(c *Config) {
			c.CIWorkerPathPrefix = "data"
			c.CIHostPathPrefix = "host-data"
		}},
		{name: "Git HTTP per-IP exceeds total", mutate: func(c *Config) {
			c.GitHTTPMaxConcurrent = 2
			c.GitHTTPMaxConcurrentPerIP = 3
		}},
		{name: "file search per-IP exceeds total", mutate: func(c *Config) {
			c.FileSearchMaxConcurrent = 1
			c.FileSearchMaxConcurrentPerIP = 2
		}},
		{name: "repo browse per-IP exceeds total", mutate: func(c *Config) {
			c.RepoBrowseMaxConcurrent = 1
			c.RepoBrowseMaxConcurrentPerIP = 2
		}},
		{name: "repo stream per-IP exceeds total", mutate: func(c *Config) {
			c.RepoStreamMaxConcurrent = 1
			c.RepoStreamMaxConcurrentPerIP = 2
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

func TestLoadConfigRejectsExplicitEmptyContainerUser(t *testing.T) {
	t.Setenv("GITMAN_CI_CONTAINER_USER", "")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "GITMAN_CI_CONTAINER_USER") {
		t.Fatalf("expected explicit empty container user to be rejected, got %v", err)
	}
}

func TestLoadConfigReturnsEnvironmentErrors(t *testing.T) {
	t.Setenv("GITMAN_WORKER_CONCURRENCY", "not-a-number")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "GITMAN_WORKER_CONCURRENCY") {
		t.Fatalf("expected load error for invalid environment, got %v", err)
	}
}

func TestParseCIContainerUser(t *testing.T) {
	tests := []struct {
		value    string
		wantUID  int
		wantGID  int
		wantFail bool
	}{
		{value: "1:1", wantUID: 1, wantGID: 1},
		{value: "1000:1000", wantUID: 1000, wantGID: 1000},
		{value: " 42:7 ", wantUID: 42, wantGID: 7},
		// Root is syntactically valid configuration. The worker rejects it.
		{value: "0:0", wantUID: 0, wantGID: 0},
		{value: "", wantFail: true},
		{value: "1000", wantFail: true},
		{value: "1000:", wantFail: true},
		{value: ":1000", wantFail: true},
		{value: "git:1000", wantFail: true},
		{value: "1000:git", wantFail: true},
		{value: "1:2:3", wantFail: true},
		{value: "-1:2", wantFail: true},
	}
	for _, tt := range tests {
		uid, gid, err := ParseCIContainerUser(tt.value)
		if tt.wantFail {
			if err == nil {
				t.Fatalf("ParseCIContainerUser(%q) unexpectedly succeeded", tt.value)
			}
			continue
		}
		if err != nil || uid != tt.wantUID || gid != tt.wantGID {
			t.Fatalf("ParseCIContainerUser(%q) = %d:%d, %v; want %d:%d", tt.value, uid, gid, err, tt.wantUID, tt.wantGID)
		}
	}
}

func TestProductionWarnings(t *testing.T) {
	safe := &Config{
		PublicURL:          "https://git.example",
		ForceSecureCookies: true,
		SecretKey:          "configured",
	}
	if warnings := safe.ProductionWarnings(); len(warnings) != 0 {
		t.Fatalf("safe production config warnings = %v", warnings)
	}

	unsafe := &Config{
		PublicURL:           "https://git.example",
		AllowRegister:       true,
		CIAllowDockerSocket: true,
	}
	warnings := strings.Join(unsafe.ProductionWarnings(), "\n")
	for _, want := range []string{"Secure", "self-registration", "GITMAN_SECRET_KEY", "Docker socket"} {
		if !strings.Contains(warnings, want) {
			t.Fatalf("production warnings missing %q: %s", want, warnings)
		}
	}
}

func TestProductionWarningsForHTTP(t *testing.T) {
	remote := &Config{PublicURL: "http://git.example", SecretKey: "configured"}
	warnings := strings.Join(remote.ProductionWarnings(), "\n")
	if !strings.Contains(warnings, "plain HTTP") {
		t.Fatalf("non-loopback HTTP warning missing: %s", warnings)
	}

	loopback := &Config{PublicURL: "http://127.0.0.1:8080", SecretKey: "configured"}
	if warnings := strings.Join(loopback.ProductionWarnings(), "\n"); strings.Contains(warnings, "plain HTTP") {
		t.Fatalf("loopback development URL received production HTTP warning: %s", warnings)
	}

	forced := &Config{PublicURL: "http://localhost:8080", ForceSecureCookies: true, SecretKey: "configured"}
	if warnings := strings.Join(forced.ProductionWarnings(), "\n"); !strings.Contains(warnings, "browsers may refuse") {
		t.Fatalf("HTTP + forced secure-cookie warning missing: %s", warnings)
	}
}
