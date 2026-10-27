package worker

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/postgres/pgtest"
)

const (
	imageOne = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	imageTwo = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	imageOld = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
)

func setImageInventory(t *testing.T) {
	t.Helper()
	t.Setenv("FAKE_DOCKER_IMAGE_LS", strings.Join([]string{
		`{"Repository":"app","Tag":"new","Digest":"<none>","ID":"` + imageOne + `","CreatedAt":"2026-01-02T00:00:00Z","Size":"10MB"}`,
		`{"Repository":"app","Tag":"old","Digest":"<none>","ID":"` + imageOne + `","CreatedAt":"2026-01-01T00:00:00Z","Size":"10MB"}`,
		`{"Repository":"<none>","Tag":"<none>","Digest":"<none>","ID":"` + imageTwo + `","CreatedAt":"2026-01-01T00:00:00Z","Size":"5MB"}`,
	}, "\n"))
	t.Setenv("FAKE_DOCKER_IMAGE_INSPECT", strings.Join([]string{
		`{"Id":"` + imageOne + `","RepoTags":["app:new","app:old"],"RepoDigests":["app@sha256:abc"],"Created":"2026-01-01T00:00:00Z","Size":1000,"Metadata":{"LastTagTime":"2026-01-02T00:00:00Z"}}`,
		`{"Id":"` + imageTwo + `","RepoTags":[],"RepoDigests":[],"Created":"2026-01-01T00:00:00Z","Size":500,"Metadata":{"LastTagTime":"0001-01-01T00:00:00Z"}}`,
	}, "\n"))
	t.Setenv("FAKE_DOCKER_CONTAINER_IDS", "c1\nc2\n")
	t.Setenv("FAKE_DOCKER_CONTAINER_IMAGES", imageOne+"\n"+imageTwo+"\n")
	t.Setenv("FAKE_DOCKER_DF", strings.Join([]string{
		`{"Active":"2","Reclaimable":"1MB (20%)","Size":"5MB","TotalCount":"3","Type":"Images"}`,
		`{"Active":"1","Reclaimable":"512MB","Size":"1.5GB","TotalCount":"2","Type":"Build Cache"}`,
	}, "\n"))
}

func TestImageInventoryParsesTagsDanglingContainersAndCache(t *testing.T) {
	fake := newFakeDocker(t)
	setImageInventory(t)
	images, cacheSize, cacheReclaimable, err := fake.docker.ImageInventory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if cacheSize != 1_500_000_000 || cacheReclaimable != 512_000_000 {
		t.Fatalf("cache = %d, %d", cacheSize, cacheReclaimable)
	}
	if len(images) != 3 {
		t.Fatalf("images = %+v, want two tags and one dangling image", images)
	}
	byRef := map[string]ci.InventoryImage{}
	for _, image := range images {
		byRef[image.Ref()] = image
	}
	if image := byRef["app:new"]; image.ID != imageOne || !image.InUse || image.Size != 1000 || image.NormalizedRepository != "docker.io/library/app" {
		t.Errorf("app:new = %+v", image)
	}
	if image := byRef["app:old"]; image.Created.After(byRef["app:new"].Created) {
		t.Errorf("creation times not preserved: %+v", image)
	}
	dangling := byRef[imageTwo]
	if !dangling.Dangling() || !dangling.InUse || dangling.Size != 500 {
		t.Errorf("dangling image = %+v", dangling)
	}
	if len(byRef["app:new"].Digests) != 1 || byRef["app:new"].Digests[0] != "docker.io/library/app@sha256:abc" {
		t.Errorf("digests = %v", byRef["app:new"].Digests)
	}
}

func TestPruneBuildCacheArgumentsAndOutput(t *testing.T) {
	fake := newFakeDocker(t)
	t.Setenv("FAKE_DOCKER_BUILDER_PRUNE", "Total reclaimed space: 1.5GB\n")
	reclaimed, err := fake.docker.PruneBuildCache(t.Context(), 3, 168)
	if err != nil || reclaimed != 1_500_000_000 {
		t.Fatalf("PruneBuildCache = %d, %v", reclaimed, err)
	}
	calls := fake.CallsTo(t, "builder")
	if len(calls) != 1 {
		t.Fatalf("builder calls = %v", calls)
	}
	joined := strings.Join(calls[0], " ")
	// Without --all Docker prunes only dangling cache, which cannot meet a cap.
	for _, want := range []string{"prune --force --all", "--keep-storage 3GiB", "--filter until=168h"} {
		if !strings.Contains(joined, want) {
			t.Errorf("prune arguments lack %q: %s", want, joined)
		}
	}
	if _, err := fake.docker.PruneBuildCache(t.Context(), 3, 0); err != nil {
		t.Fatal(err)
	}
	if calls := fake.CallsTo(t, "builder"); len(calls) != 2 || strings.Contains(strings.Join(calls[1], " "), "until=") {
		t.Errorf("cap-only call = %v", calls)
	}
}

func TestRemoveImageIsIdempotentAndDoesNotForce(t *testing.T) {
	fake := newFakeDocker(t)
	if err := fake.docker.RemoveImage(t.Context(), "app:old"); err != nil {
		t.Fatal(err)
	}
	calls := fake.CallsTo(t, "image")
	joined := strings.Join(calls[0], " ")
	if !strings.Contains(joined, "rm -- app:old") || strings.Contains(joined, "--force") {
		t.Fatalf("image rm arguments = %s", joined)
	}
	t.Setenv("FAKE_DOCKER_IMAGE_RM_MISSING", "1")
	if err := fake.docker.RemoveImage(t.Context(), "app:gone"); err != nil {
		t.Fatalf("an absent image must be success on retry: %v", err)
	}
}

func TestHostExecutionLockSerializesRunsAndImageWork(t *testing.T) {
	root := t.TempDir()
	releaseExclusive, ok, err := hostExecutionLock(root, true)
	if err != nil || !ok {
		t.Fatalf("exclusive lock = %v, %v", ok, err)
	}
	if _, ok, _ := hostExecutionLock(root, false); ok {
		t.Fatal("a run took a shared lock while image work held it exclusively")
	}
	releaseExclusive()
	releaseShared, ok, err := hostExecutionLock(root, false)
	if err != nil || !ok {
		t.Fatalf("shared lock = %v, %v", ok, err)
	}
	if _, ok, _ := hostExecutionLock(root, true); ok {
		t.Fatal("image work took the exclusive lock while a run was active")
	}
	releaseShared()
}

func TestImageOperationRevalidatesAndRecordsResults(t *testing.T) {
	ctx := t.Context()
	database := pgtest.Open(t)
	if _, err := database.Pool.Exec(ctx, `INSERT INTO repos (id, name) VALUES ('r1', 'app')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `INSERT INTO workers (id, hostname, engine_id) VALUES ('w1', 'host', 'fake-engine')`); err != nil {
		t.Fatal(err)
	}
	pipeline := []byte(`image: alpine:3.20
images:
  app: {keep: 1}
steps:
  - name: build
    run: echo hi
`)
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
	`, pipeline); err != nil {
		t.Fatal(err)
	}
	svc := ci.NewService(database)
	now := time.Now()
	if err := svc.PublishInventory(ctx, ci.HostInventory{
		EngineID: "fake-engine", WorkerID: "w1", Hostname: "host",
		Images: []ci.InventoryImage{
			{Repository: "app", NormalizedRepository: "docker.io/library/app", Tag: "new", ID: imageOne, Size: 1000, Created: now},
			{Repository: "app", NormalizedRepository: "docker.io/library/app", Tag: "old", ID: imageOld, Size: 1000, Created: now.Add(-time.Hour)},
		},
		Cache: ci.ImageCachePolicy{KeepGiB: 3, OlderThanHours: 168},
	}); err != nil {
		t.Fatal(err)
	}
	id, plan, err := svc.QueueImagePrune(ctx, "fake-engine", "")
	if err != nil || id == "" || len(plan.Remove) != 1 || plan.Remove[0].Tag != "old" {
		t.Fatalf("QueueImagePrune = %q, %+v, %v", id, plan, err)
	}

	fake := newFakeDocker(t)
	setImageInventory(t)
	t.Setenv("FAKE_DOCKER_IMAGE_LS", strings.Join([]string{
		`{"Repository":"app","Tag":"new","ID":"` + imageOne + `","CreatedAt":"2026-01-02T00:00:00Z","Size":"10MB"}`,
		`{"Repository":"app","Tag":"old","ID":"` + imageOld + `","CreatedAt":"2026-01-01T00:00:00Z","Size":"10MB"}`,
	}, "\n"))
	t.Setenv("FAKE_DOCKER_IMAGE_INSPECT", strings.Join([]string{
		`{"Id":"` + imageOne + `","RepoTags":["app:new"],"Created":"2026-01-02T00:00:00Z","Size":1000}`,
		`{"Id":"` + imageOld + `","RepoTags":["app:old"],"Created":"2026-01-01T00:00:00Z","Size":1000}`,
	}, "\n"))
	t.Setenv("FAKE_DOCKER_CONTAINER_IDS", "")
	t.Setenv("FAKE_DOCKER_CONTAINER_IMAGES", "")
	root := t.TempDir()
	w := New(Config{WorkspaceRoot: root, ImageCache: config.DefaultImageCache()}, database, svc, nil, fake.docker, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.id = "w1"
	w.runImageOperations(ctx)

	operation, err := svc.ImageOperation(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if operation.Pending() || len(operation.Results) != 1 || operation.Results[0].Status != "removed" || operation.Results[0].Ref != "app:old" {
		t.Fatalf("operation = %+v", operation)
	}
	calls := fake.CallsTo(t, "image")
	found := false
	for _, call := range calls {
		if strings.Join(call, " ") == "image rm -- app:old" {
			found = true
		}
	}
	if !found {
		t.Fatalf("image rm calls = %v", calls)
	}
	var detail, action string
	if err := database.Pool.QueryRow(ctx, `SELECT action, detail FROM events ORDER BY created_at DESC LIMIT 1`).Scan(&action, &detail); err != nil {
		t.Fatal(err)
	}
	if action != "image.pruned" || !strings.Contains(detail, "app:old removed") {
		t.Fatalf("audit event = %q, %q", action, detail)
	}
}

func TestImageOperationsDoNotRunDuringMaintenance(t *testing.T) {
	ctx := t.Context()
	database := pgtest.Open(t)
	if _, err := database.Pool.Exec(ctx, `INSERT INTO workers (id, hostname, engine_id) VALUES ('w1', 'host', 'fake-engine')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `UPDATE instance SET maintenance = true`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO repository_operations (id, repo_id, kind, name, payload, engine_id)
		VALUES ('op1', NULL, 'image.remove', 'fake-engine', '{"targets":[{"ref":"app:1","id":"sha256:x"}]}', 'fake-engine')
	`); err != nil {
		t.Fatal(err)
	}
	svc := ci.NewService(database)
	fake := newFakeDocker(t)
	w := New(Config{WorkspaceRoot: t.TempDir()}, database, svc, nil, fake.docker, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.id = "w1"
	w.runImageOperations(ctx)
	if calls := fake.CallsTo(t, "image"); len(calls) != 0 {
		t.Fatalf("maintenance ran image work: %v", calls)
	}
	op, err := svc.ImageOperation(ctx, "op1")
	if err != nil || !op.Pending() {
		t.Fatalf("operation = %+v, %v; want pending", op, err)
	}
}

func TestLowDiskPolicyDoesNotRunWithoutPressure(t *testing.T) {
	database := pgtest.Open(t)
	fake := newFakeDocker(t)
	svc := ci.NewService(database)
	resources := config.DefaultResources()
	resources.DiskReserveGiB = 1
	w := New(Config{WorkspaceRoot: t.TempDir(), Resources: resources}, database, svc, nil, fake.docker, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if w.lowDisk(t.Context()) {
		t.Fatal("low-disk automation acted with plenty of free space")
	}
}

func TestImageOperationOnShutdownRecordsInterruption(t *testing.T) {
	ctx := t.Context()
	database := pgtest.Open(t)
	if _, err := database.Pool.Exec(ctx, `INSERT INTO workers (id, hostname, engine_id) VALUES ('w1', 'host', 'fake-engine')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO repository_operations (id, repo_id, kind, name, payload, engine_id)
		VALUES ('op1', NULL, 'image.remove', 'fake-engine', '{"targets":[{"ref":"app:1","id":"sha256:1"}]}', 'fake-engine')
	`); err != nil {
		t.Fatal(err)
	}
	svc := ci.NewService(database)
	fake := newFakeDocker(t)
	w := New(Config{WorkspaceRoot: t.TempDir()}, database, svc, nil, fake.docker, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.id = "w1"
	operation, err := svc.ClaimImageOperation(ctx, "fake-engine", "w1")
	if err != nil || operation == nil {
		t.Fatalf("claim = %+v, %v", operation, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	w.executeImageOperation(cancelled, operation)
	finished, err := svc.ImageOperation(ctx, "op1")
	if err != nil {
		t.Fatal(err)
	}
	if finished.Pending() || !strings.Contains(finished.Error, "shut down") {
		t.Fatalf("interrupted operation = %+v", finished)
	}
}
