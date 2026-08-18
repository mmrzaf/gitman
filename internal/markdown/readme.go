package markdown

import (
	"html"
	"html/template"
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode"
)

// Options controls safe README link rewriting. Repository-controlled input is
// always escaped before Gitman emits the small, fixed HTML vocabulary below.
type Options struct {
	Owner      string
	Repository string
	Ref        string
	ReadmePath string
}

var orderedItem = regexp.MustCompile(`^\s*([0-9]+)\.\s+(.+)$`)

// Render renders a deliberately compact Markdown subset suitable for repository
// landing pages. It supports headings, paragraphs, fenced code, blockquotes,
// ordered/unordered lists, horizontal rules, inline code, emphasis, strong text,
// and links. Raw HTML is never interpreted.
func Render(input string, opts Options) template.HTML {
	input = strings.ReplaceAll(input, "\r\n", "\n")
	input = strings.ReplaceAll(input, "\r", "\n")
	lines := strings.Split(input, "\n")

	var out strings.Builder
	var paragraph []string
	var listKind string
	var inQuote bool
	var inCode bool
	var codeFence string
	var codeLanguage string
	var codeLines []string

	flushParagraph := func() {
		if len(paragraph) == 0 {
			return
		}
		text := strings.TrimSpace(strings.Join(paragraph, " "))
		if text != "" {
			out.WriteString("<p>")
			out.WriteString(renderInline(text, opts))
			out.WriteString("</p>\n")
		}
		paragraph = nil
	}
	closeList := func() {
		if listKind == "" {
			return
		}
		out.WriteString("</" + listKind + ">\n")
		listKind = ""
	}
	closeQuote := func() {
		if !inQuote {
			return
		}
		flushParagraph()
		closeList()
		out.WriteString("</blockquote>\n")
		inQuote = false
	}
	flushCode := func() {
		if !inCode {
			return
		}
		out.WriteString(`<pre class="readme-code"><code`)
		if codeLanguage != "" {
			out.WriteString(` class="language-` + html.EscapeString(codeLanguage) + `"`)
		}
		out.WriteString(">")
		out.WriteString(html.EscapeString(strings.Join(codeLines, "\n")))
		out.WriteString("</code></pre>\n")
		inCode = false
		codeFence = ""
		codeLanguage = ""
		codeLines = nil
	}

	for _, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if inCode {
			if strings.HasPrefix(trimmed, codeFence) {
				flushCode()
			} else {
				codeLines = append(codeLines, raw)
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			flushParagraph()
			closeList()
			closeQuote()
			codeFence = trimmed[:3]
			codeLanguage = sanitizeLanguage(strings.TrimSpace(strings.TrimPrefix(trimmed, codeFence)))
			inCode = true
			continue
		}
		if trimmed == "" {
			flushParagraph()
			closeList()
			closeQuote()
			continue
		}
		if trimmed == "---" || trimmed == "***" || trimmed == "___" {
			flushParagraph()
			closeList()
			closeQuote()
			out.WriteString("<hr>\n")
			continue
		}
		if strings.HasPrefix(trimmed, ">") {
			flushParagraph()
			closeList()
			if !inQuote {
				out.WriteString("<blockquote>\n")
				inQuote = true
			}
			quoteText := strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))
			if quoteText != "" {
				out.WriteString("<p>")
				out.WriteString(renderInline(quoteText, opts))
				out.WriteString("</p>\n")
			}
			continue
		}
		closeQuote()
		if level, text, ok := heading(trimmed); ok {
			flushParagraph()
			closeList()
			id := headingID(text)
			out.WriteString("<h")
			out.WriteByte(byte('0' + level))
			if id != "" {
				out.WriteString(` id="` + html.EscapeString(id) + `"`)
			}
			out.WriteString(">")
			out.WriteString(renderInline(text, opts))
			if id != "" {
				out.WriteString(` <a class="readme-heading-anchor" href="#` + html.EscapeString(id) + `" aria-label="Link to this section">#</a>`)
			}
			out.WriteString("</h")
			out.WriteByte(byte('0' + level))
			out.WriteString(">\n")
			continue
		}
		if text, ok := unordered(trimmed); ok {
			flushParagraph()
			if listKind != "ul" {
				closeList()
				out.WriteString("<ul>\n")
				listKind = "ul"
			}
			out.WriteString("<li>")
			out.WriteString(renderInline(text, opts))
			out.WriteString("</li>\n")
			continue
		}
		if match := orderedItem.FindStringSubmatch(raw); len(match) == 3 {
			flushParagraph()
			if listKind != "ol" {
				closeList()
				out.WriteString("<ol>\n")
				listKind = "ol"
			}
			out.WriteString("<li>")
			out.WriteString(renderInline(strings.TrimSpace(match[2]), opts))
			out.WriteString("</li>\n")
			continue
		}
		closeList()
		paragraph = append(paragraph, strings.TrimSpace(raw))
	}

	if inCode {
		flushCode()
	}
	flushParagraph()
	closeList()
	closeQuote()
	return template.HTML(out.String())
}

func heading(line string) (int, string, bool) {
	count := 0
	for count < len(line) && count < 6 && line[count] == '#' {
		count++
	}
	if count == 0 || count >= len(line) || line[count] != ' ' {
		return 0, "", false
	}
	text := strings.TrimSpace(line[count:])
	return count, strings.TrimSpace(strings.TrimSuffix(text, strings.Repeat("#", count))), text != ""
}

func unordered(line string) (string, bool) {
	if len(line) < 2 {
		return "", false
	}
	if (line[0] == '-' || line[0] == '*' || line[0] == '+') && unicode.IsSpace(rune(line[1])) {
		return strings.TrimSpace(line[2:]), true
	}
	return "", false
}

func renderInline(text string, opts Options) string {
	var out strings.Builder
	for i := 0; i < len(text); {
		if text[i] == '`' {
			if end := strings.IndexByte(text[i+1:], '`'); end >= 0 {
				end += i + 1
				out.WriteString("<code>")
				out.WriteString(html.EscapeString(text[i+1 : end]))
				out.WriteString("</code>")
				i = end + 1
				continue
			}
		}
		if strings.HasPrefix(text[i:], "**") || strings.HasPrefix(text[i:], "__") {
			marker := text[i : i+2]
			if end := strings.Index(text[i+2:], marker); end >= 0 {
				end += i + 2
				out.WriteString("<strong>")
				out.WriteString(renderInline(text[i+2:end], opts))
				out.WriteString("</strong>")
				i = end + 2
				continue
			}
		}
		if text[i] == '*' || text[i] == '_' {
			marker := text[i]
			if end := strings.IndexByte(text[i+1:], marker); end >= 0 {
				end += i + 1
				out.WriteString("<em>")
				out.WriteString(renderInline(text[i+1:end], opts))
				out.WriteString("</em>")
				i = end + 1
				continue
			}
		}
		if text[i] == '[' {
			closeLabel := strings.IndexByte(text[i+1:], ']')
			if closeLabel >= 0 {
				closeLabel += i + 1
				if closeLabel+1 < len(text) && text[closeLabel+1] == '(' {
					closeURL := strings.IndexByte(text[closeLabel+2:], ')')
					if closeURL >= 0 {
						closeURL += closeLabel + 2
						label := text[i+1 : closeLabel]
						href := rewriteURL(strings.TrimSpace(text[closeLabel+2:closeURL]), opts)
						if href != "" {
							out.WriteString(`<a href="` + html.EscapeString(href) + `"`)
							if isExternal(href) {
								out.WriteString(` rel="nofollow noopener noreferrer"`)
							}
							out.WriteString(">")
							out.WriteString(renderInline(label, opts))
							out.WriteString("</a>")
							i = closeURL + 1
							continue
						}
					}
				}
			}
		}
		start := i
		for i < len(text) && !strings.ContainsRune("`*_[]", rune(text[i])) {
			i++
		}
		if start == i {
			i++
		}
		out.WriteString(html.EscapeString(text[start:i]))
	}
	return out.String()
}

func rewriteURL(raw string, opts Options) string {
	if raw == "" || strings.ContainsAny(raw, "\x00\r\n") {
		return ""
	}
	if strings.HasPrefix(raw, "#") {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if parsed.IsAbs() {
		switch strings.ToLower(parsed.Scheme) {
		case "http", "https", "mailto":
			return parsed.String()
		default:
			return ""
		}
	}
	if strings.HasPrefix(raw, "//") {
		return ""
	}
	repoPath := parsed.Path
	if strings.HasPrefix(repoPath, "/") {
		repoPath = strings.TrimPrefix(repoPath, "/")
	} else {
		repoPath = path.Clean(path.Join(path.Dir(opts.ReadmePath), repoPath))
	}
	if repoPath == "." {
		repoPath = ""
	}
	if strings.HasPrefix(repoPath, "../") || repoPath == ".." {
		return ""
	}
	route := "blob"
	if strings.HasSuffix(parsed.Path, "/") || repoPath == "" {
		route = "tree"
	}
	values := url.Values{}
	values.Set("ref", opts.Ref)
	if repoPath != "" {
		values.Set("path", repoPath)
	}
	href := "/" + url.PathEscape(opts.Owner) + "/" + url.PathEscape(opts.Repository) + "/" + route + "?" + values.Encode()
	if parsed.Fragment != "" {
		href += "#" + url.PathEscape(parsed.Fragment)
	}
	return href
}

func isExternal(href string) bool {
	return strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") || strings.HasPrefix(href, "mailto:")
}

func headingID(text string) string {
	text = strings.ToLower(strings.TrimSpace(stripInlineMarkers(text)))
	var out strings.Builder
	dash := false
	for _, r := range text {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			out.WriteRune(r)
			dash = false
		case unicode.IsSpace(r) || r == '-' || r == '_':
			if out.Len() > 0 && !dash {
				out.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.Trim(out.String(), "-")
}

func stripInlineMarkers(text string) string {
	replacer := strings.NewReplacer("**", "", "__", "", "*", "", "_", "", "`", "")
	return replacer.Replace(text)
}

func sanitizeLanguage(value string) string {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}
	value = strings.ToLower(fields[0])
	var out strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '+' {
			out.WriteRune(r)
		}
	}
	return out.String()
}
