package web

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
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
	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/postgres"
)

func TestSafeNext(t *testing.T) {
	cases := map[string]string{
		"":                      "/",
		"/":                     "/",
		"/waiotech/runs?x=1":    "/waiotech/runs?x=1",
		"//evil.example":        "/",
		"/\\evil.example":       "/",
		"https://evil.example/": "/",
		"javascript:alert(1)":   "/",
		"/login?next=/login":    "/",
		"relative/path":         "/",
	}
	for in, want := range cases {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSameOrigin(t *testing.T) {
	a := &App{origin: "https://git.example.com"}
	cases := []struct {
		name    string
		method  string
		headers map[string]string
		want    bool
	}{
		{"safe method", http.MethodGet, map[string]string{"Sec-Fetch-Site": "cross-site"}, true},
		{"same-origin fetch", http.MethodPost, map[string]string{"Sec-Fetch-Site": "same-origin"}, true},
		{"user-typed", http.MethodPost, map[string]string{"Sec-Fetch-Site": "none"}, true},
		{"cross-site fetch", http.MethodPost, map[string]string{"Sec-Fetch-Site": "cross-site"}, false},
		{"same-site but other host", http.MethodPost, map[string]string{"Sec-Fetch-Site": "same-site"}, false},
		{"matching origin", http.MethodPost, map[string]string{"Origin": "https://git.example.com"}, true},
		{"other origin", http.MethodPost, map[string]string{"Origin": "https://evil.example"}, false},
		{"scheme differs", http.MethodPost, map[string]string{"Origin": "http://git.example.com"}, false},
		{"no browser headers", http.MethodPost, nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(c.method, "/logout", nil)
			for k, v := range c.headers {
				r.Header.Set(k, v)
			}
			if got := a.sameOrigin(r); got != c.want {
				t.Errorf("sameOrigin = %v, want %v", got, c.want)
			}
		})
	}
}

func TestLoginLimiter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := newLoginLimiter(func() time.Time { return now })
	for i := 0; i < loginUsernameLimit; i++ {
		if ok, _ := l.allow("darius", "1.1.1.1"); !ok {
			t.Fatalf("attempt %d refused too early", i+1)
		}
		l.failure("Darius ", "1.1.1.1")
	}
	ok, wait := l.allow("darius", "1.1.1.1")
	if ok || wait != loginWindow {
		t.Fatalf("after %d failures: allow=%v wait=%v", loginUsernameLimit, ok, wait)
	}
	if ok, _ := l.allow("darius", "2.2.2.2"); !ok {
		t.Error("another address should not be limited by these failures")
	}
	if ok, _ := l.allow("sara", "1.1.1.1"); !ok {
		t.Error("another username from the same address is below the address limit")
	}
	now = now.Add(loginWindow)
	if ok, _ := l.allow("darius", "1.1.1.1"); !ok {
		t.Error("expected the window to reset")
	}
}

func TestLoginLimiterAddressLimitAndBound(t *testing.T) {
	now := time.Now()
	l := newLoginLimiter(func() time.Time { return now })
	for i := 0; i < loginAddressLimit; i++ {
		l.failure("user"+strings.Repeat("x", i), "9.9.9.9")
	}
	if ok, _ := l.allow("fresh", "9.9.9.9"); ok {
		t.Error("expected the address limit to apply across usernames")
	}
	for i := 0; i < loginLimiterKeys; i++ {
		l.failure("u"+strings.Repeat("y", i%50)+string(rune('a'+i%26)), "10.0.0.1")
	}
	if len(l.entries) > loginLimiterKeys {
		t.Errorf("limiter holds %d entries, above its bound", len(l.entries))
	}
}

func TestAgo(t *testing.T) {
	now := time.Now()
	cases := map[time.Duration]string{
		10 * time.Second: "just now",
		5 * time.Minute:  "5 min ago",
		3 * time.Hour:    "3 h ago",
		49 * time.Hour:   "2 d ago",
	}
	for d, want := range cases {
		if got := ago(now.Add(-d)); got != want {
			t.Errorf("ago(-%v) = %q, want %q", d, got, want)
		}
	}
	old := time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC)
	if got := ago(old); got != "2025-03-04" {
		t.Errorf("ago(old) = %q", got)
	}
	// A time still to come, such as a token's expiry, is not "just now".
	if got := ago(now.Add(3*24*time.Hour + time.Hour)); got != "in 3 d" {
		t.Errorf("ago(in 3 days) = %q, want \"in 3 d\"", got)
	}
}

func TestNewFieldNeverEchoesPasswords(t *testing.T) {
	f := newForm(url.Values{"username": {"darius"}, "password": {"secret"}})
	f.Fail("password", "Wrong.")
	if got := newField(f, "username", "Username", "text", "required", "autocomplete:username"); got.Value != "darius" || !got.Required || got.Autocomplete != "username" {
		t.Errorf("username field = %+v", got)
	}
	if got := newField(f, "password", "Password", "password"); got.Value != "" || got.Error != "Wrong." {
		t.Errorf("password field = %+v", got)
	}
}

func TestAssetsAreFingerprinted(t *testing.T) {
	a, err := loadAssets()
	if err != nil {
		t.Fatal(err)
	}
	u, err := a.url("css/gitman.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(u, "/assets/static/gitman.") || !strings.HasSuffix(u, ".css") || strings.Count(u, "/") != 3 {
		t.Errorf("url = %q, want a fingerprinted name", u)
	}
	if _, err := a.url("css/missing.css"); err == nil {
		t.Error("expected an unknown asset to be an error")
	}
}

func TestRawContentNeverRunsAsAPage(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"index.html", []byte("<script>alert(1)</script>"), "text/plain; charset=utf-8"},
		{"evil.js", []byte("fetch('/people', {method: 'POST'})"), "text/plain; charset=utf-8"},
		{"logo.svg", []byte(`<svg onload="alert(1)"/>`), "text/plain; charset=utf-8"},
		{"page.xhtml", []byte("<html/>"), "text/plain; charset=utf-8"},
		{"app.py", []byte("print('hi')\n"), "text/plain; charset=utf-8"},
		{"logo.png", png, "image/png"},
		{"fake.png", []byte("<script>alert(1)</script>"), "text/plain; charset=utf-8"},
		{"tool.bin", []byte("\x00\x01\x02"), "application/octet-stream"},
	}
	for _, c := range cases {
		if got := rawContentType(c.name, c.data); got != c.want {
			t.Errorf("rawContentType(%s) = %q, want %q", c.name, got, c.want)
		}
	}

	h := http.Header{}
	setRawHeaders(h, "text/plain; charset=utf-8", `we"ird;name.txt`)
	if got := h.Get("Content-Security-Policy"); got != "default-src 'none'; sandbox" {
		t.Errorf("raw responses must be sandboxed, got CSP %q", got)
	}
	if h.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("raw responses must forbid content sniffing")
	}
	if _, params, err := mime.ParseMediaType(h.Get("Content-Disposition")); err != nil || params["filename"] != `we"ird;name.txt` {
		t.Errorf("Content-Disposition %q does not round-trip the filename: %v", h.Get("Content-Disposition"), err)
	}
	h = http.Header{}
	setRawHeaders(h, "application/octet-stream", "tool.bin")
	if !strings.HasPrefix(h.Get("Content-Disposition"), "attachment") {
		t.Errorf("a binary is not downloaded: %q", h.Get("Content-Disposition"))
	}
}

// TestSlowRequestBodiesAreCutOff sends a sign-in whose body never
// arrives in full, as a client holding connections open would: the page
// deadline must end the request instead of waiting forever.
func TestSlowRequestBodiesAreCutOff(t *testing.T) {
	app, err := New(&config.Config{PublicURL: "http://gitman.test", Port: 8080}, Services{},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	app.pageReadTimeout = 200 * time.Millisecond
	server := httptest.NewServer(app.handler)
	defer server.Close()

	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprint(conn, "POST /login HTTP/1.1\r\nHost: gitman.test\r\nContent-Type: application/x-www-form-urlencoded\r\n"+
		"Content-Length: 1000\r\n\r\nusername=a")
	start := time.Now()
	_ = conn.SetReadDeadline(start.Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("no response to a stalled request body: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity || time.Since(start) > 3*time.Second {
		t.Fatalf("stalled body answered %d after %s", resp.StatusCode, time.Since(start))
	}
}

// TestPartialsRead renders shared pieces of pages as a person reads them:
// the timeline's wording, a diff whose patch was left out, and Home's
// in-progress list for a run of a bare commit.
func TestPartialsRead(t *testing.T) {
	assets, err := loadAssets()
	if err != nil {
		t.Fatal(err)
	}
	views, err := loadViews(assets)
	if err != nil {
		t.Fatal(err)
	}
	render := func(name string, data any) string {
		t.Helper()
		var buf strings.Builder
		if err := views.pages["home"].ExecuteTemplate(&buf, name, data); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}

	out := render("feed", map[string]any{"ShowRepo": false, "Entries": []activity.Entry{
		{Kind: activity.KindPush, RepoName: "w", Actor: "darius", UpdateCount: 3, At: time.Now()},
	}})
	if !strings.Contains(out, "</span> pushed 3 refs") {
		t.Errorf("a push of several refs reads:\n%s", out)
	}

	out = render("diff", map[string]any{"Diff": &git.Diff{Files: []git.DiffFile{
		{Status: git.StatusModified, OldPath: "a", NewPath: "a", Additions: 5, PatchTruncated: true},
	}}})
	if !strings.Contains(out, "the diff reached its size limit") || strings.Contains(out, "No line changes") {
		t.Errorf("a file whose patch was left out reads:\n%s", out)
	}

	out = render("content", pageData{Person: &auth.Person{Username: "darius"}, Data: homePage{
		CreateForm: newForm(nil),
		InProgress: []ci.Summary{{RepoName: "w", Number: 7, Status: ci.StatusQueued, QueuedAt: time.Now()}},
	}})
	if !strings.Contains(out, `Run #7</a>
              <span class="chip">a commit</span>`) {
		t.Errorf("a bare commit's run in progress reads:\n%s", out)
	}
}

// TestRenderErrorMapsPoolExhaustionToServiceUnavailable covers R2-4: a
// database call refused for want of a free connection reaches the person
// as 503 with a Retry-After header, distinct from an ordinary 500.
func TestRenderErrorMapsPoolExhaustionToServiceUnavailable(t *testing.T) {
	a := renderingApp(t)
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantRetry  bool
	}{
		{"pool exhausted", fmt.Errorf("list repos: %w", postgres.ErrUnavailable), http.StatusServiceUnavailable, true},
		{"kind unavailable directly", apperr.New(apperr.KindUnavailable, "try again"), http.StatusServiceUnavailable, false},
		{"not found stays not found", postgres.ErrNotFound, http.StatusNotFound, false},
		{"a plain error stays internal", errors.New("boom"), http.StatusInternalServerError, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			a.renderError(w, r, c.err)
			if w.Code != c.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, c.wantStatus)
			}
			if got := w.Header().Get("Retry-After"); c.wantRetry && got == "" {
				t.Error("expected a Retry-After header")
			} else if !c.wantRetry && got != "" {
				t.Errorf("unexpected Retry-After header: %q", got)
			}
		})
	}
}

// TestRefURLEscapesNamesThatEndAPath is a branch or file whose name holds
// "#" or "?": a link to it must reach it, not stop where a fragment or a
// query would begin.
func TestRefURLEscapesNamesThatEndAPath(t *testing.T) {
	cases := map[[3]string]string{
		{"w", "feat#1", ""}:                "/w@feat%231",
		{"w", "main", "docs/a#b.txt"}:      "/w@main/docs/a%23b.txt",
		{"w", "release/1.2", "q?x.txt"}:    "/w@release/1.2/q%3Fx.txt",
		{"w", "main", "space in name.txt"}: "/w@main/space%20in%20name.txt",
	}
	for in, want := range cases {
		if got := refURL(in[0], in[1], in[2]); got != want {
			t.Errorf("refURL%q = %q, want %q", in, got, want)
		}
	}
}
