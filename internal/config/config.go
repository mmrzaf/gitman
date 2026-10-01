// Package config loads and validates the small set of environment
// variables Gitman needs to run. Behavior that other tools expose as a
// tunable is fixed unless it depends on the operator's host or storage.
package config

import (
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func DefaultRetention() Retention { return Retention{Logs: 30, Runs: 90, Audit: 365, Deployments: 365} }

type Retention struct{ Logs, Runs, Audit, Deployments int }

// Config holds every setting Gitman reads from its environment.
type Config struct {
	Resources Resources
	// DatabaseURL is a PostgreSQL connection string, as accepted by pgx.
	// PostgreSQL is Gitman's only store.
	DatabaseURL string

	// DataDir is the root directory for everything Gitman keeps on disk:
	// bare repositories, the generated Git hook scripts, and CI job
	// workspaces.
	DataDir string

	// PublicURL is the externally reachable base URL, used to build clone
	// URLs and the links printed in push output.
	PublicURL string

	// WebURL is where a worker reaches the web process to fetch a run's
	// commit over Git HTTP. It defaults to PublicURL; inside a Docker
	// network it is usually the web container's internal address.
	WebURL string

	// Retention separates logs, run metadata, audit records and deployments.
	Retention Retention

	// SecretKey encrypts repository secrets at rest. Secret storage is
	// unavailable when this is empty.
	SecretKey string

	// Port is the HTTP listen port for the web process.
	Port int

	// DatabaseMaxConns caps the size of the web and worker processes'
	// connection pools. 0 keeps each process's own default.
	DatabaseMaxConns int

	// TrustedProxies are the addresses of reverse proxies whose
	// X-Forwarded-For header is believed. Empty means the connection's
	// own peer address is the client, which is right when nothing sits
	// in front of Gitman.
	TrustedProxies []netip.Prefix

	// LogLevel is one of "debug", "info", "warn" or "error".
	LogLevel string
	// LogFormat is "text" or "json".
	LogFormat string
}

// ReposPath is the directory bare repositories are stored under, one
// directory per repository ID.
func (c *Config) ReposPath() string {
	return filepath.Join(c.DataDir, "repos")
}

// HooksPath is the directory holding the Git hook scripts that route
// proc-receive back into this binary. The web process
// regenerates it on every start, so the scripts always point at the
// binary that is actually running.
func (c *Config) HooksPath() string {
	return filepath.Join(c.DataDir, "hooks")
}

// WorkspacesPath is the directory CI job workspaces are created under.
func (c *Config) WorkspacesPath() string {
	return filepath.Join(c.DataDir, "workspaces")
}

// Environment variable names, shared with the Git hook environment so the
// hook process loads exactly the configuration the web process runs with.
const (
	EnvDatabaseURL = "GITMAN_DATABASE_URL"
	EnvDataDir     = "GITMAN_DATA_DIR"
	EnvPublicURL   = "GITMAN_PUBLIC_URL"
	EnvWebURL      = "GITMAN_WEB_URL"
	EnvSecretKey   = "GITMAN_SECRET_KEY"
	EnvPort        = "GITMAN_PORT"
	// EnvDatabaseMaxConns caps the web and worker processes' connection
	// pool sizes; 0 (the default) keeps each process's own default.
	EnvDatabaseMaxConns        = "GITMAN_DATABASE_MAX_CONNS"
	EnvLogRetentionDays        = "GITMAN_LOG_RETENTION_DAYS"
	EnvRunRetentionDays        = "GITMAN_RUN_RETENTION_DAYS"
	EnvAuditRetentionDays      = "GITMAN_AUDIT_RETENTION_DAYS"
	EnvDeploymentRetentionDays = "GITMAN_DEPLOYMENT_RETENTION_DAYS"
	// EnvTrustedProxies is a comma-separated list of IP addresses or
	// CIDR ranges, such as "172.16.0.0/12" for a proxy on a Docker
	// network.
	EnvTrustedProxies = "GITMAN_TRUSTED_PROXIES"
	// EnvLogLevel is one of "debug", "info" (the default), "warn" or
	// "error".
	EnvLogLevel = "GITMAN_LOG_LEVEL"
	// EnvLogFormat is "text" (the default, for a terminal or a log
	// collector that parses lines itself) or "json" (for one that wants
	// structured fields).
	EnvLogFormat = "GITMAN_LOG_FORMAT"
)

// Load reads configuration from the environment and validates it.
func Load() (*Config, error) {
	cfg := &Config{
		DatabaseURL: strings.TrimSpace(os.Getenv(EnvDatabaseURL)),
		DataDir:     getEnv(EnvDataDir, ".data"),
		PublicURL:   strings.TrimRight(getEnv(EnvPublicURL, "http://localhost:8080"), "/"),
		WebURL:      strings.TrimRight(strings.TrimSpace(os.Getenv(EnvWebURL)), "/"),
		SecretKey:   os.Getenv(EnvSecretKey),
		LogLevel:    getEnv(EnvLogLevel, "info"),
		LogFormat:   getEnv(EnvLogFormat, "text"),
	}

	port, err := getEnvInt(EnvPort, 8080)
	if err != nil {
		return nil, err
	}
	cfg.Port = port

	if cfg.DatabaseMaxConns, err = getEnvInt(EnvDatabaseMaxConns, 0); err != nil {
		return nil, err
	}

	for _, setting := range []struct {
		name     string
		fallback int
		dst      *int
	}{
		{EnvStepMemoryMiB, 2048, &cfg.Resources.MemoryMiB}, {EnvStepCPUs, 2, &cfg.Resources.CPUs},
		{EnvStepPIDs, 256, &cfg.Resources.PIDs}, {EnvWorkspaceGiB, 10, &cfg.Resources.WorkspaceGiB}, {EnvDiskReserveGiB, 5, &cfg.Resources.DiskReserveGiB},
		{EnvLogRetentionDays, 30, &cfg.Retention.Logs}, {EnvRunRetentionDays, 90, &cfg.Retention.Runs},
		{EnvAuditRetentionDays, 365, &cfg.Retention.Audit}, {EnvDeploymentRetentionDays, 365, &cfg.Retention.Deployments},
	} {
		if *setting.dst, err = getEnvInt(setting.name, setting.fallback); err != nil {
			return nil, err
		}
	}

	proxies, err := parseProxies(os.Getenv(EnvTrustedProxies))
	if err != nil {
		return nil, err
	}
	cfg.TrustedProxies = proxies

	if err := cfg.Resources.Validate(); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate rejects a configuration that would otherwise fail in a
// confusing way once Gitman is already serving requests, and makes
// DataDir absolute so every process and every Git subprocess agrees on
// where it is regardless of working directory.
func (c *Config) Validate() error {
	c.Resources = c.Resources.WithDefaults()
	if err := c.Resources.Validate(); err != nil {
		return err
	}
	if c.DatabaseURL == "" {
		return fmt.Errorf("%s is required", EnvDatabaseURL)
	}
	if strings.TrimSpace(c.DataDir) == "" {
		return fmt.Errorf("%s must not be empty", EnvDataDir)
	}
	abs, err := filepath.Abs(c.DataDir)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", EnvDataDir, err)
	}
	c.DataDir = abs

	if err := validateBaseURL(c.PublicURL); err != nil {
		return fmt.Errorf("%s %w", EnvPublicURL, err)
	}
	if c.WebURL == "" {
		c.WebURL = c.PublicURL
	}
	if err := validateBaseURL(c.WebURL); err != nil {
		return fmt.Errorf("%s %w", EnvWebURL, err)
	}
	c.PublicURL = strings.TrimSuffix(c.PublicURL, "/")
	c.WebURL = strings.TrimSuffix(c.WebURL, "/")
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("%s must be between 1 and 65535", EnvPort)
	}
	if c.DatabaseMaxConns < 0 || c.DatabaseMaxConns > 1000 || c.DatabaseMaxConns > 0 && c.DatabaseMaxConns < 4 {
		return fmt.Errorf("%s must be 0 (the process's own default) or between 4 and 1000", EnvDatabaseMaxConns)
	}
	for _, setting := range []struct {
		name string
		days int
	}{
		{EnvLogRetentionDays, c.Retention.Logs}, {EnvRunRetentionDays, c.Retention.Runs},
		{EnvAuditRetentionDays, c.Retention.Audit}, {EnvDeploymentRetentionDays, c.Retention.Deployments},
	} {
		if setting.days < 1 || setting.days > 36500 {
			return fmt.Errorf("%s must be between 1 and 36500 days", setting.name)
		}
	}
	if c.SecretKey != "" {
		raw, err := base64.StdEncoding.DecodeString(c.SecretKey)
		if err != nil || len(raw) != 32 {
			return fmt.Errorf("%s must be base64 encoding of 32 random bytes", EnvSecretKey)
		}
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("%s must be \"debug\", \"info\", \"warn\" or \"error\"", EnvLogLevel)
	}
	switch c.LogFormat {
	case "text", "json":
	default:
		return fmt.Errorf("%s must be \"text\" or \"json\"", EnvLogFormat)
	}
	return nil
}

// NewLogger builds the process's logger per LogLevel and LogFormat,
// writing to w.
func (c *Config) NewLogger(w io.Writer) *slog.Logger {
	var level slog.Level
	switch c.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if c.LogFormat == "json" {
		handler = slog.NewJSONHandler(w, opts)
	} else {
		handler = slog.NewTextHandler(w, opts)
	}
	return slog.New(handler)
}

func validateBaseURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.RawPath != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("must be an absolute http or https URL without credentials, path prefix, query or fragment")
	}
	return nil
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}

func getEnvInt(key string, fallback int) (int, error) {
	value, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	return n, nil
}

func parseProxies(raw string) ([]netip.Prefix, error) {
	var prefixes []netip.Prefix
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "/") {
			prefix, err := netip.ParsePrefix(part)
			if err != nil {
				return nil, fmt.Errorf("%s: %q is not an IP address or CIDR range", EnvTrustedProxies, part)
			}
			prefixes = append(prefixes, prefix.Masked())
			continue
		}
		addr, err := netip.ParseAddr(part)
		if err != nil {
			return nil, fmt.Errorf("%s: %q is not an IP address or CIDR range", EnvTrustedProxies, part)
		}
		addr = addr.Unmap()
		prefixes = append(prefixes, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return prefixes, nil
}
