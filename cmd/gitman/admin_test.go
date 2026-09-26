package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/postgres/pgtest"
	"github.com/mmrzaf/gitman/internal/worker/dockertest"
)

func TestWorkerCleanupCommandIsRegistered(t *testing.T) {
	if _, ok := adminGroups["worker"]["cleanup"]; !ok {
		t.Fatal(`expected a "worker cleanup" admin command to be registered`)
	}
}

func TestAdminWorkerCleanupRejectsArguments(t *testing.T) {
	if err := adminWorkerCleanup(t.Context(), &adminEnv{}, []string{"unexpected"}); err == nil {
		t.Fatal("expected an error for an unexpected argument")
	}
}

// TestAdminWorkerCleanupFailsLostRunsBeforeRemovingLeftovers is the
// host whose only worker was killed outright: nothing else there fails
// that worker's run, and a run still recorded as running keeps its
// container and workspace. The command must fail the run first, then
// remove what it left behind — and only among this instance's containers.
func TestAdminWorkerCleanupFailsLostRunsBeforeRemovingLeftovers(t *testing.T) {
	ctx := t.Context()
	database := pgtest.Open(t)
	if _, err := database.Pool.Exec(ctx, `INSERT INTO repos (id, name) VALUES ('r1', 'demo')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO workers (id, hostname, heartbeat_at) VALUES ('w1', 'host', now() - interval '10 minutes')
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO runs (id, repo_id, number, commit_hash, trigger, status, worker_id, started_at)
		VALUES ('run1', 'r1', 1, 'abc123', 'manual', 'running', 'w1', now() - interval '11 minutes')
	`); err != nil {
		t.Fatal(err)
	}
	fake := dockertest.New(t)
	t.Setenv("FAKE_DOCKER_PS", "c1 run1\n")
	cfg := &config.Config{DataDir: t.TempDir()}
	if err := os.MkdirAll(filepath.Join(cfg.WorkspacesPath(), "run1", "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	ciService := ci.NewService(database)
	var out bytes.Buffer
	env := &adminEnv{cfg: cfg, ci: ciService, out: &out, dockerBinary: fake.Binary}
	if err := adminWorkerCleanup(ctx, env, nil); err != nil {
		t.Fatalf("adminWorkerCleanup: %v", err)
	}

	var status string
	if err := database.Pool.QueryRow(ctx, `SELECT status FROM runs WHERE id = 'run1'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Errorf("run status = %q, want failed", status)
	}
	if got := out.String(); got != "Failed 1 runs, removed 1 containers and 1 workspaces.\n" {
		t.Errorf("output = %q", got)
	}
	instance, err := ciService.InstanceID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ps := fake.CallsTo(t, "ps")
	if len(ps) != 1 || !slices.Contains(ps[0], "label=gitman.instance="+instance) {
		t.Errorf("ps calls = %q; want one, filtered to this instance's containers", ps)
	}
	if rm := fake.CallsTo(t, "rm"); len(rm) != 1 || rm[0][len(rm[0])-1] != "c1" {
		t.Errorf("rm calls = %q; want c1 removed", rm)
	}
}
