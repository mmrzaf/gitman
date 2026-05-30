package config

import (
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type Config struct {
	Port               string
	DBPath             string
	ReposPath          string
	AuthKeysPath       string
	BinaryPath         string
	SSHUser            string
	ServerHost         string
	ArtifactsPath      string
	SecretKey          string
	InternalURL        string
	LogLevel           string
	AllowRegister      bool
	WorkerConcurrency  int
	ForceSecureCookies bool
	TrustProxyHeaders  bool

	CacheRoot          string
	MemoryLimit        string
	CPULimit           string
	CIJobTimeout       time.Duration
	CINetwork          string
	CIArtifactMaxBytes int64
	CIArtifactMaxFiles int
	CILogMaxBytes      int64
}

func LoadConfig() *Config {
	exePath, err := os.Executable()
	if err != nil {
		log.Fatalf("Failed to detect executable path: %v", err)
	}
	exePath, err = filepath.Abs(exePath)
	if err != nil {
		log.Fatalf("Failed to resolve absolute executable path: %v", err)
	}

	return &Config{
		Port:               getEnv("GITMAN_PORT", "8080"),
		DBPath:             getEnv("GITMAN_DB", ".data/db/gitman.sqlite"),
		ReposPath:          getEnv("GITMAN_REPOS", ".data/repos"),
		AuthKeysPath:       getEnv("GITMAN_AUTH_KEYS", ".data/authorized_keys"),
		BinaryPath:         getEnv("GITMAN_BINARY_PATH", exePath),
		SSHUser:            getEnv("GITMAN_SSH_USER", "git"),
		ServerHost:         getEnv("GITMAN_SERVER_HOST", "localhost"),
		ArtifactsPath:      getEnv("GITMAN_ARTIFACTS", ".data/artifacts"),
		SecretKey:          getEnv("GITMAN_SECRET_KEY", ""),
		InternalURL:        getEnv("GITMAN_INTERNAL_URL", "http://localhost:8080"),
		LogLevel:           getEnv("GITMAN_LOG_LEVEL", "info"),
		AllowRegister:      getEnvBool("GITMAN_ALLOW_REGISTER", false),
		WorkerConcurrency:  getEnvInt("GITMAN_WORKER_CONCURRENCY", 1),
		ForceSecureCookies: getEnvBool("GITMAN_FORCE_SECURE_COOKIES", false),
		TrustProxyHeaders:  getEnvBool("GITMAN_TRUST_PROXY_HEADERS", false),

		CacheRoot:          getEnv("GITMAN_CACHE_ROOT", ".data/ci/cache"),
		MemoryLimit:        getEnv("GITMAN_MEMORY_LIMIT", "512m"),
		CPULimit:           getEnv("GITMAN_CPU_LIMIT", "1"),
		CIJobTimeout:       getEnvDuration("GITMAN_CI_TIMEOUT", 30*time.Minute),
		CINetwork:          getEnv("GITMAN_CI_NETWORK", "none"),
		CIArtifactMaxBytes: getEnvInt64("GITMAN_CI_ARTIFACT_MAX_BYTES", 100*1024*1024),
		CIArtifactMaxFiles: getEnvInt("GITMAN_CI_ARTIFACT_MAX_FILES", 1000),
		CILogMaxBytes:      getEnvInt64("GITMAN_CI_LOG_MAX_BYTES", 10*1024*1024),
	}
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
