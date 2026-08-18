package markdown

import (
	"strings"
	"testing"
)

func TestRenderEscapesRawHTMLAndDangerousLinks(t *testing.T) {
	got := string(Render("# Hello <script>alert(1)</script>\n\n[x](javascript:alert(1))\n\n`<img src=x>`", Options{}))
	if strings.Contains(got, "<script>") || strings.Contains(got, `href="javascript:`) || strings.Contains(got, "<img src=x>") {
		t.Fatalf("unsafe README output: %s", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") || !strings.Contains(got, "&lt;img src=x&gt;") {
		t.Fatalf("expected escaped repository content: %s", got)
	}
}

func TestRenderRepositoryRelativeLinks(t *testing.T) {
	got := string(Render("[docs](docs/guide.md) [site](https://example.com)", Options{Owner: "alice", Repository: "demo", Ref: "main", ReadmePath: "README.md"}))
	if !strings.Contains(got, `/alice/demo/blob?path=docs%2Fguide.md&amp;ref=main`) {
		t.Fatalf("relative link not rewritten: %s", got)
	}
	if !strings.Contains(got, `href="https://example.com"`) || !strings.Contains(got, `noopener`) {
		t.Fatalf("external link not preserved safely: %s", got)
	}
}

func TestRenderBasicBlocks(t *testing.T) {
	got := string(Render("## Install\n\n- one\n- two\n\n```go\nfmt.Println(\"ok\")\n```", Options{}))
	for _, want := range []string{`<h2 id="install">`, `<ul>`, `<li>one</li>`, `class="language-go"`, `fmt.Println(&#34;ok&#34;)`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %s", want, got)
		}
	}
}
