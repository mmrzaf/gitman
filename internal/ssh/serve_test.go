package ssh

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/mmrzaf/gitman/internal/apperr"
)

func TestParseGitSSHCommand(t *testing.T) {
	tests := []struct {
		input   string
		action  string
		owner   string
		repo    string
		wantErr bool
	}{
		{"git-upload-pack 'alice/project.git'", "git-upload-pack", "alice", "project", false},
		{"git-receive-pack \"alice/project.git\"", "git-receive-pack", "alice", "project", false},
		{"git-upload-archive 'alice/project.git'", "git-upload-archive", "alice", "project", false},
		{"bash -c 'id'", "", "", "", true},
		{"git-upload-pack alice/project.git", "", "", "", true},
		{"git-upload-pack '../project.git'", "", "", "", true},
	}
	for _, tt := range tests {
		got, err := parseGitSSHCommand(tt.input)
		if tt.wantErr {
			if err == nil {
				t.Fatalf("parse %q unexpectedly succeeded: %+v", tt.input, got)
			}
			continue
		}
		if err != nil || got.Action != tt.action || got.Owner != tt.owner || got.RepoName != tt.repo {
			t.Fatalf("parse %q = %+v, %v", tt.input, got, err)
		}
	}
}

func TestServeGreetingDoesNotRequireRepositoryState(t *testing.T) {
	var stdout bytes.Buffer
	if err := Serve(context.Background(), "unused", "", nil, nil, strings.NewReader(""), &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "successfully authenticated") {
		t.Fatalf("unexpected greeting: %q", stdout.String())
	}
}

func TestServeRejectsMissingDependenciesWithoutPanicking(t *testing.T) {
	err := Serve(context.Background(), "key", "git-upload-pack 'alice/project.git'", nil, nil, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !apperr.Is(err, apperr.KindInternal) {
		t.Fatalf("error = %v, want internal error", err)
	}
}
