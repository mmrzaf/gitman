package web

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestOwnVersion(t *testing.T) {
	commit := "3f2a91c0b7d84e5f6a1b2c3d4e5f60718293a4b5"
	for _, tc := range []struct {
		version string
		want    bool
	}{
		{commit[:12], false}, // what a branch deploy's version is
		{commit, false},
		{commit[:7], false},
		{"v1.0.0", true}, // a tag
		{"release-1.2", true},
		{"3f2a91c-rc1", true},
		{"", false},
	} {
		if got := ownVersion(tc.version, commit); got != tc.want {
			t.Errorf("ownVersion(%q, commit) = %v, want %v", tc.version, got, tc.want)
		}
	}
}

// TestADeploymentShowsItsVersionOnlyWhenItIsNotTheCommit ships a branch,
// whose version is its commit's first characters, and a tag, whose version
// is its name, and looks at every place a deployment is shown.
func TestADeploymentShowsItsVersionOnlyWhenItIsNotTheCommit(t *testing.T) {
	database, store, b := setupWithStore(t)
	ctx := context.Background()
	signIn(t, database, b, "darius", false)
	repo := seedFilesRepo(t, database, store, b)
	gitRepo := mustOpen(t, store, repo)
	main, tagged := mustResolve(t, gitRepo, "main"), mustResolve(t, gitRepo, "v1.0.0")
	person := mustPerson(t, database, "darius")

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := database.Pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO workers (id, hostname) VALUES ('w1', 'host')`)
	exec(`INSERT INTO runs (id, repo_id, number, commit_hash, ref_kind, ref_name, trigger, triggered_by, status, target, worker_id, started_at, finished_at)
	      VALUES ('r1', $1, 1, $2, 'branch', 'main', 'push', $3, 'passed', 'staging', 'w1', now(), now()),
	             ('r2', $1, 2, $4, 'tag', 'v1.0.0', 'push', $3, 'passed', 'production', 'w1', now(), now())`,
		repo.ID, main, person.ID, tagged)
	exec(`INSERT INTO deployments (id, repo_id, target, version, commit_hash, run_id, person_id)
	      VALUES ('d1', $1, 'staging', $2, $3, 'r1', $5), ('d2', $1, 'production', 'v1.0.0', $4, 'r2', $5)`,
		repo.ID, main[:12], main, tagged, person.ID)

	resp, body := b.do(http.MethodGet, "/waiotech", nil, nil)
	expect(t, resp, body, http.StatusOK,
		// The tag's version is shown as itself, in its card, its row and the feed.
		`<p class="target-version" title="v1.0.0">v1.0.0</p>`,
		// The branch's card leads with its commit, which says all there is to say.
		`<p class="target-version"><a class="hash" href="/waiotech/commit/`+main+`"`,
		`<span class="version">v1.0.0</span> <span class="muted">→</span> production`,
		`shipped <span class="version">v1.0.0</span> to <strong>production</strong>`,
		// The branch's is its commit, which is shown as a commit.
		`<td class="cell-shrink">staging</td>`,
		`shipped <a class="hash" href="/waiotech/commit/`+main+`" title="`+main+`">`+main[:7]+`</a> to <strong>staging</strong>`)
	for _, unwanted := range []string{`target-version" title="` + main[:12], `version">` + main[:12]} {
		if strings.Contains(body, unwanted) {
			t.Errorf("the Overview shows a branch deploy's commit as its version: %s", unwanted)
		}
	}
	if n := strings.Count(body, `class="version">`); n < 1 || strings.Contains(body, `class="version">`+main[:12]) {
		t.Errorf("the Overview's version labels are wrong (%d)", n)
	}

	resp, body = b.do(http.MethodGet, "/", nil, nil)
	expect(t, resp, body, http.StatusOK, `<dt>production</dt>`, `<span class="version">v1.0.0</span>`)
	if n := strings.Count(body, `class="version">`); n != 2 {
		// One on production's card, one in the feed's shipped line.
		t.Errorf("Home shows %d versions, want the tag's on its card and in the feed", n)
	}
	if strings.Contains(body, `class="version">`+main[:12]) {
		t.Error("Home shows a branch deploy's commit as its version")
	}
	expect(t, resp, body, http.StatusOK, `shipped <a class="hash" href="/waiotech/commit/`+main+`"`)
}
