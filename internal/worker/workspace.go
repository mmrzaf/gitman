package worker

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// Paths inside every step's container.
const (
	containerSource  = "/workspace"
	containerMeta    = "/gitman"
	containerSummary = containerMeta + "/summary"
)

// workspace is the directory a run works in on the worker host: the
// checked-out commit, mounted at /workspace in every step, and a
// metadata directory mounted at /gitman, which holds $GITMAN_SUMMARY.
type workspace struct {
	root string
}

func (ws workspace) source() string  { return filepath.Join(ws.root, "src") }
func (ws workspace) meta() string    { return filepath.Join(ws.root, "meta") }
func (ws workspace) summary() string { return filepath.Join(ws.meta(), "summary") }

// createWorkspace makes an empty workspace for a run under root. The
// directories are world-writable because a step's image may run as any
// user; a workspace exists only for the one run that owns it.
func createWorkspace(root, runID string) (workspace, error) {
	ws := workspace{root: filepath.Join(root, runID)}
	if err := os.RemoveAll(ws.root); err != nil {
		return ws, fmt.Errorf("clear workspace: %w", err)
	}
	for _, dir := range []string{ws.source(), ws.meta()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return ws, fmt.Errorf("create workspace: %w", err)
		}
		if err := os.Chmod(dir, 0o777); err != nil {
			return ws, fmt.Errorf("create workspace: %w", err)
		}
	}
	if err := os.WriteFile(ws.summary(), nil, 0o666); err != nil {
		return ws, fmt.Errorf("create summary file: %w", err)
	}
	if err := os.Chmod(ws.summary(), 0o666); err != nil {
		return ws, fmt.Errorf("create summary file: %w", err)
	}
	return ws, nil
}

func (ws workspace) remove() error {
	return os.RemoveAll(ws.root)
}

// checkoutTimeout bounds fetching one commit.
const checkoutTimeout = 10 * time.Minute

// checkout fetches exactly commit from repoURL and checks it out. The
// credentials travel in an HTTP header set through Git's environment,
// so the fetch token never appears in a command line or in .git/config.
func (ws workspace) checkout(ctx context.Context, repoURL, username, password, commit string) error {
	ctx, cancel := context.WithTimeout(ctx, checkoutTimeout)
	defer cancel()
	auth := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	env := append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.extraHeader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic "+auth,
	)
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"fetch", "--quiet", "--depth=1", "--no-tags", repoURL, commit},
		{"-c", "advice.detachedHead=false", "checkout", "--quiet", "--detach", "FETCH_HEAD"},
	} {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = ws.source()
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(lastLines(string(out), 5)))
		}
	}
	return nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// Limits on what a run's summary may hold.
const (
	maxSummaryBytes    = 256 << 10
	maxSummaryEntries  = 100
	maxSummaryValueLen = 1000
)

var summaryKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// readSummary parses the key=value lines a run wrote to $GITMAN_SUMMARY.
// A later line for the same key replaces an earlier one. Malformed lines,
// and lines longer than maxSummaryLine, are skipped; values are masked
// like the logs are, and cut at a length fit for display.
//
// The file belongs to the run's steps, which may have replaced it with a
// symbolic link to a file of the worker's, a named pipe or a device. It
// is read only while it is still a regular file: checked before it is
// opened, opened without following a link and without blocking, checked
// again once open, and read up to a fixed size.
func (ws workspace) readSummary(masker *secretMasker) (map[string]string, error) {
	info, err := os.Lstat(ws.summary())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read summary: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errSummaryNotAFile
	}
	f, err := os.OpenFile(ws.summary(), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read summary: %w", err)
	}
	defer func() { _ = f.Close() }()
	if info, err = f.Stat(); err != nil {
		return nil, fmt.Errorf("read summary: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errSummaryNotAFile
	}
	summary := map[string]string{}
	reader := bufio.NewReaderSize(io.LimitReader(f, maxSummaryBytes), maxSummaryLine)
	for {
		line, err := readSummaryLine(reader)
		if line != "" {
			addSummaryLine(summary, line, masker)
		}
		if err == io.EOF {
			return summary, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read summary: %w", err)
		}
	}
}

var errSummaryNotAFile = errors.New("read summary: $GITMAN_SUMMARY was replaced by something other than a file")

// maxSummaryLine is the longest summary line read; a longer one is
// skipped whole, and the lines after it are still read.
const maxSummaryLine = 64 << 10

// readSummaryLine returns the next line, or "" for a line longer than
// maxSummaryLine, which it reads past.
func readSummaryLine(r *bufio.Reader) (string, error) {
	line, isPrefix, err := r.ReadLine()
	if !isPrefix {
		return string(line), err
	}
	for isPrefix && err == nil {
		_, isPrefix, err = r.ReadLine()
	}
	return "", err
}

func addSummaryLine(summary map[string]string, line string, masker *secretMasker) {
	key, value, ok := strings.Cut(line, "=")
	key = strings.TrimSpace(key)
	if !ok || !summaryKeyPattern.MatchString(key) {
		return
	}
	if _, exists := summary[key]; !exists && len(summary) == maxSummaryEntries {
		return
	}
	value = storable(masker.mask(strings.TrimSpace(value)))
	summary[key] = cutAtRune(value, maxSummaryValueLen)
}

// workspaceRuns lists the runs that have a workspace under root.
func workspaceRuns(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	var runs []string
	for _, e := range entries {
		if e.IsDir() {
			runs = append(runs, e.Name())
		}
	}
	return runs, nil
}
