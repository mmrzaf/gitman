package trigger

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mmrzaf/gitman/internal/git"
)

const (
	managedHookPrefix = "# gitman-managed-post-receive:"
	managedHookMarker = "# gitman-managed-post-receive:v1"
)

var ErrUnmanagedHook = errors.New("unmanaged post-receive hook exists")

type hookState uint8

const (
	hookAbsent hookState = iota
	hookManaged
	hookOutdated
	hookUnmanaged
)

func detectHookState(path string) (hookState, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return hookAbsent, nil
	}
	if err != nil {
		return 0, err
	}
	text := string(data)
	if strings.Contains(text, managedHookMarker) {
		return hookManaged, nil
	}
	// Hooks from the prior managed format are safe for Gitman to replace.
	if strings.Contains(text, managedHookPrefix) || strings.Contains(text, "# Managed by Gitman CI/CD.") || strings.Contains(text, "# Managed by Gitman CI.") {
		return hookOutdated, nil
	}
	return hookUnmanaged, nil
}

// EnsureHook makes Gitman's durable post-receive trigger hook an invariant of
// a managed repository. It never overwrites an operator-owned hook.
func (m *Manager) EnsureHook(ctx context.Context, owner, repo string) error {
	repoPath, err := git.SecureRepoPath(m.ReposPath, owner, repo)
	if err != nil {
		return err
	}
	if err := git.CheckBareRepository(ctx, repoPath); err != nil {
		return fmt.Errorf("verify managed repository: %w", err)
	}
	path := filepath.Join(repoPath, "hooks", "post-receive")
	state, err := detectHookState(path)
	if err != nil {
		return err
	}
	if state == hookManaged {
		return nil
	}
	if state == hookUnmanaged {
		return fmt.Errorf("%w: %s", ErrUnmanagedHook, path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeExecutableFileAtomic(path, buildHookScript(owner, repo))
}

// ReconcileAll ensures every registered repository has Gitman's current
// managed hook. Unmanaged hooks are reported and left untouched.
func (m *Manager) ReconcileAll(ctx context.Context) error {
	locations, err := m.DB.ListRepositoryLocations(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, location := range locations {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, err)...)
		}
		if err := m.EnsureHook(ctx, location.Owner, location.Name); err != nil {
			errs = append(errs, fmt.Errorf("%s/%s: %w", location.Owner, location.Name, err))
		}
	}
	return errors.Join(errs...)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func writeExecutableFileAtomic(path, content string) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".post-receive-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		if removeErr := os.Remove(tmpPath); removeErr != nil && !os.IsNotExist(removeErr) {
			err = errors.Join(err, removeErr)
		}
	}()
	closeTemp := func(cause error) error {
		return errors.Join(cause, tmp.Close())
	}
	if err := tmp.Chmod(0o700); err != nil {
		return closeTemp(err)
	}
	if _, err := tmp.WriteString(content); err != nil {
		return closeTemp(err)
	}
	if err := tmp.Sync(); err != nil {
		return closeTemp(err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	parent, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := parent.Sync()
	closeErr := parent.Close()
	return errors.Join(syncErr, closeErr)
}

func buildHookScript(owner, repo string) string {
	return fmt.Sprintf(`#!/bin/bash
%s
# Durable local delivery: events remain queued while the web process is down.
GITMAN_OWNER=%s
GITMAN_REPO=%s
HOOK_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
QUEUE_DIR="$HOOK_DIR/%s"
SEQUENCE_FILE="$QUEUE_DIR/.sequence"
umask 077

warn_queue_failure() {
    printf 'warning: Gitman could not queue CI for %%s/%%s\n' "$GITMAN_OWNER" "$GITMAN_REPO" >&2
    command -v logger >/dev/null 2>&1 && logger -t gitman-ci-hook -- "cannot persist CI event for $GITMAN_OWNER/$GITMAN_REPO"
}

if ! mkdir -p "$QUEUE_DIR"; then
    warn_queue_failure
    exit 0
fi

while read -r old new ref; do
    if [[ "$ref" != refs/heads/* && "$ref" != refs/tags/* ]]; then
        continue
    fi
    if [[ "$new" =~ ^0+$ ]]; then
        continue
    fi
    tmp="$(mktemp "$QUEUE_DIR/.event.XXXXXXXXXXXX")" || { warn_queue_failure; continue; }
    if printf '%%s\n%%s\n%%s\n' "$old" "$new" "$ref" > "$tmp"; then
        (
            flock -x 9 || exit 1
            sequence=0
            if [[ -f "$SEQUENCE_FILE" ]]; then
                IFS= read -r sequence < "$SEQUENCE_FILE" || exit 1
                [[ "$sequence" =~ ^[0-9]+$ ]] || exit 1
            fi
            next=$((10#$sequence + 1))
            while [[ -e "$(printf "$QUEUE_DIR/event-%%020d" "$next")" ||
                     -e "$(printf "$QUEUE_DIR/.pending-event-%%020d" "$next")" ||
                     -e "$(printf "$QUEUE_DIR/.processing-event-%%020d" "$next")" ]]; do
                next=$((next + 1))
            done
            pending="$(printf "$QUEUE_DIR/.pending-event-%%020d" "$next")"
            if ! mv "$tmp" "$pending" || ! sync -f "$QUEUE_DIR"; then
                exit 1
            fi
            sequence_tmp="$(mktemp "$QUEUE_DIR/.sequence.XXXXXXXXXXXX")" || exit 1
            if ! printf '%%s\n' "$next" > "$sequence_tmp" ||
               ! sync -f "$sequence_tmp" ||
               ! mv -f "$sequence_tmp" "$SEQUENCE_FILE"; then
                rm -f "$sequence_tmp"
                exit 1
            fi
            final="$(printf "$QUEUE_DIR/event-%%020d" "$next")"
            mv "$pending" "$final" && sync -f "$QUEUE_DIR"
        ) 9>"$QUEUE_DIR/.sequence.lock"
        if [[ -e "$tmp" ]]; then
            warn_queue_failure
            rm -f "$tmp"
        fi
    else
        warn_queue_failure
        rm -f "$tmp"
    fi
done
exit 0
`, managedHookMarker, shellQuote(owner), shellQuote(repo), QueueDirName)
}
