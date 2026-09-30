package web

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	reposvc "github.com/mmrzaf/gitman/internal/repo"
)

// notFoundPhrase is the exact wording repoNamed uses for a repository
// that either does not exist or cannot be read — the two must be
// indistinguishable, which is what every check in this file tests for.
const notFoundPhrase = "There is no repository named"

// matrixFixture is two repositories — one visible to everyone, one
// restricted with exactly one explicit reader — each with a real commit
// and a real run, and three signed-in browsers: an admin, the
// restricted repository's reader, and a member with no access to it.
type matrixFixture struct {
	admin, reader, outsider *browser
	everyone, restricted    *reposvc.Repo
	// commit and runNumber are the same shape in both repositories, so
	// one path template works for either.
	commit    string
	runNumber int64
}

func newMatrixFixture(t *testing.T) *matrixFixture {
	t.Helper()
	database, store, adminB := setupWithStore(t)
	signIn(t, database, adminB, "lead", true)

	readerB := newBrowser(t, adminB.server)
	signIn(t, database, readerB, "reader", false)
	outsiderB := newBrowser(t, adminB.server)
	signIn(t, database, outsiderB, "outsider", false)

	repos := reposvc.NewService(database, store, "")
	seed := func(name string) (*reposvc.Repo, string) {
		resp, body := adminB.do(http.MethodPost, "/repos", url.Values{"name": {name}}, nil)
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("create %s: %d\n%s", name, resp.StatusCode, body)
		}
		r, err := repos.GetByName(context.Background(), name)
		if err != nil {
			t.Fatal(err)
		}
		barePath, err := store.Path(r.ID)
		if err != nil {
			t.Fatal(err)
		}
		work := t.TempDir()
		runGit(t, work, "clone", "--quiet", barePath, ".")
		writeFile(t, work, "README.md", []byte("# "+name+"\n"))
		runGit(t, work, "add", "-A")
		runGit(t, work, "commit", "--quiet", "-m", "Initial commit")
		commit := runGit(t, work, "rev-parse", "HEAD")
		runGit(t, work, "push", "--quiet", "origin", "main")
		syncRepoRefs(t, database, store, r.ID)

		if _, err := database.Pool.Exec(context.Background(), `
			INSERT INTO runs (id, repo_id, number, commit_hash, trigger, status, ref_kind, ref_name)
			VALUES ($1, $2, 1, $3, 'manual', 'queued', 'branch', 'main')
		`, name+"-run1", r.ID, commit); err != nil {
			t.Fatal(err)
		}
		return r, commit
	}

	everyone, commit := seed("everyone-repo")
	restricted, restrictedCommit := seed("restricted-repo")
	if commit[:10] == "" || restrictedCommit[:10] == "" {
		t.Fatal("expected non-empty commits")
	}
	if err := repos.SetVisibility(context.Background(), restricted.ID, reposvc.VisibilityRestricted, ""); err != nil {
		t.Fatal(err)
	}
	readerPerson := mustPerson(t, database, "reader")
	if err := repos.AddReader(context.Background(), restricted.ID, readerPerson.ID, readerPerson.Username, ""); err != nil {
		t.Fatal(err)
	}

	return &matrixFixture{
		admin: adminB, reader: readerB, outsider: outsiderB,
		everyone: everyone, restricted: restricted, commit: commit, runNumber: 1,
	}
}

// route is one repository-scoped endpoint: method and a path template
// with "%s" for the repository name. Every one of these must resolve
// the repository through repoByName/repoNamed (checked separately, by
// TestEveryRepoRouteReachesTheAccessCheck) — this table proves the
// check actually behaves correctly at runtime for a representative
// route from each handler family.
func (f *matrixFixture) routes() []struct {
	name   string
	method string
	path   func(repoName string) string
} {
	return []struct {
		name   string
		method string
		path   func(repoName string) string
	}{
		{"repository page", http.MethodGet, func(r string) string { return "/" + r }},
		{"files", http.MethodGet, func(r string) string { return "/" + r + "@main/README.md" }},
		{"history", http.MethodGet, func(r string) string { return "/" + r + "/history" }},
		{"history at a path", http.MethodGet, func(r string) string { return "/" + r + "/history?ref=main&path=README.md" }},
		{"history activity", http.MethodGet, func(r string) string { return "/" + r + "/history?tab=activity" }},
		{"old history tab address", http.MethodGet, func(r string) string { return "/" + r + "@main/README.md?tab=history" }},
		{"commit view", http.MethodGet, func(r string) string { return "/" + r + "/commit/" + f.commit[:10] }},
		{"compare", http.MethodGet, func(r string) string {
			return "/" + r + "/compare/" + f.commit[:10] + "..." + f.commit[:10]
		}},
		{"archive", http.MethodGet, func(r string) string { return "/" + r + "/archive/main.tar.gz" }},
		{"archive of a commit", http.MethodGet, func(r string) string { return "/" + r + "/archive/" + f.commit + ".zip" }},
		{"compare pickers", http.MethodGet, func(r string) string { return "/" + r + "/compare" }},
		{"compare pickers with refs", http.MethodGet, func(r string) string { return "/" + r + "/compare?base=main&head=main" }},
		{"tree-paths", http.MethodGet, func(r string) string { return "/" + r + "/tree-paths?ref=main" }},
		{"run view", http.MethodGet, func(r string) string { return "/" + r + "/runs/" + strconv.FormatInt(f.runNumber, 10) }},
		{"run log", http.MethodGet, func(r string) string {
			return "/" + r + "/runs/" + strconv.FormatInt(f.runNumber, 10) + "/log?step=0"
		}},
		{"runs list", http.MethodGet, func(r string) string { return "/" + r + "/runs" }},
		{"settings", http.MethodGet, func(r string) string { return "/" + r + "/settings" }},
	}
}

// TestAccessMatrix is every route in (*matrixFixture).routes(), for a
// reader, a non-reader and an admin, against both an everyone-visible
// and a restricted repository. A non-reader must see exactly the
// not-found page a nonexistent repository would give — never anything
// else, and never a 403 — for every one of them; a reader or an admin
// must never see it.
func TestAccessMatrix(t *testing.T) {
	f := newMatrixFixture(t)
	for _, route := range f.routes() {
		for _, repo := range []*reposvc.Repo{f.everyone, f.restricted} {
			for _, who := range []struct {
				name string
				b    *browser
				// blocked is whether this person must NOT be able to
				// read repo.
				blocked bool
			}{
				{"admin", f.admin, false},
				{"reader", f.reader, false},
				{"outsider", f.outsider, repo == f.restricted},
			} {
				t.Run(route.name+"/"+repo.Name+"/"+who.name, func(t *testing.T) {
					resp, body := who.b.do(route.method, route.path(repo.Name), nil, nil)
					gotBlocked := strings.Contains(body, notFoundPhrase)
					if who.blocked && (!gotBlocked || resp.StatusCode != http.StatusNotFound) {
						t.Fatalf("%s on %s: status %d, blocked=%v; want blocked with 404", who.name, repo.Name, resp.StatusCode, gotBlocked)
					}
					if !who.blocked && gotBlocked {
						t.Fatalf("%s on %s: got the not-found page; want real access\n%s", who.name, repo.Name, body)
					}
				})
			}
		}
	}
}

// TestAccessMatrixMutatingRoutes is the same property as TestAccessMatrix
// for every route that changes something, without asserting the change
// itself succeeded — that a person may push, run or configure a
// repository is covered by internal/repo and internal/push's own tests;
// this only checks the read gate runs before any of that logic does.
func TestAccessMatrixMutatingRoutes(t *testing.T) {
	f := newMatrixFixture(t)
	routes := []struct {
		name   string
		path   func(repoName string) string
		values url.Values
	}{
		{"run cancel", func(r string) string {
			return "/" + r + "/runs/" + strconv.FormatInt(f.runNumber, 10) + "/cancel"
		}, url.Values{}},
		{"run again", func(r string) string {
			return "/" + r + "/runs/" + strconv.FormatInt(f.runNumber, 10) + "/again"
		}, url.Values{}},
		{"delete a ref", func(r string) string { return "/" + r + "/refs/delete" }, url.Values{"ref": {"refs/heads/main"}}},
		{"run a ref", func(r string) string { return "/" + r + "/runs" }, url.Values{"ref": {"refs/heads/main"}}},
		{"settings description", func(r string) string { return "/" + r + "/settings/description" }, url.Values{"description": {"x"}}},
		{"settings default branch", func(r string) string { return "/" + r + "/settings/default-branch" }, url.Values{"default_branch": {"main"}}},
	}
	for _, route := range routes {
		for _, repo := range []*reposvc.Repo{f.everyone, f.restricted} {
			for _, who := range []struct {
				name    string
				b       *browser
				blocked bool
			}{
				{"admin", f.admin, false},
				{"outsider", f.outsider, repo == f.restricted},
			} {
				t.Run(route.name+"/"+repo.Name+"/"+who.name, func(t *testing.T) {
					resp, body := who.b.do(http.MethodPost, route.path(repo.Name), route.values, nil)
					gotBlocked := strings.Contains(body, notFoundPhrase)
					if who.blocked && (!gotBlocked || resp.StatusCode != http.StatusNotFound) {
						t.Fatalf("%s on %s: status %d, blocked=%v; want blocked with 404", who.name, repo.Name, resp.StatusCode, gotBlocked)
					}
					if !who.blocked && gotBlocked {
						t.Fatalf("%s on %s: got the not-found page; want the action attempted\n%s", who.name, repo.Name, body)
					}
				})
			}
		}
	}
}

// TestAccessMatrixHomeJumpAndActivity is the list-shaped surfaces: a
// restricted repository a person cannot read must never be named on
// Home, in the jump-to list, or in the activity feed.
func TestAccessMatrixHomeJumpAndActivity(t *testing.T) {
	f := newMatrixFixture(t)

	for _, who := range []struct {
		name    string
		b       *browser
		visible bool
	}{
		{"admin", f.admin, true},
		{"reader", f.reader, true},
		{"outsider", f.outsider, false},
	} {
		_, home := who.b.do(http.MethodGet, "/", nil, nil)
		if strings.Contains(home, f.restricted.Name) != who.visible {
			t.Errorf("Home for %s: restricted repo named = %v, want %v", who.name, strings.Contains(home, f.restricted.Name), who.visible)
		}
		if !strings.Contains(home, f.everyone.Name) {
			t.Errorf("Home for %s: the everyone-visible repo is missing", who.name)
		}

		_, jump := who.b.do(http.MethodGet, "/jump", nil, nil)
		if strings.Contains(jump, f.restricted.Name) != who.visible {
			t.Errorf("Jump for %s: restricted repo named = %v, want %v", who.name, strings.Contains(jump, f.restricted.Name), who.visible)
		}
	}
}

// TestEveryRepoRouteReachesTheAccessCheck parses routes.go itself and
// fails if a route taking a {repo} path value is registered against a
// handler that does not resolve it through repoByName or repoNamed
// (directly, or by calling runByNumber, which does) — so a new
// repository-scoped route with no read check fails this test even if
// nobody remembers to add it to the tables above.
func TestEveryRepoRouteReachesTheAccessCheck(t *testing.T) {
	fset := token.NewFileSet()
	routesFile, err := parser.ParseFile(fset, "routes.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	type route struct {
		pattern string
		handler string
	}
	var routesFound []route
	ast.Inspect(routesFile, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "HandleFunc" && sel.Sel.Name != "Handle") || len(call.Args) != 2 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		pattern, err := strconv.Unquote(lit.Value)
		if err != nil || !strings.Contains(pattern, "{repo}") {
			return true
		}
		routesFound = append(routesFound, route{pattern: pattern, handler: handlerName(call.Args[1])})
		return true
	})
	if len(routesFound) == 0 {
		t.Fatal("found no {repo} routes in routes.go — the parser is looking in the wrong place")
	}

	// gateReaching is every function already known to call
	// repoByName/repoNamed, directly or (for runByNumber) one call away.
	// A handler not in this set, and not itself calling repoByName or
	// repoNamed, fails the test.
	gateReaching := findGateReachingFuncs(t, fset)

	for _, r := range routesFound {
		if r.handler == "" {
			t.Errorf("route %q: could not identify its handler function name", r.pattern)
			continue
		}
		if !gateReaching[r.handler] {
			t.Errorf("route %q's handler %q does not appear to call repoByName or repoNamed (directly, or via runByNumber) — a restricted repository would leak through it", r.pattern, r.handler)
		}
	}
}

// handlerName extracts the method name a route's handler expression
// resolves to: "a.repository" or "a.gitRPC(x)" style expressions both
// reduce to their outermost selector's name; a.page(level, a.method)
// reduces to "method", the actual handler.
func handlerName(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.SelectorExpr:
		return v.Sel.Name
	case *ast.CallExpr:
		if sel, ok := v.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "page" && len(v.Args) == 2 {
			return handlerName(v.Args[1])
		}
	}
	return ""
}

// findGateReachingFuncs parses every .go file in this package and
// returns the set of function names whose body calls repoByName or
// repoNamed, plus (one hop further) any function that calls one of
// those. Two hops is enough for this package's actual call shapes
// (runByNumber calls repoByName; runView/runLog/runCancel/runAgain call
// runByNumber) and keeps this a simple, readable check rather than a
// general call-graph solver.
func findGateReachingFuncs(t *testing.T, fset *token.FileSet) map[string]bool {
	t.Helper()
	files, err := parseDirGo(fset, ".")
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string]map[string]bool{} // function name -> names it calls
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil {
				continue
			}
			called := map[string]bool{}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
						called[sel.Sel.Name] = true
					}
				}
				return true
			})
			calls[fn.Name.Name] = called
		}
	}
	direct := map[string]bool{"repoByName": true, "repoNamed": true}
	reaching := map[string]bool{}
	for name, called := range calls {
		for target := range direct {
			if called[target] {
				reaching[name] = true
			}
		}
	}
	for name, called := range calls {
		for target := range reaching {
			if called[target] {
				reaching[name] = true
			}
		}
	}
	reaching["repoByName"] = true
	reaching["repoNamed"] = true
	return reaching
}

// parseDirGo parses every non-test .go file directly in dir — this
// package's own source, never its dependencies — for the static call
// check above.
func parseDirGo(fset *token.FileSet, dir string) ([]*ast.File, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, nil
}
