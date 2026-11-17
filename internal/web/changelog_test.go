package web

import (
	"net/http"
	"strings"
	"testing"
)

func TestAccountMenuShowsTheVersionAndLinksToWhatsNew(t *testing.T) {
	database, b := setupVersion(t, "v1.0.0")
	signIn(t, database, b, "member", false)

	resp, body := b.do(http.MethodGet, "/", nil, nil)
	expect(t, resp, body, http.StatusOK, `href="/changelog"`, "Gitman v1.0.0")
}

func TestAccountMenuHidesTheVersionLinkWhenTheBuildHasNoVersion(t *testing.T) {
	database, b := setup(t)
	signIn(t, database, b, "member", false)

	resp, body := b.do(http.MethodGet, "/", nil, nil)
	expect(t, resp, body, http.StatusOK, "Sign out")
	if strings.Contains(body, "/changelog") {
		t.Fatalf("the menu links to /changelog without a version:\n%s", body)
	}
}

func TestWhatsNewShowsTheRunningVersionAndOlderReleases(t *testing.T) {
	notes, err := loadReleaseNotes()
	if err != nil || len(notes) == 0 {
		t.Fatalf("release notes: %v, count=%d", err, len(notes))
	}
	current := notes[0]
	database, b := setupVersion(t, "v"+current.Version)
	signIn(t, database, b, "member", false)

	resp, body := b.do(http.MethodGet, "/changelog", nil, nil)
	expect(t, resp, body, http.StatusOK,
		"What's new", "You are running Gitman", "Gitman "+current.Version, "Current version")
	if len(notes) > 1 {
		expect(t, resp, body, http.StatusOK, "Earlier releases", "Gitman "+notes[1].Version)
	}
	if strings.Contains(body, "Latest release") {
		t.Fatal("the running version's notes are labelled as the latest release")
	}
}

func TestWhatsNewFallsBackToTheNewestReleaseForAnUnlistedBuild(t *testing.T) {
	notes, err := loadReleaseNotes()
	if err != nil {
		t.Fatal(err)
	}
	database, b := setupVersion(t, "dev")
	signIn(t, database, b, "member", false)

	resp, body := b.do(http.MethodGet, "/changelog", nil, nil)
	expect(t, resp, body, http.StatusOK, "Latest release", "There are no notes for this build", "Gitman "+notes[0].Version)
	if strings.Contains(body, "Current version") {
		t.Fatalf("an unlisted build is labelled as having its own notes:\n%s", body)
	}
}

func TestWhatsNewIsForSignedInPeopleOnly(t *testing.T) {
	_, b := setupVersion(t, "v1.0.0")

	resp, _ := b.do(http.MethodGet, "/changelog", nil, nil)
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/login?next=") {
		t.Fatalf("signed-out /changelog = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}
