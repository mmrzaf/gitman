package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mmrzaf/gitman/internal/ci"
)

func TestImagesPageShowsInventoryMissingCacheAndTabs(t *testing.T) {
	app, database, _, admin := setupApp(t)
	signIn(t, database, admin, "lead", true)
	ctx := t.Context()
	for _, query := range []string{
		`INSERT INTO repos (id, name) VALUES ('r1', 'app')`,
		`INSERT INTO refs (repo_id, kind, name, commit_hash) VALUES ('r1', 'branch', 'main', 'abc')`,
		`INSERT INTO ref_rules (id, repo_id, kind, pattern, run_on_push) VALUES ('rule1', 'r1', 'branch', 'main', true)`,
		`INSERT INTO workers (id, hostname, engine_id, ready, readiness_at, heartbeat_at)
		 VALUES ('w1', 'host', 'engine-1', true, now(), now())`,
	} {
		if _, err := database.Pool.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO runs (id, repo_id, number, commit_hash, trigger, status, ref_kind, ref_name, pipeline)
		VALUES ('run1', 'r1', 1, 'abc', 'push', 'queued', 'branch', 'main', $1)
	`, []byte("image: alpine:3.20\nimages:\n  app: {keep: 1}\nsteps:\n  - name: build\n    run: echo hi\n")); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := app.ci.PublishInventory(ctx, ci.HostInventory{
		EngineID: "engine-1", WorkerID: "w1", Hostname: "host",
		Images: []ci.InventoryImage{
			{Repository: "app", NormalizedRepository: "docker.io/library/app", Tag: "new", ID: "sha256:new", Size: 10, Created: now},
			{Repository: "app", NormalizedRepository: "docker.io/library/app", Tag: "old", ID: "sha256:old", Size: 10, Created: now.Add(-time.Hour)},
			{Repository: "alpine", NormalizedRepository: "docker.io/library/alpine", Tag: "3.20", ID: "sha256:alpine", Size: 5, Created: now},
			{Repository: "busybox", NormalizedRepository: "docker.io/library/busybox", Tag: "1", ID: "sha256:busybox", Size: 3, Created: now},
		},
		CacheSize: 4 << 30, CacheReclaimable: 1 << 30,
		Cache: ci.ImageCachePolicy{KeepGiB: 3, OlderThanHours: 168},
	}); err != nil {
		t.Fatal(err)
	}
	operation, _, err := app.ci.QueueImagePrune(ctx, "engine-1", "")
	if err != nil || operation == "" {
		t.Fatalf("QueueImagePrune = %q, %v", operation, err)
	}

	resp, body := admin.do(http.MethodGet, "/images", nil, nil)
	expect(t, resp, body, http.StatusOK, "Images", "host",
		"Missing required images", "alpine:3.20", "app run #1",
		"Cache", "4.0 GiB", "above its 3 GiB cap", "app:old", "prunable",
		"Required", "Built", "Other", "Prune built images", "Image operations")
	if !strings.Contains(body, `href="/images?plan=engine-1"`) {
		t.Error("the dry-run plan link is missing")
	}
	if !strings.Contains(body, `id="panel-built" data-tab-panel="built" hidden`) {
		t.Error("the built tab should be hidden by default")
	}
	if !strings.Contains(body, `id="panel-other" data-tab-panel="other" hidden`) {
		t.Error("the other tab should be hidden by default")
	}

	resp, body = admin.do(http.MethodGet, "/images?tab=built", nil, nil)
	expect(t, resp, body, http.StatusOK, "app:old", "prunable", "within 1", "outside 1", "built by app run #1")
	if !strings.Contains(body, `href="/images?tab=built" data-tab="built" aria-current="page"`) {
		t.Error("the built tab should be selected")
	}
	if !strings.Contains(body, `id="panel-required" data-tab-panel="required" hidden`) {
		t.Error("the required tab should be hidden")
	}

	resp, body = admin.do(http.MethodGet, "/images?tab=other", nil, nil)
	expect(t, resp, body, http.StatusOK, "busybox:1", "not declared")

	resp, body = admin.do(http.MethodGet, "/operations", nil, nil)
	expect(t, resp, body, http.StatusOK, "Image operations", "image.prune", "pending")

	resp, body = admin.do(http.MethodGet, "/images?plan=engine-1", nil, nil)
	expect(t, resp, body, http.StatusOK, "Plan for host", "Would remove", "app:old", "Prune these images", "within the newest 1 tags")
	if start := strings.Index(body, "Would remove"); start >= 0 {
		section := body[start:]
		if end := strings.Index(section, "protected or undeclared"); end >= 0 {
			section = section[:end]
		}
		if strings.Contains(section, "app:new") {
			t.Error("an image within keep appeared in the removal list")
		}
	}

	if _, err := database.Pool.Exec(ctx, `UPDATE image_hosts SET collected_at = now() - interval '10 minutes'`); err != nil {
		t.Fatal(err)
	}
	resp, body = admin.do(http.MethodGet, "/images", nil, nil)
	expect(t, resp, body, http.StatusOK, "Stale")
}

func TestImagesActionsRequireAdminAndQueueWork(t *testing.T) {
	_, database, _, admin := setupApp(t)
	signIn(t, database, admin, "lead", true)
	member := newBrowser(t, admin.server)
	signIn(t, database, member, "member", false)
	signedOut := newBrowser(t, admin.server)

	if err := ci.NewService(database).PublishInventory(t.Context(), ci.HostInventory{
		EngineID: "engine-1", Hostname: "host",
		Images: []ci.InventoryImage{{Repository: "app", NormalizedRepository: "docker.io/library/app", Tag: "1", ID: "sha256:1", Size: 10, Created: time.Now()}},
		Cache:  ci.ImageCachePolicy{KeepGiB: 3, OlderThanHours: 168},
	}); err != nil {
		t.Fatal(err)
	}

	for _, who := range []struct {
		name string
		b    *browser
	}{
		{"member", member},
		{"signed out", signedOut},
	} {
		resp, body := who.b.do(http.MethodGet, "/images", nil, nil)
		if who.name == "member" {
			expect(t, resp, body, http.StatusForbidden, "Only admins")
		} else if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("signed-out images = %d", resp.StatusCode)
		}
		resp, body = who.b.do(http.MethodPost, "/images/cache-prune", url.Values{"host": {"engine-1"}}, nil)
		if who.name == "member" {
			expect(t, resp, body, http.StatusForbidden, "Only admins")
		} else if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/login") {
			t.Fatalf("signed-out action = %d %q", resp.StatusCode, resp.Header.Get("Location"))
		}
	}

	resp, _ := admin.do(http.MethodPost, "/images/cache-prune", url.Values{"host": {"engine-1"}}, map[string]string{"Sec-Fetch-Site": "cross-site"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site action = %d", resp.StatusCode)
	}

	resp, _ = admin.do(http.MethodPost, "/images/cache-prune", url.Values{"host": {"engine-1"}}, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/images" {
		t.Fatalf("cache prune = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	var kind string
	if err := database.Pool.QueryRow(t.Context(), `SELECT kind FROM repository_operations ORDER BY created_at DESC LIMIT 1`).Scan(&kind); err != nil || kind != "cache.prune" {
		t.Fatalf("queued operation = %q, %v", kind, err)
	}

	resp, _ = admin.do(http.MethodPost, "/images/remove", url.Values{"host": {"engine-1"}, "image": {"app:1"}}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("remove = %d", resp.StatusCode)
	}
	if err := database.Pool.QueryRow(t.Context(), `SELECT kind FROM repository_operations ORDER BY created_at DESC LIMIT 1`).Scan(&kind); err != nil || kind != "image.remove" {
		t.Fatalf("removal operation = %q, %v", kind, err)
	}

	resp, _ = admin.do(http.MethodPost, "/images/prune", url.Values{"host": {"engine-1"}}, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/images" {
		t.Fatalf("prune redirect = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}
