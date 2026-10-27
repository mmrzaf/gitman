package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/postgres"
	"github.com/mmrzaf/gitman/internal/postgres/testschema"
)

func doctorDatabase(t *testing.T) string {
	t.Helper()
	dsn := testschema.DSN(t)
	ctx := context.Background()
	database, err := postgres.Connect(ctx, dsn, postgres.Options{MaxConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return dsn
}

func TestDoctorCommandIsRegistered(t *testing.T) {
	if _, ok := commands["doctor"]; !ok {
		t.Fatal(`expected a "doctor" command to be registered`)
	}
}

func TestDoctorHealthyInstallation(t *testing.T) {
	dsn := doctorDatabase(t)
	data := t.TempDir()
	for _, name := range []string{"repos", "hooks", "workspaces"} {
		if err := os.Mkdir(filepath.Join(data, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(config.EnvDatabaseURL, dsn)
	t.Setenv(config.EnvDataDir, data)
	t.Setenv(config.EnvPublicURL, "https://git.example.com")
	t.Setenv(config.EnvTrustedProxies, "127.0.0.1")
	t.Setenv(config.EnvSecretKey, "STsEYlF+KWuLHwa+R+yP7w5HqEwoKF2zUqpbukDA9PE=")

	var out bytes.Buffer
	if err := doctorTo(context.Background(), &out); err != nil {
		t.Fatalf("doctor on a healthy installation = %v\n%s", err, out.String())
	}
	for _, want := range []string{"ok    config:", "ok    data dir:", "ok    database:", "ok    schema:", "ok    secrets:"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output does not contain %q:\n%s", want, out.String())
		}
	}
}

func TestDoctorReportsAFailureAndStops(t *testing.T) {
	dsn := doctorDatabase(t)
	t.Setenv(config.EnvDatabaseURL, dsn)
	t.Setenv(config.EnvDataDir, filepath.Join(t.TempDir(), "missing"))
	t.Setenv(config.EnvPublicURL, "https://git.example.com")
	t.Setenv(config.EnvTrustedProxies, "127.0.0.1")

	var out bytes.Buffer
	err := doctorTo(context.Background(), &out)
	if !errors.Is(err, errDoctorFailed) {
		t.Fatalf("doctor with a missing data dir = %v, want errDoctorFailed", err)
	}
	if !strings.Contains(out.String(), "fail  data dir:") {
		t.Fatalf("missing data dir was not reported:\n%s", out.String())
	}
	// The database was still checked, so one failure does not hide the rest.
	if !strings.Contains(out.String(), "ok    database:") {
		t.Fatalf("database check was skipped:\n%s", out.String())
	}
}

func TestDoctorConfigErrorNamesTheVariable(t *testing.T) {
	t.Setenv(config.EnvDatabaseURL, "")
	t.Setenv(config.EnvDataDir, t.TempDir())
	t.Setenv(config.EnvPublicURL, "https://git.example.com")

	var out bytes.Buffer
	err := doctorTo(context.Background(), &out)
	if !errors.Is(err, errDoctorFailed) {
		t.Fatalf("doctor without a database URL = %v, want errDoctorFailed", err)
	}
	if !strings.Contains(out.String(), config.EnvDatabaseURL) {
		t.Fatalf("config failure does not name %s:\n%s", config.EnvDatabaseURL, out.String())
	}
}
