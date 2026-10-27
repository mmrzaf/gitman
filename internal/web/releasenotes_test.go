package web

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestEmbeddedReleaseNotesAreValid(t *testing.T) {
	notes, err := loadReleaseNotes()
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) == 0 {
		t.Fatal("no release notes")
	}
}

// The notes are written by hand for each release, so a release that forgets
// them must fail here rather than show the previous release's notes as its own.
func TestNewestReleaseNotesMatchTheChangelog(t *testing.T) {
	notes, err := loadReleaseNotes()
	if err != nil {
		t.Fatal(err)
	}
	changelog, err := os.ReadFile("../../CHANGELOG.md")
	if err != nil {
		t.Fatal(err)
	}
	heading := regexp.MustCompile(`(?m)^## \[([^\]]+)\] - (\d{4}-\d{2}-\d{2})$`).FindSubmatch(changelog)
	if heading == nil {
		t.Fatal("CHANGELOG.md has no released version")
	}
	if string(heading[1]) != notes[0].Version {
		t.Fatalf("newest CHANGELOG release is %s but the newest release notes are for %s", heading[1], notes[0].Version)
	}
	if string(heading[2]) != notes[0].ReleasedOn {
		t.Fatalf("%s is dated %s in CHANGELOG.md and %s in the release notes", notes[0].Version, heading[2], notes[0].ReleasedOn)
	}
}

func TestParseReleaseNotesRejectsMalformedNotes(t *testing.T) {
	valid := `{"version":"1.0.0-beta.2","releasedOn":"2026-02-01","items":["b"]},{"version":"1.0.0-beta.1","releasedOn":"2026-01-01","items":["a"]}`
	if _, err := parseReleaseNotes([]byte(`{"releases":[` + valid + `]}`)); err != nil {
		t.Fatalf("valid notes: %v", err)
	}
	for name, data := range map[string]string{
		"empty":         `{"releases":[]}`,
		"not json":      `releases`,
		"unknown field": `{"releases":[{"version":"1.0.0","releasedOn":"2026-01-01","items":["a"],"extra":1}]}`,
		"leading v":     `{"releases":[{"version":"v1.0.0","releasedOn":"2026-01-01","items":["a"]}]}`,
		"no version":    `{"releases":[{"version":"","releasedOn":"2026-01-01","items":["a"]}]}`,
		"bad date":      `{"releases":[{"version":"1.0.0","releasedOn":"01/02/2026","items":["a"]}]}`,
		"no items":      `{"releases":[{"version":"1.0.0","releasedOn":"2026-01-01","items":[]}]}`,
		"duplicate":     `{"releases":[{"version":"1.0.0","releasedOn":"2026-02-01","items":["a"]},{"version":"1.0.0","releasedOn":"2026-01-01","items":["a"]}]}`,
		"oldest first":  `{"releases":[{"version":"1.0.0","releasedOn":"2026-01-01","items":["a"]},{"version":"1.0.1","releasedOn":"2026-02-01","items":["a"]}]}`,
	} {
		if _, err := parseReleaseNotes([]byte(data)); err == nil {
			t.Errorf("%s: parseReleaseNotes accepted %s", name, data)
		}
	}
}

// Two releases can ship on one day, as beta 23 and beta 24 did.
func TestParseReleaseNotesAcceptsReleasesOnTheSameDay(t *testing.T) {
	notes, err := parseReleaseNotes([]byte(`{"releases":[
		{"version":"1.0.1","releasedOn":"2026-01-01","items":["b"]},
		{"version":"1.0.0","releasedOn":"2026-01-01","items":["a"]}]}`))
	if err != nil || len(notes) != 2 {
		t.Fatalf("parseReleaseNotes = %v, %v", notes, err)
	}
}

func TestReleaseNotesForPicksTheRunningVersion(t *testing.T) {
	notes, err := parseReleaseNotes([]byte(`{"releases":[
		{"version":"1.0.0-beta.3","releasedOn":"2026-03-01","items":["c"]},
		{"version":"1.0.0-beta.2","releasedOn":"2026-02-01","items":["b"]},
		{"version":"1.0.0-beta.1","releasedOn":"2026-01-01","items":["a"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		version     string
		want        string
		exact       bool
		earlierWant []string
	}{
		{"v1.0.0-beta.3", "1.0.0-beta.3", true, []string{"1.0.0-beta.2", "1.0.0-beta.1"}},
		{"1.0.0-beta.2", "1.0.0-beta.2", true, []string{"1.0.0-beta.1"}},
		{"v1.0.0-beta.1", "1.0.0-beta.1", true, nil},
		{"dev", "1.0.0-beta.3", false, []string{"1.0.0-beta.2", "1.0.0-beta.1"}},
		{"", "1.0.0-beta.3", false, []string{"1.0.0-beta.2", "1.0.0-beta.1"}},
		{"v9.9.9", "1.0.0-beta.3", false, []string{"1.0.0-beta.2", "1.0.0-beta.1"}},
	} {
		current, exact, earlier := releaseNotesFor(notes, tc.version)
		var got []string
		for _, note := range earlier {
			got = append(got, note.Version)
		}
		if current.Version != tc.want || exact != tc.exact || strings.Join(got, ",") != strings.Join(tc.earlierWant, ",") {
			t.Errorf("releaseNotesFor(%q) = %s exact=%v earlier=%v, want %s exact=%v earlier=%v",
				tc.version, current.Version, exact, got, tc.want, tc.exact, tc.earlierWant)
		}
	}
}

func TestNormalizeVersion(t *testing.T) {
	for in, want := range map[string]string{
		"v1.0.0-beta.24": "1.0.0-beta.24",
		"V1.0.0":         "1.0.0",
		" 1.0.0 ":        "1.0.0",
		"dev":            "dev",
		"v":              "v",
		"":               "",
	} {
		if got := normalizeVersion(in); got != want {
			t.Errorf("normalizeVersion(%q) = %q, want %q", in, got, want)
		}
	}
}
