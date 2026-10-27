package web

import (
	"net/http"
	"strings"
	"testing"
)

func TestAccountMenuShowsTheVersionAndLinksToWhatsNew(t *testing.T) {
	database, b := setupVersion(t, "v1.0.0-beta.23")
	signIn(t, database, b, "member", false)

	resp, body := b.do(http.MethodGet, "/", nil, nil)
	expect(t, resp, body, http.StatusOK, `href="/changelog"`, "Gitman v1.0.0-beta.23")
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
	database, b := setupVersion(t, "v1.0.0-beta.23")
	signIn(t, database, b, "member", false)

	resp, body := b.do(http.MethodGet, "/changelog", nil, nil)
	expect(t, resp, body, http.StatusOK,
		"What's new", "You are running Gitman", "v1.0.0-beta.23",
		"Gitman 1.0.0-beta.23", "Current version", "Released 27 October 2026",
		"Maintenance tab", "Earlier releases", "Gitman 1.0.0-beta.22", "Commits is a section of its own")
	if strings.Contains(body, "Gitman 1.0.0-beta.24") {
		t.Fatalf("a newer release is listed as an earlier one:\n%s", body)
	}
	if strings.Contains(body, "Latest release") {
		t.Fatalf("the running version's notes are labelled as the latest release:\n%s", body)
	}
}

func TestWhatsNewFallsBackToTheNewestReleaseForAnUnlistedBuild(t *testing.T) {
	database, b := setupVersion(t, "dev")
	signIn(t, database, b, "member", false)

	resp, body := b.do(http.MethodGet, "/changelog", nil, nil)
	expect(t, resp, body, http.StatusOK, "Latest release", "There are no notes for this build", "Gitman 1.0.0-beta.25")
	if strings.Contains(body, "Current version") {
		t.Fatalf("an unlisted build is labelled as having its own notes:\n%s", body)
	}
}

func TestWhatsNewIsForSignedInPeopleOnly(t *testing.T) {
	_, b := setupVersion(t, "v1.0.0-beta.24")

	resp, _ := b.do(http.MethodGet, "/changelog", nil, nil)
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/login?next=") {
		t.Fatalf("signed-out /changelog = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}
