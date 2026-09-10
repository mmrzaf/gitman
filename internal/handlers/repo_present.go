package handlers

import (
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/models"
)

type RepoBreadcrumb struct {
	Name string
	Path string
}

type SourceLine struct {
	Number int
	Text   string
}

type CommitListItem struct {
	Commit git.Commit
	CI     *models.CIRun
}

func classifyRepoRef(ref string, branches, tags []string) string {
	for _, branch := range branches {
		if branch == ref {
			return "branch"
		}
	}
	for _, tag := range tags {
		if tag == ref {
			return "tag"
		}
	}
	if ref != "" {
		return "commit"
	}
	return ""
}

func repoRefKindLabel(kind git.RefKind) string {
	switch kind {
	case git.RefKindBranch:
		return "branch"
	case git.RefKindTag:
		return "tag"
	case git.RefKindCommit, git.RefKindHEAD:
		return "commit"
	default:
		return ""
	}
}

func repoBreadcrumbs(path string) []RepoBreadcrumb {
	path = strings.Trim(path, "/")
	if path == "" {
		return nil
	}
	parts := strings.Split(path, "/")
	crumbs := make([]RepoBreadcrumb, 0, len(parts))
	for i, part := range parts {
		crumbs = append(crumbs, RepoBreadcrumb{
			Name: part,
			Path: strings.Join(parts[:i+1], "/"),
		})
	}
	return crumbs
}

const (
	maxSourceRenderBytes = 2 * 1024 * 1024
	maxSourceRenderLines = 20_000
	maxSourceLineBytes   = 256 * 1024
)

func sourceRenderLimitNote(data []byte) string {
	if len(data) > maxSourceRenderBytes {
		return "Files larger than 2 MiB are available through Raw or Download."
	}
	lineCount := 1
	lineBytes := 0
	for i, b := range data {
		if b == '\n' {
			lineBytes = 0
			if i+1 < len(data) {
				lineCount++
			}
			if lineCount > maxSourceRenderLines {
				return "This file has too many lines for the interactive source viewer. Raw and Download remain available."
			}
			continue
		}
		lineBytes++
		if lineBytes > maxSourceLineBytes {
			return "This file contains an exceptionally long line that is unsafe to render interactively. Raw and Download remain available."
		}
	}
	return ""
}

func isTextBlob(data []byte) bool {
	return !strings.ContainsRune(string(data), '\x00') && utf8.Valid(data)
}

// sourceLanguageLabel is presentation metadata only. Gitman deliberately does
// not parse or color source languages; the browser shows the exact source text
// and lets Git remain the authority on repository content.
func sourceLanguageLabel(path string) string {
	base := strings.ToLower(filepath.Base(path))
	ext := strings.ToLower(filepath.Ext(base))
	labels := map[string]string{
		".go": "Go", ".js": "JavaScript", ".mjs": "JavaScript", ".cjs": "JavaScript", ".jsx": "JavaScript",
		".ts": "TypeScript", ".tsx": "TypeScript", ".mts": "TypeScript", ".cts": "TypeScript",
		".py": "Python", ".sh": "Shell", ".bash": "Shell", ".zsh": "Shell",
		".yaml": "YAML", ".yml": "YAML", ".json": "JSON", ".jsonc": "JSON", ".sql": "SQL",
		".html": "HTML", ".htm": "HTML", ".tmpl": "Template", ".css": "CSS", ".scss": "SCSS",
		".md": "Markdown", ".markdown": "Markdown", ".toml": "TOML", ".rs": "Rust", ".java": "Java",
		".c": "C", ".h": "C header", ".cc": "C++", ".cpp": "C++", ".cxx": "C++", ".hpp": "C++ header",
	}
	if label := labels[ext]; label != "" {
		return label
	}
	switch base {
	case "dockerfile":
		return "Dockerfile"
	case "makefile":
		return "Makefile"
	case "justfile":
		return "Justfile"
	case "go.mod", "go.sum":
		return "Go module"
	default:
		return "Text"
	}
}

func sourceLines(data []byte) []SourceLine {
	if len(data) == 0 {
		return nil
	}
	parts := strings.Split(string(data), "\n")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	lines := make([]SourceLine, 0, len(parts))
	for i, line := range parts {
		lines = append(lines, SourceLine{Number: i + 1, Text: strings.TrimSuffix(line, "\r")})
	}
	return lines
}

type fileSearchMatch struct {
	Path  string `json:"path"`
	URL   string `json:"url"`
	score int
}

func fileSearchMatchForPath(path, query string) (fileSearchMatch, bool) {
	score := fileSearchScore(path, query)
	if score < 0 {
		return fileSearchMatch{}, false
	}
	return fileSearchMatch{Path: path, score: score}, true
}

func finalizeFileSearchMatches(matches []fileSearchMatch, owner, repo, ref string) {
	for i := range matches {
		matches[i].URL = fmt.Sprintf("/%s/%s/blob?ref=%s&path=%s", owner, repo, url.QueryEscape(ref), url.QueryEscape(matches[i].Path))
	}
}

func betterFileSearchMatch(a, b fileSearchMatch) bool {
	if a.score == b.score {
		if len(a.Path) == len(b.Path) {
			return a.Path < b.Path
		}
		return len(a.Path) < len(b.Path)
	}
	return a.score > b.score
}

func addFileSearchMatch(matches []fileSearchMatch, match fileSearchMatch, limit int) []fileSearchMatch {
	if limit <= 0 {
		limit = 40
	}
	insertAt := sort.Search(len(matches), func(i int) bool {
		return betterFileSearchMatch(match, matches[i])
	})
	if insertAt >= limit {
		return matches
	}
	if len(matches) < limit {
		matches = append(matches, fileSearchMatch{})
	}
	copy(matches[insertAt+1:], matches[insertAt:len(matches)-1])
	matches[insertAt] = match
	return matches
}

func rankFileMatches(files []string, query, owner, repo, ref string, limit int) []fileSearchMatch {
	query = strings.TrimSpace(query)
	if limit <= 0 {
		limit = 40
	}
	matches := make([]fileSearchMatch, 0, minInt(limit, len(files)))
	for _, path := range files {
		match, ok := fileSearchMatchForPath(path, query)
		if !ok {
			continue
		}
		matches = addFileSearchMatch(matches, match, limit)
	}
	finalizeFileSearchMatches(matches, owner, repo, ref)
	return matches
}

func fileSearchScore(path, query string) int {
	if query == "" {
		return 100 - strings.Count(path, "/")*3
	}
	p := strings.ToLower(path)
	q := strings.ToLower(query)
	base := strings.ToLower(filepath.Base(path))
	switch {
	case p == q:
		return 1000
	case base == q:
		return 950
	case strings.HasPrefix(base, q):
		return 850 - len(base) + len(q)
	case strings.HasPrefix(p, q):
		return 800 - strings.Count(path, "/")*3
	case strings.Contains(base, q):
		return 700 - strings.Index(base, q)
	case strings.Contains(p, q):
		return 600 - strings.Index(p, q)/2
	}
	if score := subsequenceScore(p, q); score >= 0 {
		return 300 + score
	}
	return -1
}

func subsequenceScore(value, query string) int {
	if query == "" {
		return 0
	}
	qi := 0
	score := 0
	last := -2
	for i := 0; i < len(value) && qi < len(query); i++ {
		if value[i] != query[qi] {
			continue
		}
		if i == last+1 {
			score += 8
		} else {
			score += 2
		}
		if i == 0 || value[i-1] == '/' || value[i-1] == '_' || value[i-1] == '-' || value[i-1] == '.' {
			score += 10
		}
		last = i
		qi++
	}
	if qi != len(query) {
		return -1
	}
	return score - len(value)/8
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
