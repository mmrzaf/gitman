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

func rankFileMatches(files []string, query, owner, repo, ref string, limit int) []fileSearchMatch {
	query = strings.TrimSpace(query)
	if limit <= 0 {
		limit = 40
	}
	matches := make([]fileSearchMatch, 0, minInt(limit*3, len(files)))
	for _, path := range files {
		score := fileSearchScore(path, query)
		if score < 0 {
			continue
		}
		matches = append(matches, fileSearchMatch{
			Path:  path,
			URL:   fmt.Sprintf("/%s/%s/blob?ref=%s&path=%s", owner, repo, url.QueryEscape(ref), url.QueryEscape(path)),
			score: score,
		})
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score == matches[j].score {
			if len(matches[i].Path) == len(matches[j].Path) {
				return matches[i].Path < matches[j].Path
			}
			return len(matches[i].Path) < len(matches[j].Path)
		}
		return matches[i].score > matches[j].score
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
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
