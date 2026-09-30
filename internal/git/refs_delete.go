package git

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrRefMoved is returned when a ref changed while it was being deleted:
// another push or delete got to it first.
var ErrRefMoved = errors.New("the ref changed while it was being deleted")

// DeleteRef deletes a branch or tag and returns the object it pointed at:
// what a hook reports as the old value of a pushed delete. It returns
// ErrNotFound for a ref that is not there, and ErrRefMoved when the ref
// moved between being read and being deleted, which leaves it alone.
func (r *Repo) DeleteRef(ctx context.Context, kind Kind, name string) (old string, err error) {
	if err := ValidateName(name); err != nil {
		return "", ErrNotFound
	}
	full := FullName(kind, name)
	out, err := run(ctx, r.opts(), "rev-parse", "--verify", "--quiet", full)
	if err != nil {
		if exitCode(err) == 1 {
			return "", ErrNotFound
		}
		return "", err
	}
	old = strings.TrimSpace(string(out))
	if !IsHash(old) {
		return "", fmt.Errorf("unexpected rev-parse output %q", out)
	}
	if err := r.deleteRefIf(ctx, full, old); err != nil {
		return "", err
	}
	return old, nil
}

// deleteRefIf deletes the ref only while it still points at old.
func (r *Repo) deleteRefIf(ctx context.Context, full, old string) error {
	if _, err := run(ctx, r.opts(), "update-ref", "-d", full, old); err != nil {
		var gitErr *Error
		if errors.As(err, &gitErr) && strings.Contains(gitErr.Stderr, "but expected") {
			return ErrRefMoved
		}
		return err
	}
	return nil
}
