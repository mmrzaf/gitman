package handlers

import (
	"context"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
)

func setupTestApp(t *testing.T) *App {
	t.Helper()
	database, err := db.InitDB("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	_, err = database.CreateUser(ctx, "testuser", "TestPass123")
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}
	cfg := &config.Config{
		Port:              "8080",
		ReposPath:         t.TempDir(),
		SSHUser:           "git",
		ServerHost:        "localhost",
		ArtifactsPath:     t.TempDir(),
		InternalURL:       "http://localhost:8080",
		SecretKey:         "testsecretkey",
		WorkerConcurrency: 1,
	}

	// Minimal templates that properly render errors and content.
	tmpl := map[string]*template.Template{
		"home.html":     template.Must(template.New("").Parse(`{{define "base.html"}}home{{end}}`)),
		"login.html":    template.Must(template.New("").Parse(`{{define "base.html"}}{{if .Error}}<div class="error">{{.Error}}</div>{{else}}login page{{end}}{{end}}`)),
		"register.html": template.Must(template.New("").Parse(`{{define "base.html"}}{{if .Error}}<div class="error">{{.Error}}</div>{{else}}register page{{end}}{{end}}`)),
		"repos.html": template.Must(template.New("").Parse(`
			{{define "base.html"}}
			{{range .Data.Repos}}<span>{{.Name}}</span>{{end}}
			{{end}}`)),
		"keys.html": template.Must(template.New("").Parse(`
			{{define "base.html"}}
			{{range .Data.Keys}}<span>{{.Name}}</span>{{end}}
			{{end}}`)),
		"tokens.html": template.Must(template.New("").Parse(`
			{{define "base.html"}}
			{{range .Data.Tokens}}<span>{{.Name}}</span>{{end}}
			{{end}}`)),
	}

	return &App{
		Config:    cfg,
		DB:        database,
		Templates: tmpl,
	}
}

func loginUser(t *testing.T, app *App) *http.Cookie {
	t.Helper()
	form := url.Values{
		"username": {"testuser"},
		"password": {"TestPass123"},
	}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	app.HandleLoginPOST(w, req)
	resp := w.Result()
	for _, c := range resp.Cookies() {
		if c.Name == "session_token" {
			return c
		}
	}
	t.Fatal("login failed, no session token cookie")
	return nil
}

func TestHandleLoginGET(t *testing.T) {
	app := setupTestApp(t)
	req := httptest.NewRequest("GET", "/login", nil)
	w := httptest.NewRecorder()
	app.HandleLoginGET(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestHandleLoginPOSTSuccess(t *testing.T) {
	app := setupTestApp(t)
	cookie := loginUser(t, app)
	if cookie.Value == "" {
		t.Error("missing session token")
	}
}

func TestHandleLoginPOSTInvalid(t *testing.T) {
	app := setupTestApp(t)
	form := url.Values{"username": {"testuser"}, "password": {"wrong"}}
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	app.HandleLoginPOST(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Invalid username or password") {
		t.Errorf("expected error message, got %s", body)
	}
}

func TestHandleRegisterGET(t *testing.T) {
	app := setupTestApp(t)
	req := httptest.NewRequest("GET", "/register", nil)
	w := httptest.NewRecorder()
	app.HandleRegisterGET(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200")
	}
}

func TestHandleRegisterPOSTSuccess(t *testing.T) {
	app := setupTestApp(t)
	form := url.Values{
		"username": {"newuser"},
		"password": {"NewPass123"},
	}
	req := httptest.NewRequest("POST", "/register", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	app.HandleRegisterPOST(w, req)
	resp := w.Result()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("expected redirect, got %d", resp.StatusCode)
	}
	u, err := app.DB.GetUserByUsername(context.Background(), "newuser")
	if err != nil || u == nil {
		t.Fatal("user not created")
	}
}

func TestHandleLogout(t *testing.T) {
	app := setupTestApp(t)
	cookie := loginUser(t, app)
	req := httptest.NewRequest("GET", "/logout", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	app.HandleLogout(w, req)
	resp := w.Result()
	cleared := false
	for _, c := range resp.Cookies() {
		if c.Name == "session_token" && c.MaxAge == -1 {
			cleared = true
			break
		}
	}
	if !cleared {
		t.Error("logout did not clear session cookie")
	}
}

func TestHandleReposGET(t *testing.T) {
	app := setupTestApp(t)
	user, _ := app.DB.GetUserByUsername(context.Background(), "testuser")
	req := httptest.NewRequest("GET", "/repos", nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	w := httptest.NewRecorder()
	app.HandleReposGET(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestHandleKeysGET(t *testing.T) {
	app := setupTestApp(t)
	user, _ := app.DB.GetUserByUsername(context.Background(), "testuser")
	req := httptest.NewRequest("GET", "/keys", nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	w := httptest.NewRecorder()
	app.HandleKeysGET(w, req)
	if w.Code != 200 {
		t.Errorf("expected 200")
	}
}
