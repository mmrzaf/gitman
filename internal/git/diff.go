package git

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// DiffLimits bound how much of a diff is parsed and returned.
type DiffLimits struct {
	// MaxFiles is the number of changed files listed; files past it are
	// counted by MoreFiles but not described.
	MaxFiles int
	// MaxLines is the total number of patch lines returned across every
	// file; once it is reached, remaining patches are marked truncated.
	MaxLines int
	// MaxLineBytes caps the length of one patch line.
	MaxLineBytes int
}

// DefaultDiffLimits are the limits the UI uses for commit and compare
// views.
var DefaultDiffLimits = DiffLimits{MaxFiles: 300, MaxLines: 20000, MaxLineBytes: 2000}

// FileStatus is how a file changed.
type FileStatus string

const (
	StatusAdded       FileStatus = "added"
	StatusModified    FileStatus = "modified"
	StatusDeleted     FileStatus = "deleted"
	StatusRenamed     FileStatus = "renamed"
	StatusCopied      FileStatus = "copied"
	StatusTypeChanged FileStatus = "type-changed"
)

// LineKind is the role of one line in a hunk.
type LineKind byte

const (
	LineContext   LineKind = ' '
	LineAdded     LineKind = '+'
	LineDeleted   LineKind = '-'
	LineNoNewline LineKind = '\\'
)

// DiffLine is one line of a hunk. OldNumber and NewNumber are zero on the
// side the line does not exist on.
type DiffLine struct {
	Kind      LineKind
	OldNumber int
	NewNumber int
	Text      string
	Truncated bool
}

// Hunk is one "@@ ... @@" section of a file's patch.
type Hunk struct {
	Header string
	Lines  []DiffLine
}

// DiffFile is one changed file.
type DiffFile struct {
	Status     FileStatus
	OldPath    string
	NewPath    string
	OldMode    string
	NewMode    string
	Similarity int
	Additions  int
	Deletions  int
	Binary     bool
	Hunks      []Hunk
	// PatchTruncated reports that this file's patch was cut short or left
	// out because the diff reached its line limit.
	PatchTruncated bool
}

// Path is the file's current path, or its old path if it was deleted.
func (f DiffFile) Path() string {
	if f.Status == StatusDeleted {
		return f.OldPath
	}
	return f.NewPath
}

// Diff is the set of changes between two commits.
type Diff struct {
	Files []DiffFile
	// MoreFiles counts changed files beyond DiffLimits.MaxFiles.
	MoreFiles int
}

// Diff describes the changes from commit from to commit to. An empty from
// diffs to against the empty tree, which is how a root commit's changes
// are shown.
func (r *Repo) Diff(ctx context.Context, from, to string, limits DiffLimits) (*Diff, error) {
	if !IsHash(to) {
		return nil, ErrNotFound
	}
	if from == "" {
		from = emptyTree(to)
	}
	if !IsHash(from) {
		return nil, ErrNotFound
	}

	raw, err := run(ctx, r.opts(), "diff-tree", "-r", "-z", "-M", "--raw", from, to)
	if err != nil {
		return nil, errNotFoundIfMissing(err)
	}
	files, total, err := parseRawDiff(raw, limits.MaxFiles)
	if err != nil {
		return nil, err
	}
	diff := &Diff{Files: files, MoreFiles: total - len(files)}
	if len(files) == 0 {
		return diff, nil
	}

	numstat, err := run(ctx, r.opts(), "diff-tree", "-r", "-z", "-M", "--numstat", from, to)
	if err != nil {
		return nil, errNotFoundIfMissing(err)
	}
	if err := applyNumstat(diff.Files, numstat); err != nil {
		return nil, err
	}

	err = stream(ctx, r.opts(), func(out io.Reader) error {
		return parsePatch(bufio.NewReaderSize(out, 64<<10), diff.Files, limits)
	}, "diff-tree", "-r", "-M", "-p", "--no-color", "--no-ext-diff", "-U3", from, to)
	if err != nil {
		return nil, errNotFoundIfMissing(err)
	}
	return diff, nil
}

// parseRawDiff parses `diff-tree -r -z --raw` output, describing at most
// maxFiles files and counting the rest.
func parseRawDiff(data []byte, maxFiles int) ([]DiffFile, int, error) {
	var files []DiffFile
	total := 0
	fields := bytes.Split(bytes.TrimSuffix(data, []byte{0}), []byte{0})
	for i := 0; i < len(fields); {
		meta := string(fields[i])
		if meta == "" {
			i++
			continue
		}
		if !strings.HasPrefix(meta, ":") {
			return nil, 0, fmt.Errorf("unexpected raw diff entry %q", meta)
		}
		parts := strings.Fields(meta[1:])
		if len(parts) != 5 {
			return nil, 0, fmt.Errorf("unexpected raw diff entry %q", meta)
		}
		status := parts[4]
		paths := 1
		if status[0] == 'R' || status[0] == 'C' {
			paths = 2
		}
		if i+paths >= len(fields) {
			return nil, 0, errors.New("truncated raw diff")
		}
		f := DiffFile{OldMode: parts[0], NewMode: parts[1]}
		switch status[0] {
		case 'A':
			f.Status, f.NewPath = StatusAdded, string(fields[i+1])
		case 'D':
			f.Status, f.OldPath = StatusDeleted, string(fields[i+1])
		case 'M':
			f.Status, f.OldPath, f.NewPath = StatusModified, string(fields[i+1]), string(fields[i+1])
		case 'T':
			f.Status, f.OldPath, f.NewPath = StatusTypeChanged, string(fields[i+1]), string(fields[i+1])
		case 'R', 'C':
			f.Status = StatusRenamed
			if status[0] == 'C' {
				f.Status = StatusCopied
			}
			f.OldPath, f.NewPath = string(fields[i+1]), string(fields[i+2])
			f.Similarity, _ = strconv.Atoi(status[1:])
		default:
			f.Status, f.OldPath, f.NewPath = StatusModified, string(fields[i+1]), string(fields[i+1])
		}
		f.OldPath = strings.ToValidUTF8(f.OldPath, "\uFFFD")
		f.NewPath = strings.ToValidUTF8(f.NewPath, "\uFFFD")
		total++
		if len(files) < maxFiles {
			files = append(files, f)
		}
		i += 1 + paths
	}
	return files, total, nil
}

// applyNumstat fills in additions, deletions and the binary flag from
// `diff-tree -r -z --numstat` output, whose entries are in the same order
// as the raw diff's.
func applyNumstat(files []DiffFile, data []byte) error {
	fields := bytes.Split(bytes.TrimSuffix(data, []byte{0}), []byte{0})
	idx := 0
	for i := 0; i < len(fields) && idx < len(files); {
		entry := string(fields[i])
		if entry == "" {
			i++
			continue
		}
		parts := strings.SplitN(entry, "\t", 3)
		if len(parts) != 3 {
			return fmt.Errorf("unexpected numstat entry %q", entry)
		}
		if parts[0] == "-" && parts[1] == "-" {
			files[idx].Binary = true
		} else {
			files[idx].Additions, _ = strconv.Atoi(parts[0])
			files[idx].Deletions, _ = strconv.Atoi(parts[1])
		}
		// A rename or copy leaves the path field empty and puts the old
		// and new paths in the next two NUL-separated fields.
		if parts[2] == "" {
			i += 3
		} else {
			i++
		}
		idx++
	}
	return nil
}

// parsePatch reads `diff-tree -p` output. Each "diff --git" section
// corresponds, in order, to one entry of files.
func parsePatch(r *bufio.Reader, files []DiffFile, limits DiffLimits) error {
	fileIdx := -1
	linesLeft := limits.MaxLines
	var hunk *Hunk
	var oldLine, newLine int

	finishHunk := func() {
		if hunk != nil && fileIdx >= 0 {
			files[fileIdx].Hunks = append(files[fileIdx].Hunks, *hunk)
		}
		hunk = nil
	}
	truncateRest := func(from int) {
		for i := from; i < len(files); i++ {
			if !files[i].Binary && (files[i].Additions > 0 || files[i].Deletions > 0) {
				files[i].PatchTruncated = true
			}
		}
	}

	for {
		line, truncated, err := readLine(r, limits.MaxLineBytes)
		if err != nil {
			if errors.Is(err, io.EOF) {
				finishHunk()
				return nil
			}
			return fmt.Errorf("read patch: %w", err)
		}

		if strings.HasPrefix(line, "diff --git ") {
			finishHunk()
			fileIdx++
			if fileIdx >= len(files) {
				return errStopStream
			}
			continue
		}
		if fileIdx < 0 {
			continue
		}

		if strings.HasPrefix(line, "@@ ") {
			finishHunk()
			if linesLeft <= 0 {
				truncateRest(fileIdx)
				return errStopStream
			}
			o, n, ok := parseHunkHeader(line)
			if !ok {
				return fmt.Errorf("malformed hunk header %q", line)
			}
			oldLine, newLine = o, n
			hunk = &Hunk{Header: line}
			continue
		}
		if hunk == nil || line == "" {
			continue
		}

		if linesLeft <= 0 {
			finishHunk()
			truncateRest(fileIdx)
			return errStopStream
		}
		dl := DiffLine{Kind: LineKind(line[0]), Text: line[1:], Truncated: truncated}
		switch dl.Kind {
		case LineContext:
			dl.OldNumber, dl.NewNumber = oldLine, newLine
			oldLine++
			newLine++
		case LineAdded:
			dl.NewNumber = newLine
			newLine++
		case LineDeleted:
			dl.OldNumber = oldLine
			oldLine++
		case LineNoNewline:
		default:
			continue
		}
		hunk.Lines = append(hunk.Lines, dl)
		linesLeft--
	}
}

// parseHunkHeader extracts the starting old and new line numbers from
// "@@ -a[,b] +c[,d] @@ ...".
func parseHunkHeader(line string) (oldStart, newStart int, ok bool) {
	fields := strings.Fields(line)
	if len(fields) < 3 || !strings.HasPrefix(fields[1], "-") || !strings.HasPrefix(fields[2], "+") {
		return 0, 0, false
	}
	parse := func(s string) (int, bool) {
		start, _, _ := strings.Cut(s[1:], ",")
		n, err := strconv.Atoi(start)
		return n, err == nil
	}
	o, ok1 := parse(fields[1])
	n, ok2 := parse(fields[2])
	return o, n, ok1 && ok2
}

// readLine reads one line without its newline, keeping at most max bytes
// of it and discarding the rest, so a single enormous line cannot grow
// memory without bound.
func readLine(r *bufio.Reader, max int) (string, bool, error) {
	var buf []byte
	truncated := false
	for {
		chunk, err := r.ReadSlice('\n')
		if len(buf) < max {
			room := max - len(buf)
			if len(chunk) > room {
				buf = append(buf, chunk[:room]...)
				truncated = true
			} else {
				buf = append(buf, chunk...)
			}
		} else if len(chunk) > 0 {
			truncated = true
		}
		if err == nil {
			break
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) && len(buf) > 0 {
			break
		}
		return "", false, err
	}
	line := strings.TrimSuffix(string(buf), "\n")
	return strings.ToValidUTF8(line, "\uFFFD"), truncated, nil
}
