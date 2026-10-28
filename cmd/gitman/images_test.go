package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/config"
)

func TestImageCommandsAreRegistered(t *testing.T) {
	for _, action := range []string{"list", "missing", "prune", "remove"} {
		if _, ok := adminGroups["images"][action]; !ok {
			t.Errorf("expected a %q images command to be registered", action)
		}
	}
}

func publishTestInventory(t *testing.T, env *adminEnv, images ...ci.InventoryImage) {
	t.Helper()
	if err := env.ci.PublishInventory(t.Context(), ci.HostInventory{
		EngineID: "engine-1", Hostname: "host",
		Images: images,
		Cache:  ci.ImageCachePolicy{KeepGiB: 3, OlderThanHours: 168},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAdminImagesListJSONAndClassFilter(t *testing.T) {
	env, _, _ := newAdminEnv(t)
	now := time.Now()
	publishTestInventory(t, env,
		ci.InventoryImage{Repository: "app", NormalizedRepository: "docker.io/library/app", Tag: "1", ID: "sha256:1", Size: 10, Created: now},
		ci.InventoryImage{Repository: "other", NormalizedRepository: "docker.io/library/other", Tag: "2", ID: "sha256:2", Size: 20, Created: now},
	)
	if err := adminImagesList(t.Context(), env, []string{"--json"}); err != nil {
		t.Fatal(err)
	}
	var hosts []ci.ImageHost
	if err := json.Unmarshal([]byte(outputOf(env)), &hosts); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, outputOf(env))
	}
	if len(hosts) != 1 || len(hosts[0].Images) != 2 || hosts[0].Images[0].Class != ci.ImageOther {
		t.Fatalf("hosts = %+v", hosts)
	}
	env.out.(*bytes.Buffer).Reset()
	if err := adminImagesList(t.Context(), env, []string{"--class", "built"}); err != nil {
		t.Fatal(err)
	}
	if got := outputOf(env); strings.Contains(got, "app:1") || strings.Contains(got, "other:2") {
		t.Fatalf("class filter included non-built images:\n%s", got)
	}
	if err := adminImagesList(t.Context(), env, []string{"--class", "nope"}); err == nil {
		t.Fatal("an unknown class was accepted")
	}
}

func TestAdminImagesMissingExitsNonZero(t *testing.T) {
	env, database, _ := newAdminEnv(t)
	ctx := t.Context()
	if _, err := database.Pool.Exec(ctx, `INSERT INTO repos (id, name) VALUES ('r1', 'app')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO refs (repo_id, kind, name, commit_hash) VALUES ('r1', 'branch', 'main', 'abc')
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO ref_rules (id, repo_id, kind, pattern, run_on_push) VALUES ('rule1', 'r1', 'branch', 'main', true)
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO runs (id, repo_id, number, commit_hash, trigger, status, ref_kind, ref_name, pipeline)
		VALUES ('run1', 'r1', 1, 'abc', 'push', 'queued', 'branch', 'main', $1)
	`, []byte("image: alpine:3.20\nsteps:\n  - name: build\n    run: echo hi\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO workers (id, hostname, engine_id, ready, readiness_at, heartbeat_at)
		VALUES ('w1', 'host', 'engine-1', true, now(), now())
	`); err != nil {
		t.Fatal(err)
	}
	if err := adminImagesMissing(ctx, env, nil); err == nil {
		t.Fatal("missing required images did not fail the command")
	}
	if got := outputOf(env); !strings.Contains(got, "alpine:3.20") || !strings.Contains(got, "app run #1") {
		t.Fatalf("missing output = %q", got)
	}
	if _, err := database.Pool.Exec(ctx, `UPDATE workers SET images = ARRAY['docker.io/library/alpine:3.20']`); err != nil {
		t.Fatal(err)
	}
	env.out.(*bytes.Buffer).Reset()
	if err := adminImagesMissing(ctx, env, nil); err != nil {
		t.Fatalf("a satisfied required image still failed: %v", err)
	}
	if got := outputOf(env); !strings.Contains(got, "No required images are missing") {
		t.Fatalf("output = %q", got)
	}
}

func TestAdminImagesPruneDryRunAndConfirm(t *testing.T) {
	env, database, _ := newAdminEnv(t)
	ctx := t.Context()
	if _, err := database.Pool.Exec(ctx, `INSERT INTO repos (id, name) VALUES ('r1', 'app')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO refs (repo_id, kind, name, commit_hash) VALUES ('r1', 'branch', 'main', 'abc')
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO ref_rules (id, repo_id, kind, pattern, run_on_push) VALUES ('rule1', 'r1', 'branch', 'main', true)
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO runs (id, repo_id, number, commit_hash, trigger, status, ref_kind, ref_name, pipeline)
		VALUES ('run1', 'r1', 1, 'abc', 'push', 'queued', 'branch', 'main', $1)
	`, []byte("image: alpine:3.20\nimages:\n  app: {keep: 1}\nsteps:\n  - name: build\n    run: echo hi\n")); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	publishTestInventory(t, env,
		ci.InventoryImage{Repository: "app", NormalizedRepository: "docker.io/library/app", Tag: "new", ID: "sha256:new", Size: 10, Created: now},
		ci.InventoryImage{Repository: "app", NormalizedRepository: "docker.io/library/app", Tag: "old", ID: "sha256:old", Size: 10, Created: now.Add(-time.Hour)},
	)
	if err := adminImagesPrune(ctx, env, nil); err != nil {
		t.Fatal(err)
	}
	if got := outputOf(env); !strings.Contains(got, "Would remove 1 image") || !strings.Contains(got, "app:old") {
		t.Fatalf("dry-run output = %q", got)
	}
	var pending int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM repository_operations WHERE kind = 'image.prune'`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("dry-run queued %d operations, %v", pending, err)
	}
	env.out.(*bytes.Buffer).Reset()
	if err := adminImagesPrune(ctx, env, []string{"--confirm"}); err != nil {
		t.Fatal(err)
	}
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM repository_operations WHERE kind = 'image.prune' AND completed_at IS NULL`).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("confirmed prune queued %d operations, %v", pending, err)
	}
	if got := outputOf(env); !strings.Contains(got, "Queued pruning 1 image") {
		t.Fatalf("confirm output = %q", got)
	}
}

func TestAdminImagesRemoveRefusesRequiredAndQueuesOther(t *testing.T) {
	env, database, _ := newAdminEnv(t)
	ctx := t.Context()
	if _, err := database.Pool.Exec(ctx, `INSERT INTO repos (id, name) VALUES ('r1', 'app')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO refs (repo_id, kind, name, commit_hash) VALUES ('r1', 'branch', 'main', 'abc')
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO ref_rules (id, repo_id, kind, pattern, run_on_push) VALUES ('rule1', 'r1', 'branch', 'main', true)
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO runs (id, repo_id, number, commit_hash, trigger, status, ref_kind, ref_name, pipeline)
		VALUES ('run1', 'r1', 1, 'abc', 'push', 'queued', 'branch', 'main', $1)
	`, []byte("image: alpine:3.20\nimages:\n  app: {keep: 1}\nsteps:\n  - name: build\n    run: echo hi\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO workers (id, hostname, engine_id, ready, readiness_at, heartbeat_at)
		VALUES ('w1', 'host', 'engine-1', true, now(), now())
	`); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	publishTestInventory(t, env,
		ci.InventoryImage{Repository: "app", NormalizedRepository: "docker.io/library/app", Tag: "new", ID: "sha256:new", Size: 10, Created: now},
		ci.InventoryImage{Repository: "app", NormalizedRepository: "docker.io/library/app", Tag: "old", ID: "sha256:old", Size: 10, Created: now.Add(-time.Hour)},
	)
	if err := adminImagesRemove(ctx, env, []string{"w1", "app:old"}); err != nil {
		t.Fatal(err)
	}
	if got := outputOf(env); !strings.Contains(got, "Would remove app:old") {
		t.Fatalf("dry-run output = %q", got)
	}
	env.out.(*bytes.Buffer).Reset()
	if err := adminImagesRemove(ctx, env, []string{"--confirm", "w1", "app:old"}); err != nil {
		t.Fatal(err)
	}
	var pending int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM repository_operations WHERE kind = 'image.remove'`).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("removal operations = %d, %v", pending, err)
	}

	// A required image is refused even when explicitly named.
	if _, err := database.Pool.Exec(ctx, `UPDATE runs SET pipeline = $1`, []byte("image: app:new\nsteps:\n  - name: build\n    run: echo hi\n")); err != nil {
		t.Fatal(err)
	}
	env.out.(*bytes.Buffer).Reset()
	err := adminImagesRemove(ctx, env, []string{"--confirm", "w1", "app:new"})
	if err == nil || !strings.Contains(err.Error(), "required by") {
		t.Fatalf("required removal error = %v", err)
	}
}

func TestAdminImagesCachePrune(t *testing.T) {
	env, database, _ := newAdminEnv(t)
	env.cfg.ImageCache = config.DefaultImageCache()
	publishTestInventory(t, env)
	if err := adminImagesPrune(t.Context(), env, []string{"--cache"}); err != nil {
		t.Fatal(err)
	}
	if got := outputOf(env); !strings.Contains(got, "policy 3 GiB, 168 hours") {
		t.Fatalf("cache dry-run output = %q", got)
	}
	env.out.(*bytes.Buffer).Reset()
	if err := adminImagesPrune(t.Context(), env, []string{"--cache", "--confirm", "--keep-storage", "5", "--older-than", "24"}); err != nil {
		t.Fatal(err)
	}
	var kind, payload string
	if err := database.Pool.QueryRow(t.Context(), `
		SELECT kind, payload::text FROM repository_operations WHERE kind = 'cache.prune'
	`).Scan(&kind, &payload); err != nil {
		t.Fatal(err)
	}
	if kind != "cache.prune" || !strings.Contains(payload, `"keep_gib": 5`) || !strings.Contains(payload, `"older_than_hours": 24`) {
		t.Fatalf("cache operation = %q, %s", kind, payload)
	}
}

func TestAdminImagesMissingFailsWhenFactsAreIncomplete(t *testing.T) {
	env, database, _ := newAdminEnv(t)
	ctx := t.Context()
	if _, err := database.Pool.Exec(ctx, `INSERT INTO repos (id, name) VALUES ('r1', 'app')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO runs (id, repo_id, number, commit_hash, trigger, status, ref_kind, ref_name, pipeline)
		VALUES ('run1', 'r1', 1, 'abc', 'push', 'queued', 'branch', 'main', $1)
	`, []byte("image: [")); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO workers (id, hostname, engine_id, ready, readiness_at, heartbeat_at)
		VALUES ('w1', 'host', 'engine-1', true, now(), now())
	`); err != nil {
		t.Fatal(err)
	}
	if err := adminImagesMissing(ctx, env, nil); err == nil {
		t.Fatal("missing-image check passed with incomplete facts")
	}
	if got := outputOf(env); !strings.Contains(got, "cannot be trusted") {
		t.Fatalf("output = %q", got)
	}
}
