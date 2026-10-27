package web

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// releaseNotesJSON is the curated, user-facing list of what changed in each
// release. It is separate from CHANGELOG.md, which also records installation
// and operational detail that does not belong in front of everyone who signs
// in.
//
//go:embed releasenotes.json
var releaseNotesJSON []byte

const releaseDateLayout = "2006-01-02"

// releaseNote is what one release changed, newest first in the file.
type releaseNote struct {
	Version    string   `json:"version"`
	ReleasedOn string   `json:"releasedOn"`
	Items      []string `json:"items"`

	released time.Time
}

// Released is the release date, for display.
func (n releaseNote) Released() time.Time { return n.released }

// loadReleaseNotes decodes and validates the embedded notes.
func loadReleaseNotes() ([]releaseNote, error) { return parseReleaseNotes(releaseNotesJSON) }

// parseReleaseNotes decodes and validates release notes: unique versions,
// valid dates and at least one item each, newest first.
func parseReleaseNotes(data []byte) ([]releaseNote, error) {
	var file struct {
		Releases []releaseNote `json:"releases"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		return nil, fmt.Errorf("decode release notes: %w", err)
	}
	if len(file.Releases) == 0 {
		return nil, fmt.Errorf("release notes list no releases")
	}
	seen := make(map[string]bool, len(file.Releases))
	for i := range file.Releases {
		note := &file.Releases[i]
		version := normalizeVersion(note.Version)
		if version == "" || version != note.Version {
			return nil, fmt.Errorf("release notes: version %q must be set without a leading v", note.Version)
		}
		if seen[version] {
			return nil, fmt.Errorf("release notes: version %s is listed twice", version)
		}
		seen[version] = true
		released, err := time.Parse(releaseDateLayout, note.ReleasedOn)
		if err != nil {
			return nil, fmt.Errorf("release notes: %s has an invalid date %q", version, note.ReleasedOn)
		}
		note.released = released
		if len(note.Items) == 0 {
			return nil, fmt.Errorf("release notes: %s lists no changes", version)
		}
		if i > 0 && released.After(file.Releases[i-1].released) {
			return nil, fmt.Errorf("release notes: %s is newer than the release listed before it; list the newest release first", version)
		}
	}
	return file.Releases, nil
}

// normalizeVersion drops the leading v a tag build carries, so v1.0.0-beta.24
// and 1.0.0-beta.24 name the same release.
func normalizeVersion(version string) string {
	version = strings.TrimSpace(version)
	if len(version) > 1 && (version[0] == 'v' || version[0] == 'V') && version[1] >= '0' && version[1] <= '9' {
		return version[1:]
	}
	return version
}

// releaseNotesFor picks the notes for the running version and the releases
// older than it. A build whose version is not listed (a development build, or
// a release without notes) gets the newest release, flagged as inexact.
func releaseNotesFor(releases []releaseNote, version string) (current releaseNote, exact bool, earlier []releaseNote) {
	normalized := normalizeVersion(version)
	index := 0
	for i, note := range releases {
		if note.Version == normalized {
			index, exact = i, true
			break
		}
	}
	return releases[index], exact, releases[index+1:]
}
