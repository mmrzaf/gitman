package ci

import (
	"testing"
	"time"

	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/postgres/pgtest"
)

func TestImageFactsUseQueuedAndLatestRuleRefPipelines(t *testing.T) {
	ctx := t.Context()
	database := pgtest.Open(t)
	seedRepo(t, database, "r1", "app")
	seedRepo(t, database, "r2", "runtime")

	for _, query := range []string{
		`INSERT INTO refs (repo_id, kind, name, commit_hash) VALUES
		 ('r1', 'branch', 'main', 'a1'), ('r1', 'branch', 'feature', 'a2'), ('r1', 'tag', 'v1', 'a3'),
		 ('r2', 'tag', 'v2', 'b1')`,
		`INSERT INTO ref_rules (id, repo_id, kind, pattern, run_on_push) VALUES
		 ('rule1', 'r1', 'branch', 'main', true), ('rule2', 'r1', 'tag', 'v*', false),
		 ('rule3', 'r2', 'tag', 'v*', true)`,
	} {
		if _, err := database.Pool.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}

	queued := `image: alpine:3.20
requires:
  - postgres:18
images:
  app: {keep: 2}
steps:
  - name: build
    run: echo queued
`
	main := `image: alpine:3.21
images:
  app: {keep: 3}
steps:
  - name: build
    run: echo main
`
	tagged := `image: debian:13
images:
  app: {keep: 1}
steps:
  - name: build
    run: echo tag
`
	unruled := `image: node:22
images:
  ignored: {keep: 1}
steps:
  - name: build
    run: echo feature
`
	runtime := `image: bullseye
images:
  runtime: {keep: 2}
steps:
  - name: build
    run: echo runtime
`
	for _, q := range []struct {
		query    string
		pipeline string
	}{
		{`INSERT INTO runs (id, repo_id, number, commit_hash, trigger, status, ref_kind, ref_name, pipeline)
		  VALUES ('run1', 'r1', 1, 'a1', 'push', 'queued', 'branch', 'main', $1)`, queued},
		{`INSERT INTO runs (id, repo_id, number, commit_hash, trigger, status, ref_kind, ref_name, pipeline, finished_at)
		  VALUES ('run2', 'r1', 2, 'a1', 'push', 'passed', 'branch', 'main', $1, now())`, main},
		{`INSERT INTO runs (id, repo_id, number, commit_hash, trigger, status, ref_kind, ref_name, pipeline, finished_at)
		  VALUES ('run3', 'r1', 3, 'a3', 'push', 'passed', 'tag', 'v1', $1, now())`, tagged},
		{`INSERT INTO runs (id, repo_id, number, commit_hash, trigger, status, ref_kind, ref_name, pipeline, finished_at)
		  VALUES ('run4', 'r1', 4, 'a2', 'push', 'passed', 'branch', 'feature', $1, now())`, unruled},
		{`INSERT INTO runs (id, repo_id, number, commit_hash, trigger, status, ref_kind, ref_name, pipeline, finished_at)
		  VALUES ('run5', 'r2', 1, 'b1', 'push', 'passed', 'tag', 'v2', $1, now())`, runtime},
	} {
		if _, err := database.Pool.Exec(ctx, q.query, []byte(q.pipeline)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO deployments (id, repo_id, target, version, commit_hash)
		VALUES ('d1', 'r1', 'production', 'v1', 'a3')
	`); err != nil {
		t.Fatal(err)
	}

	facts, err := NewService(database).ImageFacts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"alpine:3.20", "postgres:18", "alpine:3.21", "debian:13", "bullseye"} {
		if _, ok := facts.Required[NormalizeImageReference(ref)]; !ok {
			t.Errorf("required missing %s: %+v", ref, facts.Required)
		}
	}
	if _, ok := facts.Required[NormalizeImageReference("node:22")]; ok {
		t.Error("a ref matched by no rule must not contribute required images")
	}
	app := facts.Built[NormalizeImageRepository("app")]
	if app.Keep != 3 {
		t.Errorf("app keep = %d, want the larger 3", app.Keep)
	}
	if len(app.Sources) != 3 {
		t.Errorf("app sources = %+v, want queued, main and v1", app.Sources)
	}
	if len(app.Deployments) != 1 || app.Deployments[0] != "v1" {
		t.Errorf("app deployments = %v, want v1", app.Deployments)
	}
	if _, ok := facts.Built[NormalizeImageRepository("ignored")]; ok {
		t.Error("a built policy from an unruled ref leaked into facts")
	}
}

func TestImageFactsUseOnlyCurrentHeadPipeline(t *testing.T) {
	ctx := t.Context()
	database := pgtest.Open(t)
	seedRepo(t, database, "r1", "app")
	for _, query := range []string{
		`INSERT INTO refs (repo_id, kind, name, commit_hash) VALUES ('r1', 'branch', 'main', 'new')`,
		`INSERT INTO ref_rules (id, repo_id, kind, pattern, run_on_push) VALUES ('rule1', 'r1', 'branch', 'main', true)`,
	} {
		if _, err := database.Pool.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO runs (id, repo_id, number, commit_hash, trigger, status, ref_kind, ref_name, pipeline, finished_at)
		VALUES ('old', 'r1', 1, 'old', 'push', 'passed', 'branch', 'main', $1, now())
	`, []byte("image: alpine:3.20\nimages:\n  app: {keep: 1}\nsteps:\n  - name: build\n    run: echo hi\n")); err != nil {
		t.Fatal(err)
	}
	svc := NewService(database)
	facts, err := svc.ImageFacts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := facts.Built[NormalizeImageRepository("app")]; ok {
		t.Fatal("an older commit's pipeline leaked into current-head facts")
	}

	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO runs (id, repo_id, number, commit_hash, trigger, status, ref_kind, ref_name, pipeline, finished_at)
		VALUES ('new', 'r1', 2, 'new', 'push', 'passed', 'branch', 'main', $1, now())
	`, []byte("image: alpine:3.20\nimages:\n  app: {keep: 2}\nsteps:\n  - name: build\n    run: echo hi\n")); err != nil {
		t.Fatal(err)
	}
	facts, err = svc.ImageFacts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	app := facts.Built[NormalizeImageRepository("app")]
	if app.Keep != 2 {
		t.Fatalf("current-head app keep = %d, want 2", app.Keep)
	}
}

func TestPublishInventoryReplacesInPlace(t *testing.T) {
	ctx := t.Context()
	database := pgtest.Open(t)
	if _, err := database.Pool.Exec(ctx, `INSERT INTO workers (id, hostname, engine_id) VALUES ('w1', 'host', 'engine-1')`); err != nil {
		t.Fatal(err)
	}
	svc := NewService(database)
	now := time.Now()
	if err := svc.PublishInventory(ctx, HostInventory{
		EngineID: "engine-1", WorkerID: "w1", Hostname: "host",
		Images: []InventoryImage{{Repository: "app", NormalizedRepository: "docker.io/library/app", Tag: "1", ID: "sha256:1", Size: 10, Created: now}},
		Cache:  ImageCachePolicy{KeepGiB: 3, OlderThanHours: 168},
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.PublishInventory(ctx, HostInventory{
		EngineID: "engine-1", WorkerID: "w1", Hostname: "host",
		Images:    []InventoryImage{{Repository: "app", NormalizedRepository: "docker.io/library/app", Tag: "2", ID: "sha256:2", Size: 20, Created: now}},
		CacheSize: 100, Cache: ImageCachePolicy{KeepGiB: 5, OlderThanHours: 24},
		Automation: "after-deploy",
	}); err != nil {
		t.Fatal(err)
	}
	hosts, err := svc.ImageHosts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 || len(hosts[0].Images) != 1 || hosts[0].Images[0].Tag != "2" {
		t.Fatalf("hosts = %+v, want one replaced inventory", hosts)
	}
	if hosts[0].Cache.Policy.KeepGiB != 5 || hosts[0].Cache.Policy.OlderThanHours != 24 || hosts[0].Automation != "after-deploy" {
		t.Errorf("reported policy = %+v, automation %q", hosts[0].Cache.Policy, hosts[0].Automation)
	}
	var count int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM image_hosts`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("image_hosts rows = %d, %v; want 1", count, err)
	}

	previous, err := svc.RecordCachePrune(ctx, "engine-1", 123, "first failure")
	if err != nil || previous != "" {
		t.Fatalf("RecordCachePrune = %q, %v", previous, err)
	}
	previous, err = svc.RecordCachePrune(ctx, "engine-1", 456, "second failure")
	if err != nil || previous != "first failure" {
		t.Fatalf("RecordCachePrune previous = %q, %v", previous, err)
	}
	hosts, err = svc.ImageHosts(ctx)
	if err != nil || hosts[0].Cache.Reclaimed != 456 || hosts[0].Cache.Error != "second failure" || hosts[0].Cache.PrunedAt == nil {
		t.Fatalf("cache state = %+v, %v", hosts[0].Cache, err)
	}
}

func TestImageOperationClaimReleaseAndIdempotentFinish(t *testing.T) {
	ctx := t.Context()
	database := pgtest.Open(t)
	for _, q := range []string{
		`INSERT INTO workers (id, hostname, engine_id) VALUES ('w1', 'host', 'engine-1'), ('w2', 'host', 'engine-1')`,
		`INSERT INTO repository_operations (id, repo_id, kind, name, payload, engine_id)
		 VALUES ('op1', NULL, 'image.remove', 'engine-1', '{"targets":[{"ref":"app:old","id":"sha256:old"}]}', 'engine-1')`,
	} {
		if _, err := database.Pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	svc := NewService(database)
	operation, err := svc.ClaimImageOperation(ctx, "engine-1", "w1")
	if err != nil || operation == nil || operation.ID != "op1" || operation.Attempts != 1 {
		t.Fatalf("claim = %+v, %v", operation, err)
	}
	if second, err := svc.ClaimImageOperation(ctx, "engine-1", "w2"); err != nil || second != nil {
		t.Fatalf("a live lease was stolen: %+v, %v", second, err)
	}
	if err := svc.ReleaseImageOperation(ctx, "op1"); err != nil {
		t.Fatal(err)
	}
	operation, err = svc.ClaimImageOperation(ctx, "engine-1", "w2")
	if err != nil || operation == nil || operation.Attempts != 2 {
		t.Fatalf("released claim = %+v, %v", operation, err)
	}

	result := ImageOperationResult{Results: []ImageResult{{Ref: "app:old", Status: "removed", Bytes: 10}}, ReclaimedBytes: 10}
	if err := svc.FinishImageOperation(ctx, operation, result); err != nil {
		t.Fatal(err)
	}
	if err := svc.FinishImageOperation(ctx, operation, result); err != nil {
		t.Fatal(err)
	}
	finished, err := svc.ImageOperation(ctx, "op1")
	if err != nil || finished.Pending() || len(finished.Results) != 1 || finished.ReclaimedBytes != 10 {
		t.Fatalf("finished operation = %+v, %v", finished, err)
	}
	var events int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE action = 'image.removed'`).Scan(&events); err != nil || events != 1 {
		t.Fatalf("audit events = %d, %v; want one", events, err)
	}
}

func TestQueueImageOperationsRefuseStaleInventory(t *testing.T) {
	ctx := t.Context()
	database := pgtest.Open(t)
	if _, err := database.Pool.Exec(ctx, `INSERT INTO workers (id, hostname, engine_id) VALUES ('w1', 'host', 'engine-1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO image_hosts (host_id, worker_id, hostname, collected_at)
		VALUES ('engine-1', 'w1', 'host', now() - interval '10 minutes')
	`); err != nil {
		t.Fatal(err)
	}
	svc := NewService(database)
	if _, _, err := svc.QueueImagePrune(ctx, "engine-1", ""); apperr.KindOf(err) != apperr.KindConflict {
		t.Fatalf("stale prune error = %v, want conflict", err)
	}
	if _, err := svc.QueueImageCachePrune(ctx, "engine-1", "", nil); apperr.KindOf(err) != apperr.KindConflict {
		t.Fatalf("stale cache error = %v, want conflict", err)
	}
	if _, err := svc.QueueImageRemove(ctx, "engine-1", "app:1", ""); apperr.KindOf(err) != apperr.KindConflict {
		t.Fatalf("stale remove error = %v, want conflict", err)
	}
}
