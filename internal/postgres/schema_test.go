package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mmrzaf/gitman/internal/postgres/pgtest"
)

// TestSchemaEnforcesTheDataModel writes rows the data model forbids,
// which only a bug could produce: the schema must refuse each one rather
// than trust every code path to get it right.
func TestSchemaEnforcesTheDataModel(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	for _, q := range []string{
		`INSERT INTO repos (id, name) VALUES ('r1', 'one'), ('r2', 'two')`,
		`INSERT INTO workers (id, hostname) VALUES ('w1', 'host')`,
		`INSERT INTO runs (id, repo_id, number, commit_hash, trigger, status, finished_at, ref_kind, ref_name)
		 VALUES ('run1', 'r1', 1, 'abc', 'push', 'passed', now(), 'branch', 'main')`,
	} {
		if _, err := database.Pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name, query string
	}{
		{"a rule pushable by named people that names nobody",
			`INSERT INTO ref_rules (id, repo_id, kind, pattern, push_policy) VALUES ('rule1', 'r1', 'branch', 'main', 'people')`},
		{"a run claimed by a worker that does not exist",
			`INSERT INTO runs (id, repo_id, number, commit_hash, trigger, status, started_at, worker_id, ref_kind, ref_name)
			 VALUES ('run2', 'r1', 2, 'abc', 'push', 'running', now(), 'no-such-worker', 'branch', 'main')`},
		{"a running run on no worker",
			`INSERT INTO runs (id, repo_id, number, commit_hash, trigger, status, started_at, ref_kind, ref_name)
			 VALUES ('run3', 'r1', 3, 'abc', 'push', 'running', now(), 'branch', 'main')`},
		{"a finished run with no finish time",
			`INSERT INTO runs (id, repo_id, number, commit_hash, trigger, status, ref_kind, ref_name) VALUES ('run4', 'r1', 4, 'abc', 'push', 'failed', 'branch', 'main')`},
		{"a queued run with a finish time",
			`INSERT INTO runs (id, repo_id, number, commit_hash, trigger, status, finished_at, ref_kind, ref_name)
			 VALUES ('run5', 'r1', 5, 'abc', 'push', 'queued', now(), 'branch', 'main')`},
		{"a queued run that has started",
			`INSERT INTO runs (id, repo_id, number, commit_hash, trigger, status, started_at, ref_kind, ref_name)
			 VALUES ('run6', 'r1', 6, 'abc', 'push', 'queued', now(), 'branch', 'main')`},
		{"a run of no branch or tag",
			`INSERT INTO runs (id, repo_id, number, commit_hash, trigger, status) VALUES ('run7', 'r1', 7, 'abc', 'manual', 'queued')`},
		{"a run of a branch with no name",
			`INSERT INTO runs (id, repo_id, number, commit_hash, trigger, status, ref_kind, ref_name) VALUES ('run8', 'r1', 8, 'abc', 'manual', 'queued', 'branch', '')`},
		{"a deployment of one repository made by another's run",
			`INSERT INTO deployments (id, repo_id, target, version, commit_hash, run_id)
			 VALUES ('d1', 'r2', 'staging', 'v1', 'abc', 'run1')`},
	} {
		_, err := database.Pool.Exec(ctx, c.query)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || (pgErr.Code != "23514" && pgErr.Code != "23503" && pgErr.Code != "23502") {
			t.Errorf("%s: err = %v; want a check, foreign key or not-null violation", c.name, err)
		}
	}

	// Pruning a run keeps its deployment, with no run attached.
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO deployments (id, repo_id, target, version, commit_hash, run_id) VALUES ('d2', 'r1', 'staging', 'v1', 'abc', 'run1')
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `DELETE FROM runs WHERE id = 'run1'`); err != nil {
		t.Fatal(err)
	}
	var repoID string
	var runID *string
	if err := database.Pool.QueryRow(ctx, `SELECT repo_id, run_id FROM deployments WHERE id = 'd2'`).Scan(&repoID, &runID); err != nil {
		t.Fatal(err)
	}
	if repoID != "r1" || runID != nil {
		t.Fatalf("deployment after its run was pruned: repo %q, run %v; want r1 and no run", repoID, runID)
	}
}
