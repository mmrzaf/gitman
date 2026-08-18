package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// CommandError preserves the Git exit status and stderr so callers can
// distinguish an expected probe miss from an operational failure.
type CommandError struct {
	Args     []string
	ExitCode int
	Stderr   string
	Err      error
}

func (e *CommandError) Error() string {
	if e == nil {
		return ""
	}
	if e.Stderr != "" {
		return fmt.Sprintf("git %s failed (exit %d): %s", strings.Join(e.Args, " "), e.ExitCode, e.Stderr)
	}
	if e.Err != nil {
		return fmt.Sprintf("git %s failed (exit %d): %v", strings.Join(e.Args, " "), e.ExitCode, e.Err)
	}
	return fmt.Sprintf("git %s failed (exit %d)", strings.Join(e.Args, " "), e.ExitCode)
}

func (e *CommandError) Unwrap() error { return e.Err }

func commandError(ctx context.Context, args []string, stderr []byte, err error) error {
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	exitCode := -1
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		exitCode = exitErr.ExitCode()
	}
	return &CommandError{
		Args:     append([]string(nil), args...),
		ExitCode: exitCode,
		Stderr:   strings.TrimSpace(string(stderr)),
		Err:      err,
	}
}

func runGitTo(ctx context.Context, stdin io.Reader, stdout io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	return commandError(ctx, args, stderr.Bytes(), err)
}

func runGit(ctx context.Context, args ...string) ([]byte, error) {
	var stdout bytes.Buffer
	if err := runGitTo(ctx, nil, &stdout, args...); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}

func repoArgs(repoPath string, args ...string) []string {
	cmdArgs := make([]string, 0, len(args)+2)
	cmdArgs = append(cmdArgs, "-C", repoPath)
	cmdArgs = append(cmdArgs, args...)
	return cmdArgs
}

func run(ctx context.Context, repoPath string, args ...string) ([]byte, error) {
	return runGit(ctx, repoArgs(repoPath, args...)...)
}

func runTo(ctx context.Context, repoPath string, w io.Writer, args ...string) error {
	return runGitTo(ctx, nil, w, repoArgs(repoPath, args...)...)
}

func commandExitCode(err error) (int, bool) {
	var commandErr *CommandError
	if !errors.As(err, &commandErr) || commandErr.ExitCode < 0 {
		return 0, false
	}
	return commandErr.ExitCode, true
}
