package config

import (
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
	return dir
}

func TestLoadValid(t *testing.T) {
	setBase(t)
	t.Setenv(EnvPublicURL, "https://git.example.com/")
	t.Setenv(EnvPort, "8090")

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
		"missing database URL":  {EnvDatabaseURL: ""},
		"invalid public URL":    {EnvPublicURL: "not-a-url"},
		"public URL with user":  {EnvPublicURL: "https://user@git.example.com"},
		"public URL with query": {EnvPublicURL: "https://git.example.com/?x=1"},
		"non-numeric port":      {EnvPort: "eighty"},
		"out-of-range port":     {EnvPort: "70000"},
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
