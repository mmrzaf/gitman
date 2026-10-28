package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/mmrzaf/gitman/internal/auth"
)

func TestAdminOnlyPagesRequireAnAdmin(t *testing.T) {
	database, admin := setup(t)
	signIn(t, database, admin, "lead", true)
	member := newBrowser(t, admin.server)
	signIn(t, database, member, "member", false)
	signedOut := newBrowser(t, admin.server)

	for _, page := range []struct {
		path, title string
	}{
		{"/operations", "Repository recovery"},
		{"/workers", "Workers"},
		{"/images", "Images"},
		{"/sessions", "Sessions"},
	} {
		t.Run(page.path, func(t *testing.T) {
			resp, body := admin.do(http.MethodGet, page.path, nil, nil)
			expect(t, resp, body, http.StatusOK, page.title, "<h1")

			resp, body = member.do(http.MethodGet, page.path, nil, nil)
			expect(t, resp, body, http.StatusForbidden, "Only admins can open this page.")

			resp, _ = signedOut.do(http.MethodGet, page.path, nil, nil)
			if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/login?next=") {
				t.Fatalf("signed-out %s = %d %q", page.path, resp.StatusCode, resp.Header.Get("Location"))
			}
		})
	}
}

func TestRepoSettingsMaintenanceRunsForAnAdminOnly(t *testing.T) {
	database, admin := setup(t)
	signIn(t, database, admin, "lead", true)
	member := newBrowser(t, admin.server)
	signIn(t, database, member, "member", false)
	resp, body := admin.do(http.MethodPost, "/repos", url.Values{"name": {"maintained"}}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create repository: %d\n%s", resp.StatusCode, body)
	}

	resp, body = admin.do(http.MethodGet, "/maintained/settings?tab=maintenance", nil, nil)
	expect(t, resp, body, http.StatusOK, "Maintenance", "Size on disk", "/maintained/settings/maintenance")

	resp, body = member.do(http.MethodGet, "/maintained/settings?tab=maintenance", nil, nil)
	expect(t, resp, body, http.StatusForbidden, "Only admins may manage")

	resp, _ = admin.do(http.MethodPost, "/maintained/settings/maintenance", url.Values{}, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/maintained/settings?tab=maintenance" {
		t.Fatalf("maintenance = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	_, body = admin.do(http.MethodGet, "/maintained/settings?tab=maintenance", nil, nil)
	if !strings.Contains(body, "Repository maintenance finished") {
		t.Fatal("maintenance result was not shown")
	}
}

func TestSessionsPageListsAndRevokesAnotherPersonsSession(t *testing.T) {
	database, admin := setup(t)
	signIn(t, database, admin, "lead", true)
	member := newBrowser(t, admin.server)
	signIn(t, database, member, "member", false)

	sessions, err := auth.NewService(database).ListSessions(context.Background(), mustPerson(t, database, "member").ID)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("member sessions = %+v, %v", sessions, err)
	}
	resp, body := admin.do(http.MethodGet, "/sessions", nil, nil)
	expect(t, resp, body, http.StatusOK, "member", sessions[0].ID, "127.0.0.1", "this session",
		"Revoke this session?", "This is the session you are using, so you will be signed out.",
		"That browser is signed out at once.")

	resp, _ = admin.do(http.MethodPost, "/sessions/"+sessions[0].ID+"/revoke", url.Values{}, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/sessions" {
		t.Fatalf("revoke session = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	_, body = admin.do(http.MethodGet, "/sessions", nil, nil)
	if !strings.Contains(body, "Revoked member") {
		t.Fatal("the revocation flash was not shown")
	}

	resp, _ = member.do(http.MethodGet, "/", nil, nil)
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/login") {
		t.Fatalf("revoked member still signed in: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestAccountPageMarksAndRevokesItsOwnSessions(t *testing.T) {
	database, b := setup(t)
	signIn(t, database, b, "darius", false)
	other := newBrowser(t, b.server)
	resp, body := login(other, "darius", "correct-horse-battery", "/")
	expect(t, resp, body, http.StatusSeeOther)

	sessions, err := auth.NewService(database).ListSessions(context.Background(), mustPerson(t, database, "darius").ID)
	if err != nil || len(sessions) != 2 {
		t.Fatalf("sessions = %+v, %v", sessions, err)
	}
	resp, body = b.do(http.MethodGet, "/me?tab=sessions", nil, nil)
	expect(t, resp, body, http.StatusOK, "this session", "Sessions")

	_, currentID, err := auth.NewService(database).SessionIdentity(context.Background(), b.cookies["gitman_session"].Value)
	if err != nil {
		t.Fatal(err)
	}
	var target string
	for _, session := range sessions {
		if session.ID != currentID {
			target = session.ID
		}
	}
	if target == "" {
		t.Fatal("could not tell the two sessions apart")
	}
	resp, _ = b.do(http.MethodPost, "/me/sessions/"+target+"/revoke", url.Values{}, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/me?tab=sessions" {
		t.Fatalf("revoke own session = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	_, body = b.do(http.MethodGet, "/me?tab=sessions", nil, nil)
	if !strings.Contains(body, "Revoked that session.") {
		t.Fatal("the revocation flash was not shown")
	}
	resp, _ = other.do(http.MethodGet, "/", nil, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("the other session still works: %d", resp.StatusCode)
	}
}
