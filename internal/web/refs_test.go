package web

import (
	"context"
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/mmrzaf/gitman/internal/auth"
	"github.com/mmrzaf/gitman/internal/git"
	reposvc "github.com/mmrzaf/gitman/internal/repo"
)

// signInTo signs a person in to a test server and returns their browser.
func signInTo(t *testing.T, e *gitHTTPEnv, username string) *browser {
	t.Helper()
	b := newBrowser(t, e.server)
	if resp, body := login(b, username, "correct-horse-battery", "/"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("sign in as %s: %d\n%s", username, resp.StatusCode, body)
	}
	return b
}

// pushDir is a repository to push from as one person; deleting a ref needs
// no commits of its own.
func (e *gitHTTPEnv) pushDir(c gitCredential) string {
	e.t.Helper()
	dir := e.t.TempDir()
	e.mustGit(".", "init", "--quiet", "--initial-branch=main", dir)
	e.mustGit(dir, "remote", "add", "origin", e.url(c))
	return dir
}

// refExists reports whether the repository has the ref, asked of Git
// itself.
func (e *gitHTTPEnv) refExists(full string) bool {
	e.t.Helper()
	out := e.mustGit(e.work, "ls-remote", "origin", full)
	return strings.Contains(out, full)
}

// TestWebDeleteIsAllowedExactlyWhenAPushWouldBe deletes refs of every kind
// of rule as three different people, once from the web and once by pushing,
// and requires the two to agree with each other and with what the rules
// say, reason included.
func TestWebDeleteIsAllowedExactlyWhenAPushWouldBe(t *testing.T) {
	t.Parallel()
	e := setupGitHTTP(t)
	_, rootCred := e.person("root", true, auth.ScopeWrite)
	alice, aliceCred := e.person("alice", false, auth.ScopeWrite)
	_, bobCred := e.person("bob", false, auth.ScopeWrite)
	e.saveRule(reposvc.Rule{Kind: git.KindBranch, Pattern: "release/*", PushPolicy: reposvc.PushEveryone})
	e.saveRule(reposvc.Rule{Kind: git.KindBranch, Pattern: "ops/*", PushPolicy: reposvc.PushAdmins, AllowDelete: true})
	e.saveRule(reposvc.Rule{Kind: git.KindTag, Pattern: "v*", PushPolicy: reposvc.PushPeople, PushPeople: []string{alice.ID}, AllowDelete: true})
	e.saveRule(reposvc.Rule{Kind: git.KindTag, Pattern: "keep-*", PushPolicy: reposvc.PushEveryone})

	e.initWork(rootCred)
	e.commit("README.md", "# demo\n")
	e.mustGit(e.work, "push", "--quiet", "origin", "main")

	type ref struct {
		kind git.Kind
		// name is what a ref of this kind is called; %s is the person and
		// the way it is deleted, so every attempt has a ref of its own.
		name string
		// allowed is whether each person may delete it.
		allowed map[string]bool
		reason  map[string]string
	}
	cases := map[string]ref{
		"a ref no rule matches": {git.KindBranch, "feature-%s", map[string]bool{"alice": true, "bob": true, "root": true}, nil},
		"a branch whose rule does not allow deleting": {git.KindBranch, "release/%s", nil, map[string]string{
			"alice": `the rule for branch "release/*" does not allow deleting it`,
			"bob":   `the rule for branch "release/*" does not allow deleting it`,
			"root":  `the rule for branch "release/*" does not allow deleting it`,
		}},
		"a branch only admins push to": {git.KindBranch, "ops/%s", map[string]bool{"root": true}, map[string]string{
			"alice": `the rule for branch "ops/*" does not allow alice to push here`,
			"bob":   `the rule for branch "ops/*" does not allow bob to push here`,
		}},
		"a tag only named people push to": {git.KindTag, "v1-%s", map[string]bool{"alice": true, "root": true}, map[string]string{
			"bob": `the rule for tag "v*" does not allow bob to push here`,
		}},
		"a tag whose rule does not allow deleting": {git.KindTag, "keep-%s", nil, map[string]string{
			"alice": `the rule for tag "keep-*" does not allow deleting it`,
			"bob":   `the rule for tag "keep-*" does not allow deleting it`,
			"root":  `the rule for tag "keep-*" does not allow deleting it`,
		}},
	}
	people := map[string]gitCredential{"alice": aliceCred, "bob": bobCred, "root": rootCred}

	// Every ref each person will try to delete, made in one push.
	var create []string
	for _, c := range cases {
		for person := range people {
			for _, how := range []string{"web", "push"} {
				name := strings.Replace(c.name, "%s", person+"-"+how, 1)
				create = append(create, "HEAD:"+git.FullName(c.kind, name))
			}
		}
	}
	e.mustGit(e.work, append([]string{"push", "--quiet", "origin"}, create...)...)

	for label, c := range cases {
		for person, cred := range people {
			t.Run(label+", "+person, func(t *testing.T) {
				b := signInTo(t, e, person)
				webRef := git.FullName(c.kind, strings.Replace(c.name, "%s", person+"-web", 1))
				pushRef := git.FullName(c.kind, strings.Replace(c.name, "%s", person+"-push", 1))
				want := c.allowed[person]

				resp, body := b.do(http.MethodPost, "/demo/refs/delete", url.Values{"ref": {webRef}}, nil)
				out, ok := e.git(e.pushDir(cred), "push", "origin", "--delete", pushRef)
				if want {
					expect(t, resp, body, http.StatusSeeOther)
					if !ok {
						t.Fatalf("the web deleted %s but a push could not:\n%s", webRef, out)
					}
					if e.refExists(webRef) || e.refExists(pushRef) {
						t.Errorf("a ref is still there after being deleted")
					}
					return
				}
				reason := c.reason[person]
				expect(t, resp, body, http.StatusForbidden)
				if !strings.Contains(html.UnescapeString(body), reason) {
					t.Errorf("the web's refusal does not say %q:\n%s", reason, body)
				}
				expectRejected(t, out, ok, pushRef+": "+reason)
				if !e.refExists(webRef) || !e.refExists(pushRef) {
					t.Errorf("a refused delete removed a ref")
				}
			})
		}
	}

	// The default branch is never deleted, by anyone, from either.
	for person, cred := range people {
		b := signInTo(t, e, person)
		resp, body := b.do(http.MethodPost, "/demo/refs/delete", url.Values{"ref": {"refs/heads/main"}}, nil)
		expect(t, resp, body, http.StatusForbidden, "the default branch cannot be deleted")
		out, ok := e.git(e.pushDir(cred), "push", "origin", "--delete", "main")
		expectRejected(t, out, ok, "the default branch cannot be deleted")
	}
	if !e.refExists("refs/heads/main") {
		t.Fatal("the default branch is gone")
	}
}

// TestWebDeleteIsRecordedLikeAPush deletes one branch from the web and
// another by pushing, and compares everything Gitman keeps about them.
func TestWebDeleteIsRecordedLikeAPush(t *testing.T) {
	t.Parallel()
	e := setupGitHTTP(t)
	_, cred := e.person("alice", false, auth.ScopeWrite)
	e.saveRule(reposvc.Rule{Kind: git.KindBranch, Pattern: "feature-*", PushPolicy: reposvc.PushEveryone, AllowDelete: true, RunOnPush: true})
	e.initWork(cred)
	e.commit(".gitman.yml", "image: alpine:3.20\nsteps:\n  - name: test\n    run: echo hi\n")
	e.mustGit(e.work, "push", "--quiet", "origin", "main")
	head := e.commit("a.txt", "a\n")
	e.mustGit(e.work, "push", "--quiet", "origin", "HEAD:refs/heads/feature-web", "HEAD:refs/heads/feature-push")
	if n := e.count(`SELECT count(*) FROM runs WHERE status = 'queued'`); n != 2 {
		t.Fatalf("%d runs queued, want one for each branch", n)
	}

	b := signInTo(t, e, "alice")
	resp, body := b.do(http.MethodPost, "/demo/refs/delete", url.Values{"ref": {"refs/heads/feature-web"}}, nil)
	expect(t, resp, body, http.StatusSeeOther)
	if got := resp.Header.Get("Location"); got != "/demo" {
		t.Errorf("redirects to %q", got)
	}
	e.mustGit(e.work, "push", "--quiet", "origin", "--delete", "feature-push")

	// The same push and update rows, apart from which branch.
	const shape = `
		SELECT u.kind, u.is_create, u.is_delete, u.is_force, u.commit_count, u.commit_count_capped,
		       u.old_commit = $2, u.new_commit = repeat('0', 40), p.source_ip <> '', pe.username
		FROM push_updates u
		JOIN pushes p ON p.id = u.push_id
		JOIN people pe ON pe.id = p.person_id
		WHERE u.name = $1 AND u.is_delete`
	var rows [2]string
	for i, name := range []string{"feature-web", "feature-push"} {
		var kind, who string
		var create, del, force, capped, oldOK, newOK, hasIP bool
		var commits int
		if err := e.db.Pool.QueryRow(context.Background(), shape, name, head).Scan(&kind, &create, &del, &force, &commits, &capped, &oldOK, &newOK, &hasIP, &who); err != nil {
			t.Fatalf("%s: no delete was recorded: %v", name, err)
		}
		rows[i] = strings.Join([]string{kind, who}, " ")
		if create || !del || force || commits != 0 || capped || !oldOK || !newOK || !hasIP {
			t.Errorf("%s recorded as create=%v delete=%v force=%v commits=%d capped=%v old=%v new=%v ip=%v",
				name, create, del, force, commits, capped, oldOK, newOK, hasIP)
		}
	}
	if rows[0] != rows[1] {
		t.Errorf("recorded for %q from the web and %q from a push", rows[0], rows[1])
	}

	// Both are out of the ref index, and their queued runs are cancelled.
	if n := e.count(`SELECT count(*) FROM refs WHERE name LIKE 'feature-%'`); n != 0 {
		t.Errorf("%d deleted branches are still in the ref index", n)
	}
	if n := e.count(`SELECT count(*) FROM runs WHERE status = 'cancelled' AND reason = 'The branch ' || ref_name || ' was deleted.'`); n != 2 {
		t.Errorf("%d queued runs were cancelled for their deleted branch, want 2", n)
	}
	if n := e.count(`SELECT count(*) FROM runs WHERE status = 'queued'`); n != 0 {
		t.Errorf("%d runs of deleted branches are still queued", n)
	}

	// It shows in Activity as a push's delete does.
	resp, body = b.do(http.MethodGet, "/demo/activity", nil, nil)
	expect(t, resp, body, http.StatusOK)
	text := stripTags(body)
	if strings.Count(text, "alice deleted") != 2 {
		t.Errorf("Activity lists %d deletes by alice, want 2:\n%s", strings.Count(text, "alice deleted"), text)
	}
}

func TestWebDeleteOffersTheButtonOnlyWhereItWouldBeAllowed(t *testing.T) {
	t.Parallel()
	e := setupGitHTTP(t)
	_, rootCred := e.person("root", true, auth.ScopeWrite)
	e.person("alice", false, auth.ScopeWrite)
	e.saveRule(reposvc.Rule{Kind: git.KindBranch, Pattern: "release/*", PushPolicy: reposvc.PushEveryone})
	e.saveRule(reposvc.Rule{Kind: git.KindBranch, Pattern: "ops/*", PushPolicy: reposvc.PushAdmins, AllowDelete: true})
	e.initWork(rootCred)
	e.commit("README.md", "# demo\n")
	e.mustGit(e.work, "push", "--quiet", "origin", "main")
	e.mustGit(e.work, "branch", "merged")
	e.mustGit(e.work, "branch", "release/1")
	e.mustGit(e.work, "branch", "ops/deploy")
	e.mustGit(e.work, "tag", "v1")
	e.mustGit(e.work, "checkout", "--quiet", "-b", "ahead")
	e.commit("b.txt", "b\n")
	e.mustGit(e.work, "push", "--quiet", "origin", "merged", "release/1", "ops/deploy", "ahead", "v1")

	// The Delete button of a ref, told from its Run button, which posts the
	// same ref somewhere else.
	button := func(body, ref string) bool {
		kind, name, _ := git.SplitFullName(ref)
		return strings.Contains(body, `aria-label="Delete `+string(kind)+` `+name+`"`)
	}

	alice := signInTo(t, e, "alice")
	resp, body := alice.do(http.MethodGet, "/demo", nil, nil)
	expect(t, resp, body, http.StatusOK)
	for ref, want := range map[string]bool{
		"refs/heads/merged":     true,
		"refs/heads/ahead":      true,
		"refs/tags/v1":          true,
		"refs/heads/main":       false, // the default branch
		"refs/heads/release/1":  false, // its rule does not allow deleting
		"refs/heads/ops/deploy": false, // alice may not push to it
	} {
		if got := button(body, ref); got != want {
			t.Errorf("alice: Delete button for %s = %v, want %v", ref, got, want)
		}
	}
	// What the confirmation says about a branch, and about a tag.
	for _, want := range []string{
		`data-confirm="Delete branch merged?"`, `It is merged into main, so nothing is lost.`,
		`data-confirm="Delete branch ahead?"`, `It is not merged into main.`,
		`data-confirm="Delete tag v1?"`, `The commit stays, and the tag can be pushed again.`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("alice's Overview lacks %q", want)
		}
	}

	root := signInTo(t, e, "root")
	resp, body = root.do(http.MethodGet, "/demo", nil, nil)
	expect(t, resp, body, http.StatusOK)
	if !button(body, "refs/heads/ops/deploy") || button(body, "refs/heads/release/1") || button(body, "refs/heads/main") {
		t.Errorf("root's buttons are wrong: want ops/deploy only of those three")
	}
}

func TestWebDeleteRefusesWhatIsNotARef(t *testing.T) {
	database, store, b := setupWithStore(t)
	signIn(t, database, b, "darius", false)
	seedFilesRepo(t, database, store, b)

	resp, body := b.do(http.MethodPost, "/waiotech/refs/delete", url.Values{"ref": {"refs/heads/nope"}}, nil)
	expect(t, resp, body, http.StatusNotFound, "has no branch named")
	resp, body = b.do(http.MethodPost, "/waiotech/refs/delete", url.Values{"ref": {"refs/tags/nope"}}, nil)
	expect(t, resp, body, http.StatusNotFound, "has no tag named")
	for _, ref := range []string{"", "main", "heads/release", "refs/notes/commits", "HEAD", "refs/heads/"} {
		resp, body = b.do(http.MethodPost, "/waiotech/refs/delete", url.Values{"ref": {ref}}, nil)
		expect(t, resp, body, http.StatusUnprocessableEntity, "Choose a branch or tag to delete.")
	}
	// Nothing was deleted by any of them.
	if resp, body = b.do(http.MethodGet, "/waiotech@release", nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("release is gone: %d\n%s", resp.StatusCode, body)
	}
	// A request from another site is not carried out.
	resp, body = b.do(http.MethodPost, "/waiotech/refs/delete", url.Values{"ref": {"refs/heads/release"}},
		map[string]string{"Sec-Fetch-Site": "cross-site"})
	expect(t, resp, body, http.StatusForbidden)
	if resp, body = b.do(http.MethodGet, "/waiotech@release", nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("release was deleted by a cross-site request: %d\n%s", resp.StatusCode, body)
	}
}
