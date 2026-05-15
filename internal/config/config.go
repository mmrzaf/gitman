package config

import (
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
)

type Config struct {
	Port              string
	DBPath            string
	ReposPath         string
	AuthKeysPath      string
	BinaryPath        string
	SSHUser           string
	ServerHost        string
	ArtifactsPath     string
	SecretKey         string
	InternalURL       string
	LogLevel          string
	AllowRegister     bool
	WorkerConcurrency int
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
		Port:              getEnv("GITMAN_PORT", "8080"),
		DBPath:            getEnv("GITMAN_DB", ".data/db/gitman.sqlite"),
		ReposPath:         getEnv("GITMAN_REPOS", ".data/repos"),
		AuthKeysPath:      getEnv("GITMAN_AUTH_KEYS", ".data/authorized_keys"),
		BinaryPath:        getEnv("GITMAN_BINARY_PATH", exePath),
		SSHUser:           getEnv("GITMAN_SSH_USER", "git"),
		ServerHost:        getEnv("GITMAN_SERVER_HOST", "localhost"),
		ArtifactsPath:     getEnv("GITMAN_ARTIFACTS", ".data/artifacts"),
		SecretKey:         getEnv("GITMAN_SECRET_KEY", ""),
		InternalURL:       getEnv("GITMAN_INTERNAL_URL", "http://localhost:8080"),
		LogLevel:          getEnv("GITMAN_LOG_LEVEL", "info"),
		AllowRegister:     getEnvBool("GITMAN_ALLOW_REGISTER", false),
		WorkerConcurrency: getEnvInt("GITMAN_WORKER_CONCURRENCY", 1),
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

func getEnvBool(key string, fallback bool) bool {
	if val, ok := os.LookupEnv(key); ok {
		if b, err := strconv.ParseBool(val); err == nil {
			return b
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
