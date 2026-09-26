package push

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/mmrzaf/gitman/internal/git"
)

// Hook time limits. pre-receive holds the pusher's connection open while
// it runs; post-receive also creates runs, which read pipeline files.
const (
	preReceiveTimeout  = 2 * time.Minute
	postReceiveTimeout = 5 * time.Minute
)

// Timeout is how long the named hook may run.
func Timeout(name string) time.Duration {
	if name == PostReceive {
		return postReceiveTimeout
	}
	return preReceiveTimeout
}

// CheckDir verifies that dir — where Git started the hook, which for a
// bare repository is the repository itself — is the repository the push
// was authorized for.
func CheckDir(dir, reposPath, repoID string) error {
	want, err := git.RepoPath(reposPath, repoID)
	if err != nil {
		return err
	}
	if filepath.Clean(dir) != filepath.Clean(want) {
		return fmt.Errorf("hook is running in %s, not in the repository the push was authorized for", dir)
	}
	return nil
}

// Serve runs the named hook for the ref updates Git writes to stdin.
func (h *Hook) Serve(ctx context.Context, name string, stdin io.Reader) error {
	updates, err := ParseUpdates(stdin)
	if err != nil {
		return err
	}
	switch name {
	case PreReceive:
		return h.PreReceive(ctx, updates)
	case PostReceive:
		return h.PostReceive(ctx, updates)
	}
	return fmt.Errorf("unknown hook %q", name)
}

// Explain tells the pusher, on out, why the named hook failed with err,
// and returns err. A refusal needs no more: PreReceive has already given
// its reasons.
func Explain(out io.Writer, name string, err error) error {
	if err == nil || errors.Is(err, ErrRejected) {
		return err
	}
	if name == PostReceive {
		fmt.Fprintf(out, "Gitman accepted the push but could not record it: %v\n", err)
		fmt.Fprintln(out, "The next push to this repository brings its branch and tag list up to date.")
	} else {
		fmt.Fprintf(out, "Gitman could not check this push: %v\n", err)
	}
	return err
}
