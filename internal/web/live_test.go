package web

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mmrzaf/gitman/internal/activity"
	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/auth"
	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/postgres/pgtest"
	reposvc "github.com/mmrzaf/gitman/internal/repo"
)

// renderingApp is an App with templates and assets but no services, for
// rendering pages from constructed data.
func renderingApp(t *testing.T) *App {
	t.Helper()
	assets, err := loadAssets()
	if err != nil {
		t.Fatal(err)
	}
	views, err := loadViews(assets)
	if err != nil {
		t.Fatalf("templates do not parse: %v", err)
	}
	return &App{assets: assets, views: views, hub: newHub(), now: time.Now,
		log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func sampleRun(status ci.Status) *ci.RunDetail {
	started := time.Now().Add(-90 * time.Second)
	finished := time.Now().Add(-10 * time.Second)
	code, fail := 0, 2
	run := &ci.RunDetail{
		Summary: ci.Summary{ID: "run-1", RepoName: "waiotech", Number: 42, RefKind: git.KindBranch, RefName: "develop",
			Trigger: ci.TriggerPush, Status: status, Actor: "darius", QueuedAt: started, StartedAt: &started},
		Commit: strings.Repeat("a", 40), Target: "staging", Version: "3f2a91c",
		Steps: []ci.StepDetail{
			{ID: "s0", Index: 0, Name: "test", Status: ci.StepPassed, ExitCode: &code, StartedAt: &started, FinishedAt: &finished},
			{ID: "s1", Index: 1, Name: "deploy", Status: ci.StepFailed, ExitCode: &fail, StartedAt: &started, FinishedAt: &finished},
			{ID: "s2", Index: 2, Name: "notify", Status: ci.StepSkipped},
		},
		Results: []ci.SummaryEntry{{Key: "image", Value: "waiotech:3f2a91c"}},
	}
	if status == ci.StatusFailed {
		run.Reason = `Step "deploy" exited with code 2.`
		run.FinishedAt = &finished
	}
	return run
}

func TestRunPageRenders(t *testing.T) {
	a := renderingApp(t)
	for _, status := range []ci.Status{ci.StatusFailed, ci.StatusRunning} {
		t.Run(string(status), func(t *testing.T) {
			run := sampleRun(status)
			req := httptest.NewRequest(http.MethodGet, "/waiotech/runs/42", nil)
			page := runPage{repoFrame: repoFrame{Repo: &reposvc.Repo{Name: "waiotech"}}, Run: run, Selected: selectedStep(req, run),
				Log: splitLog("building\n<script>alert(1)</script>\n", 1), LogAfter: 3, Now: time.Now(), Duration: 80 * time.Second}
			rec := httptest.NewRecorder()
			a.render(rec, signedIn(req), http.StatusOK, "run", "#42 · waiotech", page)
			body := rec.Body.String()
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d:\n%s", rec.Code, body)
			}
			for _, want := range []string{"Run #42", "develop", "staging", "deploy", "exit code 2", "waiotech:3f2a91c",
				`data-log-step="s1"`, `data-log-after="3"`, "&lt;script&gt;", "1m 20s", `id="L2"`, `href="?step=0"`,
				`aria-current="step"`, `data-error-pattern="`} {
				if !strings.Contains(body, want) {
					t.Errorf("page lacks %q", want)
				}
			}
			live := strings.Contains(body, `data-live-events="/events?run=run-1"`)
			if live != (status == ci.StatusRunning) {
				t.Errorf("live stream present = %v for a %s run", live, status)
			}
			if status == ci.StatusFailed && !strings.Contains(body, "Run again") {
				t.Error("a finished run offers no way to run it again")
			}
			if status == ci.StatusRunning && !strings.Contains(body, ">Cancel<") {
				t.Error("a running run offers no way to cancel it")
			}
		})
	}
}

func TestSelectedStep(t *testing.T) {
	run := sampleRun(ci.StatusFailed)
	pick := func(query string) string {
		s := selectedStep(httptest.NewRequest(http.MethodGet, "/x"+query, nil), run)
		if s == nil {
			return ""
		}
		return s.Name
	}
	if got := pick(""); got != "deploy" {
		t.Errorf("default step = %q, want the failed one", got)
	}
	if got := pick("?step=0"); got != "test" {
		t.Errorf("?step=0 = %q", got)
	}
	if got := pick("?step=9"); got != "deploy" {
		t.Errorf("an out-of-range ?step = %q, want the default", got)
	}
	run.Steps[1].Status = ci.StepRunning
	if got := pick(""); got != "deploy" {
		t.Errorf("default step while running = %q", got)
	}
}

func TestFormatDurationAndLogCleaning(t *testing.T) {
	for d, want := range map[time.Duration]string{45 * time.Second: "45s", 185 * time.Second: "3m 05s", 3720 * time.Second: "1h 02m"} {
		if got := formatDuration(d); got != want {
			t.Errorf("formatDuration(%v) = %q, want %q", d, got, want)
		}
	}
	if got := ansiEscape.ReplaceAllString("\x1b[32mok\x1b[0m \x1b]0;title\x07done", ""); got != "ok done" {
		t.Errorf("terminal escapes left in log: %q", got)
	}
	if got := safeFileName("build & test/2"); got != "build-test-2" {
		t.Errorf("safeFileName = %q", got)
	}
}

func TestHubNeverBlocks(t *testing.T) {
	h := newHub()
	slow, unsubscribe := h.subscribe()
	defer unsubscribe()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			h.publish(notice{channel: ci.NotifyChannel, payload: "r"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publishing blocked on a subscriber that does not read")
	}
	if len(slow.notices) == 0 {
		t.Fatal("the subscriber received nothing")
	}
	if !slow.missed.Load() {
		t.Fatal("the notices the subscriber missed were not remembered, so it would never catch up")
	}
}

// TestEventStreamRefusesAMalformedRun keeps a stream from echoing what a
// caller put in ?run= into its own events, where a line break would
// start an event of the caller's making.
func TestEventStreamRefusesAMalformedRun(t *testing.T) {
	a := renderingApp(t)
	req := httptest.NewRequest(http.MethodGet, "/events?run="+url.QueryEscape("run-1\nevent: change\ndata: forged"), nil)
	if err := a.events(httptest.NewRecorder(), req); apperr.KindOf(err) != apperr.KindInvalid {
		t.Fatalf("events = %v; want the request refused as invalid", err)
	}
}

// eventStreamFixture is a minimal, DB-backed App for testing /events'
// notice filtering: real enough that mustReadRun/mustReadStep's
// readability checks work, without the full session/routing stack
// setup and browser build. The returned handler already carries person
// in its request context, the way page's session middleware would.
func eventStreamFixture(t *testing.T) (a *App, handler http.HandlerFunc) {
	t.Helper()
	database := pgtest.Open(t)
	store := git.NewStore(t.TempDir())
	t.Cleanup(store.Close)
	repos := reposvc.NewService(database, store, "")
	people := auth.NewService(database)
	a = &App{repos: repos, ci: ci.NewService(database), hub: newHub(), now: time.Now,
		log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	r, err := repos.Create(context.Background(), "demo", "", "main", "")
	if err != nil {
		t.Fatal(err)
	}
	person, err := people.Create(context.Background(), "darius", "correct-horse-battery", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(context.Background(), `
		INSERT INTO runs (id, repo_id, number, commit_hash, trigger, status)
		VALUES ('run-1', $1, 1, 'abc123', 'manual', 'queued')
	`, r.ID); err != nil {
		t.Fatal(err)
	}
	handler = func(w http.ResponseWriter, req *http.Request) {
		req = req.WithContext(context.WithValue(req.Context(), personKey{}, person))
		if err := a.events(w, req); err != nil {
			t.Error(err)
		}
	}
	return a, handler
}

func TestEventStreamSendsChanges(t *testing.T) {
	a, handler := eventStreamFixture(t)
	server := httptest.NewServer(handler)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/events?run=run-1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}

	lines := bufio.NewScanner(resp.Body)
	next := func() string {
		for lines.Scan() {
			if line := lines.Text(); strings.HasPrefix(line, "event:") || strings.HasPrefix(line, "data:") {
				return line
			}
		}
		return ""
	}
	// Another run's change, and the activity feed's, are filtered out;
	// this run's arrives.
	go func() {
		time.Sleep(100 * time.Millisecond)
		a.hub.publish(notice{channel: activity.NotifyChannel, payload: "repo-1"})
		a.hub.publish(notice{channel: ci.NotifyChannel, payload: "run-2"})
		a.hub.publish(notice{channel: ci.NotifyChannel, payload: "run-1"})
	}()
	if got := next(); got != "event: change" {
		t.Fatalf("first event line = %q", got)
	}
	if got := next(); got != "data: run-1" {
		t.Fatalf("change data = %q, want run-1 only", got)
	}
}

// TestEventStreamFollowsActivity is a page with no run of its own — Home,
// or a Repository page — which must update for a push or a settings
// change, not only for runs.
func TestEventStreamFollowsActivity(t *testing.T) {
	a, handler := eventStreamFixture(t)
	server := httptest.NewServer(handler)
	defer server.Close()
	repo, err := a.repos.GetByName(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	go func() {
		time.Sleep(100 * time.Millisecond)
		a.hub.publish(notice{channel: activity.NotifyChannel, payload: repo.ID})
	}()
	lines := bufio.NewScanner(resp.Body)
	for lines.Scan() {
		if lines.Text() == "event: change" {
			return
		}
	}
	t.Fatal("no change event for a push or settings change")
}

func TestLivePagesRender(t *testing.T) {
	a := renderingApp(t)
	now := time.Now()
	run := ci.Summary{ID: "run-1", RepoName: "waiotech", Number: 42, RefKind: git.KindBranch, RefName: "develop",
		Status: ci.StatusRunning, Actor: "darius", QueuedAt: now}
	feed := []activity.Entry{{Kind: activity.KindRun, RepoName: "waiotech", At: now, RunNumber: 41, Status: "passed"}}
	repo := &reposvc.Repo{Name: "waiotech", DefaultBranch: "main"}

	pages := []struct {
		name string
		data any
		want []string
	}{
		{"home", homePage{InProgress: []ci.Summary{run}, Timeline: feed, CreateForm: newForm(nil)},
			[]string{`data-live-events="/events"`, `href="/waiotech/runs/42"`, `href="/waiotech/runs/41"`, `data-live-region="progress"`}},
		{"repository", repositoryPage{repoFrame: repoFrame{Repo: repo, Section: "overview"}, CloneURL: "waiotech.git", Timeline: feed, Tab: "branches",
			Branches: []refRow{{IndexedRef: reposvc.IndexedRef{Kind: git.KindBranch, Name: "develop", Commit: strings.Repeat("b", 40), UpdatedAt: now}, LatestRun: &run}}},
			[]string{`data-live-events="/events"`, `href="/waiotech/runs/42"`, `data-live-region="branches"`, `data-live-region="tags"`,
				`href="/waiotech/compare/main...develop"`, `aria-current="page">`}},
		{"commit", commitPage{repoFrame: repoFrame{Repo: repo}, Commit: &git.Commit{Hash: strings.Repeat("c", 40), Subject: "Fix it"}, Diff: &git.Diff{}},
			[]string{`action="/waiotech/commit/` + strings.Repeat("c", 40) + `/run"`, "Run this commit"}},
	}
	for _, p := range pages {
		t.Run(p.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			a.render(rec, signedIn(httptest.NewRequest(http.MethodGet, "/", nil)), http.StatusOK, p.name, p.name, p.data)
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d:\n%s", rec.Code, rec.Body.String())
			}
			for _, want := range p.want {
				if !strings.Contains(rec.Body.String(), want) {
					t.Errorf("page lacks %q", want)
				}
			}
		})
	}
}

// signedIn attaches a member to r, as the page wrapper does for every
// page but the login page.
func signedIn(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), personKey{}, &auth.Person{ID: "p1", Username: "darius"}))
}

func TestRoutesRegisterAndResolve(t *testing.T) {
	a := renderingApp(t)
	mux := http.NewServeMux()
	a.register(mux) // Go's router panics here on conflicting patterns.
	for path, want := range map[string]string{
		"GET /events":                       "GET /events",
		"GET /waiotech/runs/42":             "GET /{repo}/runs/{n}",
		"GET /waiotech/runs/42/log":         "GET /{repo}/runs/{n}/log",
		"POST /waiotech/runs/42/cancel":     "POST /{repo}/runs/{n}/cancel",
		"POST /waiotech/runs/42/again":      "POST /{repo}/runs/{n}/again",
		"POST /waiotech/commit/abc1234/run": "POST /{repo}/commit/{sha}/run",
		"GET /waiotech":                     "GET /{repo}",
	} {
		method, target, _ := strings.Cut(path, " ")
		_, pattern := mux.Handler(httptest.NewRequest(method, target, nil))
		if pattern != want {
			t.Errorf("%s resolves to %q, want %q", path, pattern, want)
		}
	}
}

func TestFilesAtRefIsDecidedBeforeRouting(t *testing.T) {
	for target, want := range map[string]bool{
		"/waiotech@main":                 true,
		"/waiotech@main/runs/42":         true,
		"/waiotech@release/1.2/settings": true,
		"/waiotech@3f2a91c/info/refs":    true,
		"/waiotech/runs/42":              false,
		"/waiotech":                      false,
		"/events":                        false,
	} {
		if got := filesAtRef(httptest.NewRequest(http.MethodGet, target, nil)); got != want {
			t.Errorf("filesAtRef(GET %s) = %v, want %v", target, got, want)
		}
	}
	if filesAtRef(httptest.NewRequest(http.MethodPost, "/waiotech@main/runs/42/cancel", nil)) {
		t.Error("a POST is never a file view")
	}
}
