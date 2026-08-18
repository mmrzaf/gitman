package git

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultDiffFileLimit  = 200
	defaultPatchFileLimit = 60
	defaultPatchByteLimit = 512 * 1024
	perFilePatchByteLimit = 192 * 1024
	defaultPatchLineLimit = 3000
	perFilePatchLineLimit = 1000
)

// CommitDetail is the full presentation metadata for one immutable revision.
type CommitDetail struct {
	Hash     string
	Parents  []string
	Author   string
	Email    string
	Date     time.Time
	Subject  string
	Body     string
	Branches []string
	Tags     []string
}

// ChangedFile describes how one path changed in a commit. Path is the path in
// the commit being viewed; OldPath is populated for renames/copies and deletes.
type ChangedFile struct {
	Status     string
	StatusCode string
	OldPath    string
	Path       string
	Additions  int
	Deletions  int
	Binary     bool
	Similarity int
}

// DiffLine is one displayable line inside a unified diff hunk.
type DiffLine struct {
	Kind    string
	OldLine int
	NewLine int
	Text    string
}

// DiffHunk is a single @@ ... @@ section from a unified patch.
type DiffHunk struct {
	Header   string
	OldStart int
	NewStart int
	Lines    []DiffLine
}

// FileDiff combines stable path/status metadata with parsed patch hunks.
type FileDiff struct {
	ChangedFile
	Hunks     []DiffHunk
	Truncated bool
}

// CommitDiff is the complete diff presentation model for a commit. Files may
// be capped for pathological commits; TotalFiles always reports the true count
// observed before the presentation cap.
type CommitDiff struct {
	Files      []FileDiff
	TotalFiles int
	Additions  int
	Deletions  int
	Truncated  bool
}

func ResolveRevisionCommitHash(ctx context.Context, repoPath, ref string) (string, error) {
	resolved, err := resolveRef(ctx, repoPath, ref)
	if err != nil {
		return "", err
	}
	out, err := run(ctx, repoPath, "rev-parse", "--verify", "--quiet", resolved.spec+"^{commit}")
	if err != nil {
		if code, ok := commandExitCode(err); ok && code == 1 {
			return "", ErrRefNotFound
		}
		return "", fmt.Errorf("resolve revision commit: %w", err)
	}
	hash := strings.TrimSpace(string(out))
	if !canonicalGitHashRegex.MatchString(hash) {
		return "", fmt.Errorf("resolved revision is not a canonical commit hash")
	}
	return hash, nil
}

func GetCommitDetail(ctx context.Context, repoPath, hash string) (CommitDetail, error) {
	resolved, err := ResolveCommitHash(ctx, repoPath, hash)
	if err != nil {
		return CommitDetail{}, err
	}
	format := "%H%x00%P%x00%an%x00%ae%x00%aI%x00%s%x00%b"
	out, err := run(ctx, repoPath, "show", "-s", "--no-show-signature", "--format="+format, resolved)
	if err != nil {
		return CommitDetail{}, fmt.Errorf("read commit detail: %w", err)
	}
	parts := bytes.SplitN(bytes.TrimSuffix(out, []byte{'\n'}), []byte{0}, 7)
	if len(parts) != 7 {
		return CommitDetail{}, fmt.Errorf("malformed commit metadata")
	}
	date, err := time.Parse(time.RFC3339, string(parts[4]))
	if err != nil {
		return CommitDetail{}, fmt.Errorf("parse commit date: %w", err)
	}
	detail := CommitDetail{
		Hash:    string(parts[0]),
		Author:  string(parts[2]),
		Email:   string(parts[3]),
		Date:    date,
		Subject: string(parts[5]),
		Body:    strings.TrimSpace(string(parts[6])),
	}
	if parentText := strings.TrimSpace(string(parts[1])); parentText != "" {
		detail.Parents = strings.Fields(parentText)
	}
	detail.Branches, detail.Tags, err = refsPointingAt(ctx, repoPath, detail.Hash)
	if err != nil {
		return CommitDetail{}, err
	}
	return detail, nil
}

func refsPointingAt(ctx context.Context, repoPath, hash string) ([]string, []string, error) {
	out, err := run(ctx, repoPath, "for-each-ref", "--format=%(refname)%00%(objectname)%00%(*objectname)", "refs/heads", "refs/tags")
	if err != nil {
		return nil, nil, fmt.Errorf("list refs pointing at commit: %w", err)
	}
	var branches, tags []string
	for _, line := range bytes.Split(bytes.TrimSpace(out), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		parts := bytes.SplitN(line, []byte{0}, 3)
		if len(parts) != 3 {
			return nil, nil, fmt.Errorf("malformed ref metadata")
		}
		refName := string(parts[0])
		object := string(parts[1])
		peeled := string(parts[2])
		if object != hash && peeled != hash {
			continue
		}
		switch {
		case strings.HasPrefix(refName, "refs/heads/"):
			branches = append(branches, strings.TrimPrefix(refName, "refs/heads/"))
		case strings.HasPrefix(refName, "refs/tags/"):
			tags = append(tags, strings.TrimPrefix(refName, "refs/tags/"))
		}
	}
	sort.Strings(branches)
	sort.Strings(tags)
	return branches, tags, nil
}

func commitDiffArgs(detail CommitDetail, args ...string) []string {
	if len(detail.Parents) == 0 {
		base := []string{"diff-tree", "--root", "--no-commit-id", "-r"}
		return append(base, append(args, detail.Hash)...)
	}
	base := []string{"diff"}
	base = append(base, args...)
	base = append(base, detail.Parents[0], detail.Hash)
	return base
}

func GetCommitChanges(ctx context.Context, repoPath string, detail CommitDetail) ([]ChangedFile, error) {
	nameArgs := commitDiffArgs(detail, "--name-status", "-z", "--find-renames")
	nameOut, err := run(ctx, repoPath, nameArgs...)
	if err != nil {
		return nil, fmt.Errorf("read changed paths: %w", err)
	}
	changes, err := parseNameStatusZ(nameOut)
	if err != nil {
		return nil, err
	}

	numArgs := commitDiffArgs(detail, "--numstat", "-z", "--find-renames")
	numOut, err := run(ctx, repoPath, numArgs...)
	if err != nil {
		return nil, fmt.Errorf("read diff statistics: %w", err)
	}
	stats, err := parseNumstatZ(numOut)
	if err != nil {
		return nil, err
	}
	for i := range changes {
		key := diffPathKey(changes[i].OldPath, changes[i].Path)
		if stat, ok := stats[key]; ok {
			changes[i].Additions = stat.Additions
			changes[i].Deletions = stat.Deletions
			changes[i].Binary = stat.Binary
			continue
		}
		// Some Git versions report a simple path in numstat after a low-similarity
		// rename. Fall back to the destination path without weakening path parsing.
		if stat, ok := stats[diffPathKey("", changes[i].Path)]; ok {
			changes[i].Additions = stat.Additions
			changes[i].Deletions = stat.Deletions
			changes[i].Binary = stat.Binary
		}
	}
	return changes, nil
}

func parseNameStatusZ(out []byte) ([]ChangedFile, error) {
	records := bytes.Split(out, []byte{0})
	var changes []ChangedFile
	for i := 0; i < len(records); {
		if len(records[i]) == 0 {
			i++
			continue
		}
		statusToken := string(records[i])
		i++
		if statusToken == "" || len(statusToken) < 1 {
			return nil, fmt.Errorf("malformed name-status record")
		}
		code := statusToken[:1]
		change := ChangedFile{StatusCode: code, Status: statusLabel(code)}
		if (code == "R" || code == "C") && len(statusToken) > 1 {
			similarity, err := strconv.Atoi(statusToken[1:])
			if err != nil {
				return nil, fmt.Errorf("malformed rename/copy similarity %q: %w", statusToken[1:], err)
			}
			change.Similarity = similarity
		}
		if i >= len(records) || len(records[i]) == 0 {
			return nil, fmt.Errorf("malformed changed path record")
		}
		first := string(records[i])
		i++
		if code == "R" || code == "C" {
			if i >= len(records) || len(records[i]) == 0 {
				return nil, fmt.Errorf("malformed rename/copy record")
			}
			change.OldPath = first
			change.Path = string(records[i])
			i++
		} else {
			change.Path = first
			if code == "D" {
				change.OldPath = first
			}
		}
		changes = append(changes, change)
	}
	return changes, nil
}

type diffStat struct {
	Additions int
	Deletions int
	Binary    bool
}

func parseNumstatZ(out []byte) (map[string]diffStat, error) {
	records := bytes.Split(out, []byte{0})
	stats := make(map[string]diffStat)
	for i := 0; i < len(records); {
		if len(records[i]) == 0 {
			i++
			continue
		}
		fields := bytes.SplitN(records[i], []byte{'\t'}, 3)
		if len(fields) != 3 {
			return nil, fmt.Errorf("malformed numstat record")
		}
		stat := diffStat{}
		if string(fields[0]) == "-" || string(fields[1]) == "-" {
			stat.Binary = true
		} else {
			additions, err := strconv.Atoi(string(fields[0]))
			if err != nil {
				return nil, fmt.Errorf("malformed numstat additions %q: %w", fields[0], err)
			}
			deletions, err := strconv.Atoi(string(fields[1]))
			if err != nil {
				return nil, fmt.Errorf("malformed numstat deletions %q: %w", fields[1], err)
			}
			stat.Additions = additions
			stat.Deletions = deletions
		}
		path := string(fields[2])
		i++
		if path != "" {
			stats[diffPathKey("", path)] = stat
			continue
		}
		if i+1 >= len(records) || len(records[i]) == 0 || len(records[i+1]) == 0 {
			return nil, fmt.Errorf("malformed rename numstat record")
		}
		oldPath := string(records[i])
		newPath := string(records[i+1])
		i += 2
		stats[diffPathKey(oldPath, newPath)] = stat
	}
	return stats, nil
}

func diffPathKey(oldPath, path string) string {
	return oldPath + "\x00" + path
}

func statusLabel(code string) string {
	switch code {
	case "A":
		return "added"
	case "D":
		return "deleted"
	case "M":
		return "modified"
	case "R":
		return "renamed"
	case "C":
		return "copied"
	case "T":
		return "type changed"
	case "U":
		return "unmerged"
	default:
		return "changed"
	}
}

type cappedWriter struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	original := len(p)
	remaining := w.limit - w.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			_, _ = w.buf.Write(p[:remaining])
			w.truncated = true
		} else {
			_, _ = w.buf.Write(p)
		}
	} else if len(p) > 0 {
		w.truncated = true
	}
	return original, nil
}

func runLimited(ctx context.Context, repoPath string, limit int, args ...string) ([]byte, bool, error) {
	out := &cappedWriter{limit: limit}
	if err := runTo(ctx, repoPath, out, args...); err != nil {
		return nil, out.truncated, err
	}
	return out.buf.Bytes(), out.truncated, nil
}

var hunkHeaderRE = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(?:.*)$`)

func parseUnifiedPatch(out []byte) []DiffHunk {
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	var hunks []DiffHunk
	var current *DiffHunk
	oldLine, newLine := 0, 0
	for _, line := range lines {
		if match := hunkHeaderRE.FindStringSubmatch(line); match != nil {
			oldLine, _ = strconv.Atoi(match[1])
			newLine, _ = strconv.Atoi(match[3])
			hunks = append(hunks, DiffHunk{Header: line, OldStart: oldLine, NewStart: newLine})
			current = &hunks[len(hunks)-1]
			continue
		}
		if current == nil {
			continue
		}
		diffLine := DiffLine{}
		switch {
		case strings.HasPrefix(line, "+"):
			diffLine.Kind = "add"
			diffLine.NewLine = newLine
			diffLine.Text = strings.TrimPrefix(line, "+")
			newLine++
		case strings.HasPrefix(line, "-"):
			diffLine.Kind = "delete"
			diffLine.OldLine = oldLine
			diffLine.Text = strings.TrimPrefix(line, "-")
			oldLine++
		case strings.HasPrefix(line, "\\"):
			diffLine.Kind = "meta"
			diffLine.Text = line
		default:
			diffLine.Kind = "context"
			diffLine.OldLine = oldLine
			diffLine.NewLine = newLine
			if strings.HasPrefix(line, " ") {
				diffLine.Text = strings.TrimPrefix(line, " ")
			} else {
				diffLine.Text = line
			}
			oldLine++
			newLine++
		}
		current.Lines = append(current.Lines, diffLine)
	}
	return hunks
}

func patchForFile(ctx context.Context, repoPath string, detail CommitDetail, file ChangedFile, byteLimit int) ([]byte, bool, error) {
	paths := []string{}
	if file.OldPath != "" {
		paths = append(paths, file.OldPath)
	}
	if file.Path != "" && file.Path != file.OldPath {
		paths = append(paths, file.Path)
	}
	if len(paths) == 0 {
		return nil, false, nil
	}

	var args []string
	if len(detail.Parents) == 0 {
		args = []string{"show", "--format=", "--no-color", "--no-ext-diff", "--find-renames", "--unified=3", detail.Hash, "--"}
		args = append(args, paths...)
	} else {
		args = []string{"diff", "--no-color", "--no-ext-diff", "--find-renames", "--unified=3", detail.Parents[0], detail.Hash, "--"}
		args = append(args, paths...)
	}
	return runLimited(ctx, repoPath, byteLimit, args...)
}

func capDiffHunks(hunks []DiffHunk, lineLimit int) ([]DiffHunk, int, bool) {
	if lineLimit <= 0 {
		return nil, 0, len(hunks) > 0
	}
	remaining := lineLimit
	kept := make([]DiffHunk, 0, len(hunks))
	count := 0
	for _, hunk := range hunks {
		if remaining == 0 {
			return kept, count, true
		}
		copyHunk := hunk
		if len(copyHunk.Lines) > remaining {
			copyHunk.Lines = append([]DiffLine(nil), copyHunk.Lines[:remaining]...)
			kept = append(kept, copyHunk)
			count += remaining
			return kept, count, true
		}
		copyHunk.Lines = append([]DiffLine(nil), copyHunk.Lines...)
		kept = append(kept, copyHunk)
		remaining -= len(copyHunk.Lines)
		count += len(copyHunk.Lines)
	}
	return kept, count, false
}

func GetCommitDiff(ctx context.Context, repoPath string, detail CommitDetail) (CommitDiff, error) {
	changes, err := GetCommitChanges(ctx, repoPath, detail)
	if err != nil {
		return CommitDiff{}, err
	}
	result := CommitDiff{TotalFiles: len(changes)}
	for _, change := range changes {
		result.Additions += change.Additions
		result.Deletions += change.Deletions
	}
	if len(changes) > defaultDiffFileLimit {
		changes = changes[:defaultDiffFileLimit]
		result.Truncated = true
	}

	usedBytes := 0
	usedLines := 0
	for i, change := range changes {
		file := FileDiff{ChangedFile: change}
		if i >= defaultPatchFileLimit || usedBytes >= defaultPatchByteLimit || usedLines >= defaultPatchLineLimit {
			file.Truncated = true
			result.Truncated = true
			result.Files = append(result.Files, file)
			continue
		}
		if !change.Binary {
			remainingBytes := defaultPatchByteLimit - usedBytes
			byteLimit := min(perFilePatchByteLimit, remainingBytes)
			patch, byteTruncated, patchErr := patchForFile(ctx, repoPath, detail, change, byteLimit)
			if patchErr != nil {
				return CommitDiff{}, fmt.Errorf("read patch for %q: %w", change.Path, patchErr)
			}
			usedBytes += len(patch)

			remainingLines := defaultPatchLineLimit - usedLines
			lineLimit := min(perFilePatchLineLimit, remainingLines)
			hunks, renderedLines, lineTruncated := capDiffHunks(parseUnifiedPatch(patch), lineLimit)
			usedLines += renderedLines
			file.Hunks = hunks
			file.Truncated = byteTruncated || lineTruncated
			if file.Truncated || usedBytes >= defaultPatchByteLimit || usedLines >= defaultPatchLineLimit {
				result.Truncated = true
			}
		}
		result.Files = append(result.Files, file)
	}
	return result, nil
}

// ListFiles returns blob paths at a revision. Gitlinks/submodules are excluded
// so Go to File never routes a commit object through the blob viewer. The
// output is NUL-delimited so unusual filenames survive intact.
func ListFiles(ctx context.Context, repoPath, ref string) ([]string, error) {
	resolved, err := resolveRef(ctx, repoPath, ref)
	if err != nil {
		return nil, err
	}
	out, err := run(ctx, repoPath, "ls-tree", "-r", "-z", resolved.spec)
	if err != nil {
		return nil, fmt.Errorf("list repository files: %w", err)
	}
	records := bytes.Split(out, []byte{0})
	files := make([]string, 0, len(records))
	for _, record := range records {
		if len(record) == 0 {
			continue
		}
		tab := bytes.IndexByte(record, '\t')
		if tab < 0 {
			return nil, fmt.Errorf("malformed tree record")
		}
		meta := bytes.Fields(record[:tab])
		if len(meta) < 3 {
			return nil, fmt.Errorf("malformed tree metadata")
		}
		if string(meta[1]) != "blob" {
			continue
		}
		files = append(files, string(record[tab+1:]))
	}
	return files, nil
}

// StreamBlob writes one blob without loading it into memory. Callers should
// resolve size/existence before sending response headers so errors can still be
// reported cleanly.
func StreamBlob(ctx context.Context, repoPath, ref, path string, w io.Writer) error {
	spec, err := resolveBlobSpec(ctx, repoPath, ref, path)
	if err != nil {
		return err
	}
	if err := runTo(ctx, repoPath, w, "cat-file", "-p", spec); err != nil {
		return fmt.Errorf("stream blob: %w", err)
	}
	return nil
}
