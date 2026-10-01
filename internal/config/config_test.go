package config

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func setBase(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(EnvDatabaseURL, "postgres://gitman:secret@localhost:5432/gitman")
	t.Setenv(EnvDataDir, dir)
	t.Setenv(EnvPublicURL, "")
	t.Setenv(EnvPort, "")
	t.Setenv(EnvTrustedProxies, "")
	t.Setenv(EnvSecretKey, "")
	t.Setenv(EnvWebURL, "")
	t.Setenv(EnvRetentionDays, "")
	t.Setenv(EnvDatabaseMaxConns, "")
	t.Setenv(EnvLogLevel, "")
	t.Setenv(EnvLogFormat, "")
	return dir
}

func TestLoadValid(t *testing.T) {
	setBase(t)
	t.Setenv(EnvPublicURL, "https://git.example.com/")
	t.Setenv(EnvPort, "8090")

	t.Setenv(EnvDatabaseMaxConns, "25")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PublicURL != "https://git.example.com" {
		t.Errorf("PublicURL = %q, want the trailing slash trimmed", cfg.PublicURL)
	}
	if cfg.Port != 8090 {
		t.Errorf("Port = %d, want 8090", cfg.Port)
	}
	if cfg.DatabaseMaxConns != 25 {
		t.Errorf("DatabaseMaxConns = %d, want 25", cfg.DatabaseMaxConns)
	}
}

func TestLoadDefaults(t *testing.T) {
	setBase(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PublicURL != "http://localhost:8080" {
		t.Errorf("PublicURL default = %q", cfg.PublicURL)
	}
	if cfg.Port != 8080 {
		t.Errorf("Port default = %d", cfg.Port)
	}
	if cfg.DatabaseMaxConns != 0 {
		t.Errorf("DatabaseMaxConns default = %d, want 0 (the process's own default)", cfg.DatabaseMaxConns)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel default = %q, want %q", cfg.LogLevel, "info")
	}
	if cfg.LogFormat != "text" {
		t.Errorf("LogFormat default = %q, want %q", cfg.LogFormat, "text")
	}
}

func TestLoadMakesDataDirAbsolute(t *testing.T) {
	setBase(t)
	t.Setenv(EnvDataDir, "relative-data")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !filepath.IsAbs(cfg.DataDir) {
		t.Errorf("DataDir = %q, want an absolute path", cfg.DataDir)
	}
}

func TestLoadRejects(t *testing.T) {
	cases := map[string]map[string]string{
		"missing database URL":   {EnvDatabaseURL: ""},
		"invalid public URL":     {EnvPublicURL: "not-a-url"},
		"public URL with user":   {EnvPublicURL: "https://user@git.example.com"},
		"public URL with query":  {EnvPublicURL: "https://git.example.com/?x=1"},
		"non-numeric port":       {EnvPort: "eighty"},
		"out-of-range port":      {EnvPort: "70000"},
		"non-numeric max conns":  {EnvDatabaseMaxConns: "many"},
		"negative max conns":     {EnvDatabaseMaxConns: "-1"},
		"out-of-range max conns": {EnvDatabaseMaxConns: "1001"},
		"invalid log level":      {EnvLogLevel: "verbose"},
		"invalid log format":     {EnvLogFormat: "xml"},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			setBase(t)
			for k, v := range env {
				t.Setenv(k, v)
			}
			if _, err := Load(); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestDerivedPaths(t *testing.T) {
	cfg := &Config{DataDir: "/srv/gitman"}
	if got := cfg.ReposPath(); got != "/srv/gitman/repos" {
		t.Errorf("ReposPath() = %q", got)
	}
	if got := cfg.HooksPath(); got != "/srv/gitman/hooks" {
		t.Errorf("HooksPath() = %q", got)
	}
	if got := cfg.WorkspacesPath(); got != "/srv/gitman/workspaces" {
		t.Errorf("WorkspacesPath() = %q", got)
	}
}

func TestTrustedProxies(t *testing.T) {
	setBase(t)
	t.Setenv(EnvTrustedProxies, " 172.16.0.0/12, 10.1.2.3 ,,::1")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range cfg.TrustedProxies {
		got = append(got, p.String())
	}
	want := "172.16.0.0/12 10.1.2.3/32 ::1/128"
	if strings.Join(got, " ") != want {
		t.Errorf("TrustedProxies = %v, want %s", got, want)
	}
}

func TestWebURLDefaultsToPublicURL(t *testing.T) {
	setBase(t)
	t.Setenv(EnvPublicURL, "https://git.example.com/")
	cfg, err := Load()
	if err != nil || cfg.WebURL != "https://git.example.com" {
		t.Fatalf("WebURL = %q, %v", cfg.WebURL, err)
	}
	t.Setenv(EnvWebURL, "http://web:8080/")
	cfg, err = Load()
	if err != nil || cfg.WebURL != "http://web:8080" {
		t.Fatalf("WebURL = %q, %v", cfg.WebURL, err)
	}
}

func TestRetentionDays(t *testing.T) {
	setBase(t)
	cfg, err := Load()
	if err != nil || cfg.RetentionDays != 90 {
		t.Fatalf("default RetentionDays = %d, %v", cfg.RetentionDays, err)
	}
	t.Setenv(EnvRetentionDays, "0")
	if cfg, err = Load(); err != nil || cfg.RetentionDays != 0 {
		t.Fatalf("RetentionDays=0 (keep forever) = %d, %v", cfg.RetentionDays, err)
	}
}

func TestNewLogger(t *testing.T) {
	var buf bytes.Buffer
	cfg := &Config{LogLevel: "warn", LogFormat: "json"}
	log := cfg.NewLogger(&buf)
	log.Info("should not appear, below warn")
	log.Warn("should appear", "key", "value")
	out := buf.String()
	if strings.Contains(out, "should not appear") {
		t.Errorf("info-level message logged despite LogLevel=warn: %s", out)
	}
	if !strings.Contains(out, `"msg":"should appear"`) || !strings.Contains(out, `"key":"value"`) {
		t.Errorf("expected a JSON-formatted warn line, got: %s", out)
	}

	buf.Reset()
	cfg = &Config{LogLevel: "info", LogFormat: "text"}
	cfg.NewLogger(&buf).Info("hello", "key", "value")
	if out := buf.String(); !strings.Contains(out, "msg=hello") || !strings.Contains(out, "key=value") {
		t.Errorf("expected a text-formatted info line, got: %s", out)
	}
}

func TestResourceProfile(t *testing.T) {
	setBase(t)
	t.Setenv(EnvStepMemoryMiB, "8192")
	t.Setenv(EnvStepCPUs, "8")
	t.Setenv(EnvStepPIDs, "1024")
	t.Setenv(EnvWorkspaceGiB, "100")
	t.Setenv(EnvDiskReserveGiB, "20")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Resources != (Resources{8192, 8, 1024, 100, 20}) {
		t.Fatalf("resources=%+v", cfg.Resources)
	}
	t.Setenv(EnvStepMemoryMiB, "0")
	if _, err := Load(); err == nil {
		t.Fatal("zero memory limit accepted")
	}
}
