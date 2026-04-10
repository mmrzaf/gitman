package config

import (
	"log"
	"log/slog"
	"os"
	"path/filepath"
)

type Config struct {
	Port         string
	DBPath       string
	ReposPath    string
	AuthKeysPath string
	BinaryPath   string // Auto-detected path to the running executable
	SSHUser      string // Usually 'git'
	ServerHost   string // e.g., 'git.example.com' or 'localhost'
	LogLevel     string
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
		Port:         getEnv("GITMAN_PORT", "8080"),
		DBPath:       getEnv("GITMAN_DB", ".data/db/gitman.sqlite"),
		ReposPath:    getEnv("GITMAN_REPOS", ".data/repos"),
		AuthKeysPath: getEnv("GITMAN_AUTH_KEYS", ".data/authorized_keys"),
		BinaryPath:   getEnv("GITMAN_BINARY_PATH", exePath),
		SSHUser:      getEnv("GITMAN_SSH_USER", "git"),
		ServerHost:   getEnv("GITMAN_SERVER_HOST", "localhost"),
		LogLevel:     getEnv("GITMAN_LOG_LEVEL", "debug"),
	}
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
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
