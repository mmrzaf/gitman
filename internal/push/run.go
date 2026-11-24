package push

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/mmrzaf/gitman/internal/git"
)

// Timeout bounds validation, Git application and durable recording together.
const Timeout = 5 * time.Minute

// CheckDir verifies that dir — where Git started the hook, which for a
// bare repository is the repository itself — is the repository the push
// was authorized for.
func CheckDir(dir, reposPath, repoID string) error {
	want, err := git.RepoPath(reposPath, repoID)
	if err != nil {
		return err
	}
	// Git canonicalizes the repository path before starting the hook, so a
	// data directory behind a symlink would otherwise look like a different
	// repository and every push would be refused.
	gotPath, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("resolve hook directory: %w", err)
	}
	wantPath, err := filepath.EvalSymlinks(want)
	if err != nil {
		return fmt.Errorf("resolve repository directory: %w", err)
	}
	if gotPath != wantPath {
		return fmt.Errorf("hook is running in %s, not in the repository the push was authorized for", dir)
	}
	return nil
}

// Explain tells the pusher, on out, why the named hook failed with err,
// and returns err. A refusal needs no more: validation has already given
// its reasons. A failure writing to out is left unchecked: out is the
// pusher's own connection, the only place such a failure could be
// reported, so there is nowhere to report it to.
func Explain(out io.Writer, err error) error {
	if err == nil || errors.Is(err, ErrRejected) {
		return err
	}
	_, _ = fmt.Fprintln(out, "Gitman could not complete this push. An operator can inspect Operations for pending recovery before retrying.")
	return err
}
