package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestCompareChoosesTwoRefs(t *testing.T) {
	database, store, b := setupWithStore(t)
	signIn(t, database, b, "darius", false)
	repo := seedFilesRepo(t, database, store, b)
	head := mustResolve(t, mustOpen(t, store, repo), "main")

	// Without refs, the two pickers, offering every branch and tag.
	resp, body := b.do(http.MethodGet, "/waiotech/compare", nil, nil)
	expect(t, resp, body, http.StatusOK, `id="compare-title"`, "Choose two refs", `name="base"`, `name="head"`, `data-ref-picker`,
		`<option value="main">branch</option>`, `<option value="v1.0.0">tag</option>`, `<option value="release/1.2">tag</option>`)
	resp, body = b.do(http.MethodGet, "/waiotech/compare/", nil, nil)
	expect(t, resp, body, http.StatusOK, "Choose two refs")

	// Tag to tag goes to their comparison, which shows what the release added.
	resp, body = b.do(http.MethodGet, "/waiotech/compare?base=v1.0.0&head="+url.QueryEscape("release/1.2"), nil, nil)
	expect(t, resp, body, http.StatusSeeOther)
	target := resp.Header.Get("Location")
	if target != "/waiotech/compare/v1.0.0...release/1.2" {
		t.Fatalf("redirects to %q", target)
	}
	resp, body = b.do(http.MethodGet, target, nil, nil)
	expect(t, resp, body, http.StatusOK, `value="v1.0.0"`, `value="release/1.2"`, "What merging", "which.txt")
	resp, body = b.do(http.MethodGet, target+"?tab=commits", nil, nil)
	expect(t, resp, body, http.StatusOK, "On release/1.2")
	if strings.Contains(body, "Initial commit") {
		t.Error("the comparison lists a commit both tags have")
	}

	// A commit may be either side, abbreviated as in any address.
	resp, body = b.do(http.MethodGet, "/waiotech/compare?base="+head[:10]+"&head=release", nil, nil)
	expect(t, resp, body, http.StatusSeeOther)
	if got := resp.Header.Get("Location"); got != "/waiotech/compare/"+head[:10]+"...release" {
		t.Errorf("redirects to %q", got)
	}
	resp, body = b.do(http.MethodGet, resp.Header.Get("Location"), nil, nil)
	expect(t, resp, body, http.StatusOK, "On release")

	// A side that is not a ref, or only one side, is said on the pickers,
	// which keep what was typed.
	resp, body = b.do(http.MethodGet, "/waiotech/compare?base=main&head=nope", nil, nil)
	expect(t, resp, body, http.StatusUnprocessableEntity, `value="nope"`, `value="main"`, "That is not a branch, tag or commit of this repository.")
	resp, body = b.do(http.MethodGet, "/waiotech/compare?base=main", nil, nil)
	expect(t, resp, body, http.StatusUnprocessableEntity, `value="main"`, "Choose a branch, tag or commit.")
	for _, bad := range []string{"main/server", "a...b", "main..release", "--all"} {
		resp, body = b.do(http.MethodGet, "/waiotech/compare?base=main&head="+url.QueryEscape(bad), nil, nil)
		expect(t, resp, body, http.StatusUnprocessableEntity, "That is not a branch, tag or commit of this repository.")
	}
}

func TestOverviewCountsHowFarBranchesAreFromTheDefault(t *testing.T) {
	database, store, b := setupWithStore(t)
	signIn(t, database, b, "darius", false)
	repo := seedFilesRepo(t, database, store, b)
	bare, err := store.Path(repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, bare, "branch", "same", "main")
	// A branch one commit behind main and with nothing of its own.
	runGit(t, bare, "branch", "behind", "main~1")
	syncRepoRefs(t, database, store, repo.ID)

	resp, body := b.do(http.MethodGet, "/waiotech", nil, nil)
	expect(t, resp, body, http.StatusOK, `<th scope="col">vs main</th>`,
		// release has "On release" that main lacks, and lacks main's "Update README".
		`<a href="/waiotech/compare/main...release">1 ahead, 1 behind</a>`,
		`<a href="/waiotech/compare/main...same">even</a>`,
		`<a href="/waiotech/compare/main...behind">1 behind</a>`)
	if strings.Contains(body, `compare/main...main`) {
		t.Error("the default branch is compared with itself")
	}
	// Tags are not counted.
	tags := body[strings.Index(body, `id="panel-tags"`):]
	if strings.Contains(tags, "ahead") || strings.Contains(tags, "vs main") {
		t.Error("tags have ahead and behind counts")
	}
}
