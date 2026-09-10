package config

import (
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Port                         string
	DBPath                       string
	ReposPath                    string
	AuthKeysPath                 string
	BinaryPath                   string
	SSHUser                      string
	ServerHost                   string
	PublicURL                    string
	ArtifactsPath                string
	SecretKey                    string
	LogLevel                     string
	AllowRegister                bool
	WorkerConcurrency            int
	ForceSecureCookies           bool
	TrustProxyHeaders            bool
	GitReceiveMaxBytes           int64
	GitHTTPMaxConcurrent         int
	GitHTTPMaxConcurrentPerIP    int
	GitHTTPTimeout               time.Duration
	FileSearchMaxConcurrent      int
	FileSearchMaxConcurrentPerIP int
	FileSearchMaxFiles           int
	FileSearchMaxBytes           int64
	FileSearchTimeout            time.Duration
	RepoBrowseMaxConcurrent      int
	RepoBrowseMaxConcurrentPerIP int
	RepoBrowseTimeout            time.Duration
	RepoStreamMaxConcurrent      int
	RepoStreamMaxConcurrentPerIP int
	RepoStreamTimeout            time.Duration

	CacheRoot              string
	MemoryLimit            string
	CPULimit               string
	CIJobTimeout           time.Duration
	CILeaseTimeout         time.Duration
	CIHeartbeatInterval    time.Duration
	CINetwork              string
	CIArtifactMaxBytes     int64
	CIArtifactMaxFiles     int
	CIArtifactMaxEntries   int
	CILogMaxBytes          int64
	CIWorkspaceRoot        string
	CIWorkspaceMaxBytes    int64
	CIWorkspaceMaxEntries  int
	CICacheMaxBytes        int64
	CICacheMaxEntries      int
	CIStorageMinFreeBytes  int64
	CIStorageMinFreeInodes int64
	CIContainerUser        string
	CIAllowDockerSocket    bool
	CIDockerSocketPath     string
	CIWorkerPathPrefix     string
	CIHostPathPrefix       string
}

var dockerMemoryLimitRegex = regexp.MustCompile(`^[1-9][0-9]*(?:[bkmgBKMG])?$`)

func LoadConfig() (*Config, error) {
	if err := ValidateEnvironment(); err != nil {
		return nil, err
	}
	exePath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("detect executable path: %w", err)
	}
	exePath, err = filepath.Abs(exePath)
	if err != nil {
		return nil, fmt.Errorf("resolve executable path: %w", err)
	}

	port := getEnv("GITMAN_PORT", "8080")
	serverHost := getEnv("GITMAN_SERVER_HOST", "localhost")
	publicURL := strings.TrimRight(getEnv("GITMAN_PUBLIC_URL", "http://"+serverHost+":"+port), "/")

	cfg := &Config{
		Port:                         port,
		DBPath:                       getEnv("GITMAN_DB", ".data/db/gitman.sqlite"),
		ReposPath:                    getEnv("GITMAN_REPOS", ".data/repos"),
		AuthKeysPath:                 getEnv("GITMAN_AUTH_KEYS", ".data/authorized_keys"),
		BinaryPath:                   getEnv("GITMAN_BINARY_PATH", exePath),
		SSHUser:                      getEnv("GITMAN_SSH_USER", "git"),
		ServerHost:                   serverHost,
		PublicURL:                    publicURL,
		ArtifactsPath:                getEnv("GITMAN_ARTIFACTS", ".data/artifacts"),
		SecretKey:                    getEnv("GITMAN_SECRET_KEY", ""),
		LogLevel:                     getEnv("GITMAN_LOG_LEVEL", "info"),
		AllowRegister:                getEnvBool("GITMAN_ALLOW_REGISTER", false),
		WorkerConcurrency:            getEnvInt("GITMAN_WORKER_CONCURRENCY", 1),
		ForceSecureCookies:           getEnvBool("GITMAN_FORCE_SECURE_COOKIES", false),
		TrustProxyHeaders:            getEnvBool("GITMAN_TRUST_PROXY_HEADERS", false),
		GitReceiveMaxBytes:           getEnvRequiredPositiveInt64("GITMAN_GIT_RECEIVE_MAX_BYTES", 512*1024*1024),
		GitHTTPMaxConcurrent:         getEnvInt("GITMAN_GIT_HTTP_MAX_CONCURRENT", 16),
		GitHTTPMaxConcurrentPerIP:    getEnvInt("GITMAN_GIT_HTTP_MAX_CONCURRENT_PER_IP", 4),
		GitHTTPTimeout:               getEnvDuration("GITMAN_GIT_HTTP_TIMEOUT", 30*time.Minute),
		FileSearchMaxConcurrent:      getEnvInt("GITMAN_FILE_SEARCH_MAX_CONCURRENT", 8),
		FileSearchMaxConcurrentPerIP: getEnvInt("GITMAN_FILE_SEARCH_MAX_CONCURRENT_PER_IP", 2),
		FileSearchMaxFiles:           getEnvInt("GITMAN_FILE_SEARCH_MAX_FILES", 100000),
		FileSearchMaxBytes:           getEnvRequiredPositiveInt64("GITMAN_FILE_SEARCH_MAX_BYTES", 32*1024*1024),
		FileSearchTimeout:            getEnvDuration("GITMAN_FILE_SEARCH_TIMEOUT", 5*time.Second),
		RepoBrowseMaxConcurrent:      getEnvInt("GITMAN_REPO_BROWSE_MAX_CONCURRENT", 16),
		RepoBrowseMaxConcurrentPerIP: getEnvInt("GITMAN_REPO_BROWSE_MAX_CONCURRENT_PER_IP", 4),
		RepoBrowseTimeout:            getEnvDuration("GITMAN_REPO_BROWSE_TIMEOUT", 10*time.Second),
		RepoStreamMaxConcurrent:      getEnvInt("GITMAN_REPO_STREAM_MAX_CONCURRENT", 8),
		RepoStreamMaxConcurrentPerIP: getEnvInt("GITMAN_REPO_STREAM_MAX_CONCURRENT_PER_IP", 2),
		RepoStreamTimeout:            getEnvDuration("GITMAN_REPO_STREAM_TIMEOUT", 15*time.Minute),

		CacheRoot:              getEnv("GITMAN_CACHE_ROOT", ".data/ci/cache"),
		MemoryLimit:            getEnv("GITMAN_MEMORY_LIMIT", "512m"),
		CPULimit:               getEnv("GITMAN_CPU_LIMIT", "1"),
		CIJobTimeout:           getEnvDuration("GITMAN_CI_TIMEOUT", 30*time.Minute),
		CILeaseTimeout:         getEnvDuration("GITMAN_CI_LEASE_TIMEOUT", 2*time.Minute),
		CIHeartbeatInterval:    getEnvDuration("GITMAN_CI_HEARTBEAT_INTERVAL", 15*time.Second),
		CINetwork:              getEnv("GITMAN_CI_NETWORK", "none"),
		CIArtifactMaxBytes:     getEnvInt64("GITMAN_CI_ARTIFACT_MAX_BYTES", 100*1024*1024),
		CIArtifactMaxFiles:     getEnvInt("GITMAN_CI_ARTIFACT_MAX_FILES", 1000),
		CIArtifactMaxEntries:   getEnvInt("GITMAN_CI_ARTIFACT_MAX_ENTRIES", 5000),
		CILogMaxBytes:          getEnvInt64("GITMAN_CI_LOG_MAX_BYTES", 10*1024*1024),
		CIWorkspaceRoot:        getEnv("GITMAN_CI_WORKSPACE_ROOT", ".data/ci/workspaces"),
		CIWorkspaceMaxBytes:    getEnvInt64("GITMAN_CI_WORKSPACE_MAX_BYTES", 1024*1024*1024),
		CIWorkspaceMaxEntries:  getEnvInt("GITMAN_CI_WORKSPACE_MAX_ENTRIES", 200000),
		CICacheMaxBytes:        getEnvInt64("GITMAN_CI_CACHE_MAX_BYTES", 1024*1024*1024),
		CICacheMaxEntries:      getEnvInt("GITMAN_CI_CACHE_MAX_ENTRIES", 100000),
		CIStorageMinFreeBytes:  getEnvInt64("GITMAN_CI_STORAGE_MIN_FREE_BYTES", 1024*1024*1024),
		CIStorageMinFreeInodes: getEnvInt64("GITMAN_CI_STORAGE_MIN_FREE_INODES", 10000),
		CIContainerUser:        getEnv("GITMAN_CI_CONTAINER_USER", defaultCIContainerUser()),
		CIAllowDockerSocket:    getEnvBool("GITMAN_CI_ALLOW_DOCKER_SOCKET", false),
		CIDockerSocketPath:     getEnv("GITMAN_CI_DOCKER_SOCKET_PATH", "/var/run/docker.sock"),
		CIWorkerPathPrefix:     cleanOptionalPathPrefix(getEnv("GITMAN_CI_WORKER_PATH_PREFIX", "")),
		CIHostPathPrefix:       cleanOptionalPathPrefix(getEnv("GITMAN_CI_HOST_PATH_PREFIX", "")),
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// ValidateEnvironment rejects explicitly configured values that would
// otherwise be silently replaced by defaults.
func ValidateEnvironment() error {
	positiveInts := []string{
		"GITMAN_WORKER_CONCURRENCY",
		"GITMAN_CI_ARTIFACT_MAX_FILES",
		"GITMAN_CI_ARTIFACT_MAX_ENTRIES",
		"GITMAN_CI_WORKSPACE_MAX_ENTRIES",
		"GITMAN_CI_CACHE_MAX_ENTRIES",
		"GITMAN_GIT_HTTP_MAX_CONCURRENT",
		"GITMAN_GIT_HTTP_MAX_CONCURRENT_PER_IP",
		"GITMAN_FILE_SEARCH_MAX_CONCURRENT",
		"GITMAN_FILE_SEARCH_MAX_CONCURRENT_PER_IP",
		"GITMAN_FILE_SEARCH_MAX_FILES",
		"GITMAN_REPO_BROWSE_MAX_CONCURRENT",
		"GITMAN_REPO_BROWSE_MAX_CONCURRENT_PER_IP",
		"GITMAN_REPO_STREAM_MAX_CONCURRENT",
		"GITMAN_REPO_STREAM_MAX_CONCURRENT_PER_IP",
	}
	positiveInt64s := []string{
		"GITMAN_GIT_RECEIVE_MAX_BYTES",
		"GITMAN_FILE_SEARCH_MAX_BYTES",
		"GITMAN_CI_ARTIFACT_MAX_BYTES",
		"GITMAN_CI_LOG_MAX_BYTES",
		"GITMAN_CI_WORKSPACE_MAX_BYTES",
		"GITMAN_CI_CACHE_MAX_BYTES",
		"GITMAN_CI_STORAGE_MIN_FREE_BYTES",
		"GITMAN_CI_STORAGE_MIN_FREE_INODES",
	}
	bools := []string{
		"GITMAN_ALLOW_REGISTER",
		"GITMAN_FORCE_SECURE_COOKIES",
		"GITMAN_TRUST_PROXY_HEADERS",
		"GITMAN_CI_ALLOW_DOCKER_SOCKET",
	}
	durations := []string{
		"GITMAN_GIT_HTTP_TIMEOUT",
		"GITMAN_FILE_SEARCH_TIMEOUT",
		"GITMAN_REPO_BROWSE_TIMEOUT",
		"GITMAN_REPO_STREAM_TIMEOUT",
		"GITMAN_CI_TIMEOUT",
		"GITMAN_CI_LEASE_TIMEOUT",
		"GITMAN_CI_HEARTBEAT_INTERVAL",
	}
	for _, key := range positiveInts {
		if value, ok := os.LookupEnv(key); ok {
			n, err := strconv.Atoi(value)
			if err != nil || n <= 0 {
				return fmt.Errorf("%s must be a positive integer", key)
			}
		}
	}
	for _, key := range positiveInt64s {
		if value, ok := os.LookupEnv(key); ok {
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil || n <= 0 {
				return fmt.Errorf("%s must be a positive integer", key)
			}
		}
	}
	for _, key := range bools {
		if value, ok := os.LookupEnv(key); ok {
			if _, err := strconv.ParseBool(value); err != nil {
				return fmt.Errorf("%s must be true or false", key)
			}
		}
	}
	for _, key := range durations {
		if value, ok := os.LookupEnv(key); ok {
			if d, err := time.ParseDuration(value); err == nil && d > 0 {
				continue
			}
			if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
				continue
			}
			return fmt.Errorf("%s must be a positive duration such as 30s or 5m", key)
		}
	}
	return nil
}

func (c *Config) ProductionWarnings() []string {
	if c == nil {
		return []string{"configuration is nil"}
	}
	var warnings []string
	publicURL, err := url.Parse(c.PublicURL)
	if err == nil {
		host := publicURL.Hostname()
		if publicURL.Scheme == "https" && !c.ForceSecureCookies && !c.TrustProxyHeaders {
			warnings = append(warnings, "GITMAN_PUBLIC_URL uses HTTPS but neither GITMAN_FORCE_SECURE_COOKIES nor GITMAN_TRUST_PROXY_HEADERS is enabled; session cookies may be issued without Secure behind a TLS-terminating proxy")
		}
		if publicURL.Scheme == "http" && !loopbackHost(host) {
			warnings = append(warnings, "GITMAN_PUBLIC_URL uses plain HTTP on a non-loopback host; credentials and sessions require HTTPS in production")
		}
		if publicURL.Scheme == "http" && c.ForceSecureCookies {
			warnings = append(warnings, "GITMAN_FORCE_SECURE_COOKIES is enabled while GITMAN_PUBLIC_URL uses HTTP; browsers may refuse to send the session cookie")
		}
	}
	if c.AllowRegister {
		warnings = append(warnings, "self-registration is enabled; disable GITMAN_ALLOW_REGISTER unless public account creation is intentional")
	}
	if strings.TrimSpace(c.SecretKey) == "" {
		warnings = append(warnings, "GITMAN_SECRET_KEY is not configured; CI secret storage is unavailable")
	}
	if c.CIAllowDockerSocket {
		warnings = append(warnings, "CI Docker socket access is enabled; trusted jobs can control the Docker daemon and effectively the worker host")
	}
	return warnings
}

func loopbackHost(host string) bool {
	host = strings.TrimSpace(strings.TrimSuffix(host, "."))
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("configuration is nil")
	}
	port, err := strconv.Atoi(c.Port)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("GITMAN_PORT must be a number between 1 and 65535")
	}
	for name, raw := range map[string]string{
		"GITMAN_PUBLIC_URL": c.PublicURL,
	} {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
			(parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("%s must be an absolute http or https URL", name)
		}
	}
	for name, value := range map[string]string{
		"GITMAN_DB":                    c.DBPath,
		"GITMAN_REPOS":                 c.ReposPath,
		"GITMAN_AUTH_KEYS":             c.AuthKeysPath,
		"GITMAN_ARTIFACTS":             c.ArtifactsPath,
		"GITMAN_CACHE_ROOT":            c.CacheRoot,
		"GITMAN_CI_WORKSPACE_ROOT":     c.CIWorkspaceRoot,
		"GITMAN_BINARY_PATH":           c.BinaryPath,
		"GITMAN_SSH_USER":              c.SSHUser,
		"GITMAN_SERVER_HOST":           c.ServerHost,
		"GITMAN_MEMORY_LIMIT":          c.MemoryLimit,
		"GITMAN_CPU_LIMIT":             c.CPULimit,
		"GITMAN_CI_NETWORK":            c.CINetwork,
		"GITMAN_CI_CONTAINER_USER":     c.CIContainerUser,
		"GITMAN_CI_DOCKER_SOCKET_PATH": c.CIDockerSocketPath,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s cannot be empty", name)
		}
	}
	if c.GitHTTPMaxConcurrentPerIP > c.GitHTTPMaxConcurrent {
		return fmt.Errorf("GITMAN_GIT_HTTP_MAX_CONCURRENT_PER_IP cannot exceed GITMAN_GIT_HTTP_MAX_CONCURRENT")
	}
	if c.FileSearchMaxConcurrentPerIP > c.FileSearchMaxConcurrent {
		return fmt.Errorf("GITMAN_FILE_SEARCH_MAX_CONCURRENT_PER_IP cannot exceed GITMAN_FILE_SEARCH_MAX_CONCURRENT")
	}
	if c.RepoBrowseMaxConcurrentPerIP > c.RepoBrowseMaxConcurrent {
		return fmt.Errorf("GITMAN_REPO_BROWSE_MAX_CONCURRENT_PER_IP cannot exceed GITMAN_REPO_BROWSE_MAX_CONCURRENT")
	}
	if c.RepoStreamMaxConcurrentPerIP > c.RepoStreamMaxConcurrent {
		return fmt.Errorf("GITMAN_REPO_STREAM_MAX_CONCURRENT_PER_IP cannot exceed GITMAN_REPO_STREAM_MAX_CONCURRENT")
	}
	if c.CIHeartbeatInterval*3 > c.CILeaseTimeout {
		return fmt.Errorf("GITMAN_CI_HEARTBEAT_INTERVAL must be at most one third of GITMAN_CI_LEASE_TIMEOUT")
	}
	if (c.CIWorkerPathPrefix == "") != (c.CIHostPathPrefix == "") {
		return fmt.Errorf("GITMAN_CI_WORKER_PATH_PREFIX and GITMAN_CI_HOST_PATH_PREFIX must be set together")
	}
	if c.CIWorkerPathPrefix != "" && (!filepath.IsAbs(c.CIWorkerPathPrefix) || !filepath.IsAbs(c.CIHostPathPrefix)) {
		return fmt.Errorf("GITMAN_CI_WORKER_PATH_PREFIX and GITMAN_CI_HOST_PATH_PREFIX must be absolute")
	}
	if !filepath.IsAbs(c.CIDockerSocketPath) {
		return fmt.Errorf("GITMAN_CI_DOCKER_SOCKET_PATH must be absolute")
	}
	if !dockerMemoryLimitRegex.MatchString(strings.TrimSpace(c.MemoryLimit)) {
		return fmt.Errorf("GITMAN_MEMORY_LIMIT must be a positive byte value with an optional b, k, m, or g suffix")
	}
	cpuLimit, err := strconv.ParseFloat(strings.TrimSpace(c.CPULimit), 64)
	if err != nil || cpuLimit <= 0 || math.IsNaN(cpuLimit) || math.IsInf(cpuLimit, 0) {
		return fmt.Errorf("GITMAN_CPU_LIMIT must be a positive number")
	}
	if strings.ContainsAny(c.CINetwork, "\x00\r\n\t ") {
		return fmt.Errorf("GITMAN_CI_NETWORK cannot contain whitespace or control characters")
	}
	if _, _, err := ParseCIContainerUser(c.CIContainerUser); err != nil {
		return err
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("GITMAN_LOG_LEVEL must be debug, info, warn, or error")
	}
	return nil
}

// ParseCIContainerUser validates and parses the configured container identity.
// Numeric syntax is a configuration invariant shared by every command. The
// worker separately requires both IDs to be non-root because web/admin may be
// run by root even when the worker runs elsewhere with an explicit identity.
func ParseCIContainerUser(value string) (uid, gid int, err error) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("GITMAN_CI_CONTAINER_USER must be a numeric UID:GID")
	}
	uid, uidErr := strconv.Atoi(parts[0])
	gid, gidErr := strconv.Atoi(parts[1])
	if uidErr != nil || gidErr != nil || uid < 0 || gid < 0 {
		return 0, 0, fmt.Errorf("GITMAN_CI_CONTAINER_USER must be a numeric UID:GID")
	}
	return uid, gid, nil
}

func defaultCIContainerUser() string {
	return strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
}

func cleanOptionalPathPrefix(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return filepath.Clean(value)
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if val, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(val); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

func getEnvInt64(key string, fallback int64) int64 {
	if val, ok := os.LookupEnv(key); ok {
		if n, err := strconv.ParseInt(val, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

func getEnvRequiredPositiveInt64(key string, fallback int64) int64 {
	if val, ok := os.LookupEnv(key); ok {
		n, err := strconv.ParseInt(val, 10, 64)
		if err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	if val, ok := os.LookupEnv(key); ok {
		if b, err := strconv.ParseBool(val); err == nil {
			return b
		}
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if val, ok := os.LookupEnv(key); ok {
		if d, err := time.ParseDuration(val); err == nil && d > 0 {
			return d
		}
		if seconds, err := strconv.Atoi(val); err == nil && seconds > 0 {
			return time.Duration(seconds) * time.Second
		}
	}
	return fallback
}

func ParseLogLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
