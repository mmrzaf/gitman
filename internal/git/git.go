// Package git is Gitman's only interface to Git repositories. It shells
// out to the git binary — Git itself stays the authority on repository
// content — and wraps every invocation with a sanitized environment, a
// timeout, and a cap on how much output it will buffer.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Defaults for a single git invocation. Transport commands (clone, fetch,
// push) pass their own, longer, timeout.
const (
	defaultTimeout   = 2 * time.Minute
	defaultMaxOutput = 64 << 20
	stderrTail       = 8 << 10
	// waitDelay bounds how long Wait may block after a killed process's
	// pipes should have closed, so a process that ignores its context
	// cancellation cannot hang cleanup forever.
	waitDelay = 5 * time.Second
	// readBufferSize is the buffered-reader size for streaming git output
	// a line or a NUL-delimited record at a time.
	readBufferSize = 64 << 10
)

// ErrOutputLimit is returned when a git command produces more output than
// the caller allowed it to buffer.
var ErrOutputLimit = errors.New("git output exceeded the size limit")

// Error describes a git invocation that exited unsuccessfully.
type Error struct {
	Args     []string
	ExitCode int
	Stderr   string
	Err      error
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = e.Err.Error()
	}
	return fmt.Sprintf("git %s: %s", firstArg(e.Args), msg)
}

func (e *Error) Unwrap() error { return e.Err }

func firstArg(args []string) string {
	for i, a := range args {
		if a == "-c" {
			continue
		}
		if i > 0 && args[i-1] == "-c" {
			continue
		}
		return a
	}
	return ""
}

// exitCode reports the exit code of a git invocation that failed with an
// *Error, or -1 otherwise.
func exitCode(err error) int {
	var gitErr *Error
	if errors.As(err, &gitErr) {
		return gitErr.ExitCode
	}
	return -1
}

// baseEnv is the environment every git invocation starts from. It keeps
// the invoking user's and the system's Git configuration out of the
// picture, so behavior depends only on the repository and on the flags
// Gitman passes, and never prompts for anything.
func baseEnv() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.TempDir(),
		"LANG=C",
		"LC_ALL=C",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_NO_REPLACE_OBJECTS=1",
	}
}

type cmdOptions struct {
	dir       string
	env       []string
	stdin     io.Reader
	stdout    io.Writer // nil captures output, subject to maxOutput
	maxOutput int64
	timeout   time.Duration
}

// run executes git with args and returns its captured standard output.
func run(ctx context.Context, o cmdOptions, args ...string) ([]byte, error) {
	timeout := o.timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = o.dir
	cmd.Env = append(baseEnv(), o.env...)
	cmd.Stdin = o.stdin
	cmd.WaitDelay = waitDelay

	var captured *limitedBuffer
	if o.stdout != nil {
		cmd.Stdout = o.stdout
	} else {
		max := o.maxOutput
		if max <= 0 {
			max = defaultMaxOutput
		}
		captured = &limitedBuffer{max: max}
		cmd.Stdout = captured
	}
	stderr := &tailBuffer{max: stderrTail}
	cmd.Stderr = stderr

	err := cmd.Run()
	if captured != nil && captured.exceeded {
		return nil, fmt.Errorf("git %s: %w", firstArg(args), ErrOutputLimit)
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, &Error{Args: args, ExitCode: -1, Stderr: stderr.String(), Err: ctxErr}
		}
		code := -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		}
		return nil, &Error{Args: args, ExitCode: code, Stderr: stderr.String(), Err: err}
	}
	if captured == nil {
		return nil, nil
	}
	return captured.Bytes(), nil
}

// errStopStream is returned by a stream consumer that has read all it
// wants; stream then stops git and reports success.
var errStopStream = errors.New("stop reading git output")

// stream runs git and hands its standard output to consume as it is
// produced. If consume returns errStopStream, git is stopped and stream
// returns nil: the caller got what it needed, and a truncated read is the
// intended outcome rather than a failure.
func stream(ctx context.Context, o cmdOptions, consume func(io.Reader) error, args ...string) error {
	timeout := o.timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = o.dir
	cmd.Env = append(baseEnv(), o.env...)
	cmd.WaitDelay = waitDelay
	stderr := &tailBuffer{max: stderrTail}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("git %s: %w", firstArg(args), err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("git %s: %w", firstArg(args), err)
	}

	consumeErr := consume(stdout)
	stopped := errors.Is(consumeErr, errStopStream)
	// Read before our own cancel below, which would otherwise put its
	// own reason here instead of a real timeout's.
	ctxErr := ctx.Err()
	if stopped || consumeErr != nil {
		cancel()
	}
	_, _ = io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()

	switch {
	case stopped:
		return nil
	case ctxErr != nil:
		code := -1
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			code = exitErr.ExitCode()
		}
		return &Error{Args: args, ExitCode: code, Stderr: stderr.String(), Err: ctxErr}
	case consumeErr != nil:
		return consumeErr
	case waitErr != nil:
		code := -1
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			code = exitErr.ExitCode()
		}
		return &Error{Args: args, ExitCode: code, Stderr: stderr.String(), Err: waitErr}
	}
	return nil
}

// limitedBuffer collects output until max bytes, then refuses further
// writes, which makes git fail with a broken pipe rather than letting an
// unexpectedly large output exhaust memory.
type limitedBuffer struct {
	bytes.Buffer
	max      int64
	exceeded bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if int64(b.Len()+len(p)) > b.max {
		b.exceeded = true
		return 0, ErrOutputLimit
	}
	return b.Buffer.Write(p)
}

// tailBuffer keeps only the last max bytes written, which is where git
// puts the line that explains a failure.
type tailBuffer struct {
	buf []byte
	max int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.max {
		b.buf = b.buf[len(b.buf)-b.max:]
	}
	return len(p), nil
}

func (b *tailBuffer) String() string { return string(b.buf) }
