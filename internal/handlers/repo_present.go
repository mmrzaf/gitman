package handlers

import (
	"bytes"
	"fmt"
	"html"
	"html/template"
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
	Number      int
	Highlighted template.HTML
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

func isTextBlob(data []byte) bool {
	return bytes.IndexByte(data, 0) == -1 && utf8.Valid(data)
}

type sourceLanguage struct {
	ID       string
	Label    string
	Comments []string
	Keywords map[string]struct{}
	Literals map[string]struct{}
}

func wordSet(words string) map[string]struct{} {
	set := make(map[string]struct{})
	for _, word := range strings.Fields(words) {
		set[word] = struct{}{}
	}
	return set
}

var languageByID = map[string]sourceLanguage{
	"go":         {ID: "go", Label: "Go", Comments: []string{"//"}, Keywords: wordSet("break default func interface select case defer go map struct chan else goto package switch const fallthrough if range type continue for import return var"), Literals: wordSet("true false nil iota")},
	"javascript": {ID: "javascript", Label: "JavaScript", Comments: []string{"//"}, Keywords: wordSet("async await break case catch class const continue debugger default delete do else export extends finally for from function get if import in instanceof let new of return set static super switch this throw try typeof var void while with yield"), Literals: wordSet("true false null undefined NaN Infinity")},
	"typescript": {ID: "typescript", Label: "TypeScript", Comments: []string{"//"}, Keywords: wordSet("abstract any as async await boolean break case catch class const constructor continue declare default delete do else enum export extends finally for from function get if implements import in infer instanceof interface keyof let module namespace never new of private protected public readonly return set static string super switch symbol this throw try type typeof undefined unique unknown var void while with yield"), Literals: wordSet("true false null undefined NaN Infinity")},
	"python":     {ID: "python", Label: "Python", Comments: []string{"#"}, Keywords: wordSet("and as assert async await break class continue def del elif else except finally for from global if import in is lambda nonlocal not or pass raise return try while with yield match case"), Literals: wordSet("True False None")},
	"shell":      {ID: "shell", Label: "Shell", Comments: []string{"#"}, Keywords: wordSet("case do done elif else esac fi for function if in select then time until while"), Literals: wordSet("true false")},
	"yaml":       {ID: "yaml", Label: "YAML", Comments: []string{"#"}, Keywords: map[string]struct{}{}, Literals: wordSet("true false null yes no on off")},
	"json":       {ID: "json", Label: "JSON", Keywords: map[string]struct{}{}, Literals: wordSet("true false null")},
	"sql":        {ID: "sql", Label: "SQL", Comments: []string{"--"}, Keywords: wordSet("select from where insert into update delete create table alter drop join left right inner outer on as and or not null values set group by order having limit offset union all distinct primary key foreign references index view begin commit rollback case when then else end"), Literals: wordSet("true false null")},
	"html":       {ID: "html", Label: "HTML", Keywords: map[string]struct{}{}, Literals: map[string]struct{}{}},
	"css":        {ID: "css", Label: "CSS", Keywords: map[string]struct{}{}, Literals: map[string]struct{}{}},
	"markdown":   {ID: "markdown", Label: "Markdown", Keywords: map[string]struct{}{}, Literals: map[string]struct{}{}},
	"toml":       {ID: "toml", Label: "TOML", Comments: []string{"#"}, Keywords: map[string]struct{}{}, Literals: wordSet("true false")},
	"rust":       {ID: "rust", Label: "Rust", Comments: []string{"//"}, Keywords: wordSet("as async await break const continue crate dyn else enum extern false fn for if impl in let loop match mod move mut pub ref return self Self static struct super trait true type unsafe use where while"), Literals: wordSet("true false None Some Ok Err")},
	"java":       {ID: "java", Label: "Java", Comments: []string{"//"}, Keywords: wordSet("abstract assert boolean break byte case catch char class const continue default do double else enum extends final finally float for goto if implements import instanceof int interface long native new package private protected public return short static strictfp super switch synchronized this throw throws transient try void volatile while"), Literals: wordSet("true false null")},
	"c":          {ID: "c", Label: "C/C++", Comments: []string{"//"}, Keywords: wordSet("auto break case char const continue default do double else enum extern float for goto if inline int long register restrict return short signed sizeof static struct switch typedef union unsigned void volatile while class namespace public private protected template typename using virtual override constexpr nullptr"), Literals: wordSet("true false NULL nullptr")},
}

func detectSourceLanguage(path string) sourceLanguage {
	base := strings.ToLower(filepath.Base(path))
	ext := strings.ToLower(filepath.Ext(base))
	id := ""
	switch ext {
	case ".go":
		id = "go"
	case ".js", ".mjs", ".cjs", ".jsx":
		id = "javascript"
	case ".ts", ".tsx", ".mts", ".cts":
		id = "typescript"
	case ".py":
		id = "python"
	case ".sh", ".bash", ".zsh":
		id = "shell"
	case ".yaml", ".yml":
		id = "yaml"
	case ".json", ".jsonc":
		id = "json"
	case ".sql":
		id = "sql"
	case ".html", ".htm", ".tmpl":
		id = "html"
	case ".css", ".scss":
		id = "css"
	case ".md", ".markdown":
		id = "markdown"
	case ".toml":
		id = "toml"
	case ".rs":
		id = "rust"
	case ".java":
		id = "java"
	case ".c", ".h", ".cc", ".cpp", ".cxx", ".hpp":
		id = "c"
	}
	if id == "" {
		switch base {
		case "dockerfile", "makefile", "justfile":
			id = "shell"
		case "go.mod", "go.sum":
			return sourceLanguage{ID: "text", Label: "Go module", Keywords: map[string]struct{}{}, Literals: map[string]struct{}{}}
		}
	}
	if language, ok := languageByID[id]; ok {
		return language
	}
	return sourceLanguage{ID: "text", Label: "Text", Keywords: map[string]struct{}{}, Literals: map[string]struct{}{}}
}

func sourceLines(data []byte, language sourceLanguage) []SourceLine {
	if len(data) == 0 {
		return nil
	}
	text := string(data)
	parts := strings.Split(text, "\n")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	lines := make([]SourceLine, 0, len(parts))
	for i, line := range parts {
		line = strings.TrimSuffix(line, "\r")
		lines = append(lines, SourceLine{Number: i + 1, Highlighted: highlightSourceLine(line, language)})
	}
	return lines
}

func highlightSourceLine(line string, language sourceLanguage) template.HTML {
	if line == "" {
		return ""
	}
	trimmed := strings.TrimSpace(line)
	if language.ID == "markdown" && strings.HasPrefix(trimmed, "#") {
		return template.HTML(`<span class="syn-keyword">` + html.EscapeString(line) + `</span>`)
	}
	var out strings.Builder
	for i := 0; i < len(line); {
		if line[i] >= utf8.RuneSelf {
			_, size := utf8.DecodeRuneInString(line[i:])
			out.WriteString(html.EscapeString(line[i : i+size]))
			i += size
			continue
		}
		commented := false
		for _, prefix := range language.Comments {
			if strings.HasPrefix(line[i:], prefix) {
				out.WriteString(`<span class="syn-comment">`)
				out.WriteString(html.EscapeString(line[i:]))
				out.WriteString(`</span>`)
				i = len(line)
				commented = true
				break
			}
		}
		if commented {
			continue
		}
		ch := line[i]
		if ch == '"' || ch == '\'' || ch == '`' {
			quote := ch
			j := i + 1
			escaped := false
			for j < len(line) {
				if escaped {
					escaped = false
					j++
					continue
				}
				if line[j] == '\\' && quote != '`' {
					escaped = true
					j++
					continue
				}
				j++
				if line[j-1] == quote {
					break
				}
			}
			out.WriteString(`<span class="syn-string">`)
			out.WriteString(html.EscapeString(line[i:j]))
			out.WriteString(`</span>`)
			i = j
			continue
		}
		if isASCIIDigit(ch) {
			j := i + 1
			for j < len(line) && (isASCIIDigit(line[j]) || strings.ContainsRune("._xXabcdefABCDEF", rune(line[j]))) {
				j++
			}
			out.WriteString(`<span class="syn-number">`)
			out.WriteString(html.EscapeString(line[i:j]))
			out.WriteString(`</span>`)
			i = j
			continue
		}
		if isASCIIIdentStart(ch) {
			j := i + 1
			for j < len(line) && isASCIIIdentPart(line[j]) {
				j++
			}
			word := line[i:j]
			lower := strings.ToLower(word)
			_, keyword := language.Keywords[word]
			if !keyword && language.ID == "sql" {
				_, keyword = language.Keywords[lower]
			}
			_, literal := language.Literals[word]
			if !literal {
				_, literal = language.Literals[lower]
			}
			class := ""
			switch {
			case keyword:
				class = "syn-keyword"
			case literal:
				class = "syn-literal"
			case language.ID == "yaml" || language.ID == "toml":
				k := j
				for k < len(line) && (line[k] == ' ' || line[k] == '\t') {
					k++
				}
				if k < len(line) && (line[k] == ':' || line[k] == '=') {
					class = "syn-keyword"
				}
			}
			if class != "" {
				out.WriteString(`<span class="` + class + `">`)
				out.WriteString(html.EscapeString(word))
				out.WriteString(`</span>`)
			} else {
				out.WriteString(html.EscapeString(word))
			}
			i = j
			continue
		}
		out.WriteString(html.EscapeString(string(ch)))
		i++
	}
	return template.HTML(out.String())
}

func isASCIIDigit(ch byte) bool { return ch >= '0' && ch <= '9' }
func isASCIIIdentStart(ch byte) bool {
	return ch == '_' || ch == '$' || (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z')
}
func isASCIIIdentPart(ch byte) bool { return isASCIIIdentStart(ch) || isASCIIDigit(ch) }

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
		depthPenalty := strings.Count(path, "/") * 3
		return 100 - depthPenalty
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
