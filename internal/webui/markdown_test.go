package webui

import (
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/yuin/goldmark/parser"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

func TestRenderMarkdownFormats(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		src  string
		want string
	}{
		"heading":       {src: "# Title", want: "<h1>Title</h1>"},
		"emphasis":      {src: "**bold** and *italic*", want: "<p><strong>bold</strong> and <em>italic</em></p>"},
		"list":          {src: "- one\n- two", want: "<ul>\n<li>one</li>\n<li>two</li>\n</ul>"},
		"code block":    {src: "```go\nx := 1\n```", want: `<pre><code class="language-go">x := 1`},
		"table":         {src: "| a | b |\n|---|---|\n| 1 | 2 |", want: "<table>"},
		"strikethrough": {src: "~~gone~~", want: "<del>gone</del>"},
		"task list":     {src: "- [x] done", want: `<input checked="" disabled="" type="checkbox"`},
		"link":          {src: "[docs](https://example.com)", want: `<a href="https://example.com" rel="noopener noreferrer nofollow">docs</a>`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Contains(t, string(renderMarkdown(tc.src)), tc.want)
		})
	}
}

func TestRenderMarkdownIsInert(t *testing.T) {
	t.Parallel()
	for name, src := range map[string]string{
		"script block":       "<script>alert(1)</script>",
		"inline html":        "hi <img src=x onerror=alert(1)> there",
		"javascript link":    "[x](javascript:alert(1))",
		"vbscript link":      "[x](vbscript:msgbox(1))",
		"data link":          "[x](data:text/html;base64,PHNjcmlwdD4=)",
		"javascript image":   "![x](javascript:alert(1))",
		"html block":         "<div onclick=\"alert(1)\">x</div>",
		"iframe":             "<iframe src=\"https://example.com\"></iframe>",
		"autolink js":        "<javascript:alert(1)>",
		"attribute breakout": "[x](https://example.com\" onclick=\"alert(1))",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			out := strings.ToLower(string(renderMarkdown(src)))
			assert.NotRegexp(t, `<(script|iframe|div)\b`, out)
			assert.NotContains(t, out, "<img src=x")
			assert.NotRegexp(t, `<[^>]*\son\w+=`, out)
			assert.NotRegexp(t, `(href|src)="\s*(javascript|vbscript|data):`, out)
		})
	}
}

func TestRawHTMLParsersAreRemoved(t *testing.T) {
	t.Parallel()
	blocks, inlines := parser.DefaultBlockParsers(), parser.DefaultInlineParsers()

	assert.Len(t, without(blocks, parser.NewHTMLBlockParser()), len(blocks)-1, "goldmark renamed or dropped its HTML block parser")
	assert.Len(t, without(inlines, parser.NewRawHTMLParser()), len(inlines)-1, "goldmark renamed or dropped its raw HTML parser")
}

func TestEditorUsesTheMarkdownToolbarElement(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := send(f.h, http.MethodGet, f.path(""), "", withCookie(f.c))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `<markdown-toolbar for="memory-content"`)
	for _, tag := range []string{"md-bold", "md-italic", "md-header", "md-code", "md-link", "md-unordered-list", "md-ordered-list", "md-task-list", "md-quote"} {
		assert.Contains(t, body, "<"+tag+" ", tag)
	}
	script, err := os.ReadFile("static/app.js")
	require.NoError(t, err)
	assert.Contains(t, string(script), "{ b: 'md-bold', i: 'md-italic' }", "Ctrl B and Ctrl I press the toolbar's buttons")
}

func TestRenderMarkdownShowsRawHTMLAsText(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		src  string
		want string
	}{
		"placeholder":  {src: "run memorable ui --context <name> now", want: "--context &lt;name&gt; now"},
		"generic type": {src: "returns Vec<String>", want: "Vec&lt;String&gt;"},
		"html block":   {src: "<div onclick=\"alert(1)\">x</div>", want: "&lt;div onclick=&quot;alert(1)&quot;&gt;x&lt;/div&gt;"},
		"autolink":     {src: "<https://example.com>", want: `<a href="https://example.com" rel="noopener noreferrer nofollow">https://example.com</a>`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Contains(t, string(renderMarkdown(tc.src)), tc.want)
		})
	}
}

func TestDetailRendersMarkdownPreview(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, func(m *detailMemories, _ uuid.UUID) {
		m.fetched[0].Memory.Content = "## Ports\n\n- **api** 8080"
	})

	rec := send(f.h, http.MethodGet, f.path(""), "", withCookie(f.c))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	preview := regexp.MustCompile(`(?s)<div id="memory-preview"[^>]*>.*?</div>`).FindString(body)
	require.NotEmpty(t, preview, body)
	assert.Contains(t, preview, "<h2>Ports</h2>")
	assert.Contains(t, preview, "<strong>api</strong>")
	assert.Regexp(t, `<div id="memory-preview"[^>]*\shidden[\s>]`, preview, "an editable memory opens on Write")
	assert.Contains(t, body, `hx-post="`+f.path("/preview")+`"`)
	assert.Contains(t, body, "data-markdown-toolbar")
}

func TestDeletedMemoryOpensOnRenderedMarkdown(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, func(m *detailMemories, _ uuid.UUID) {
		m.fetched[0].Memory.Status = memory.StatusDeleted
		m.fetched[0].Memory.Content = "**kept**"
	})

	rec := send(f.h, http.MethodGet, f.path(""), "", withCookie(f.c))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	preview := regexp.MustCompile(`<div id="memory-preview"[^>]*>`).FindString(body)
	require.NotEmpty(t, preview)
	assert.NotRegexp(t, `\shidden[\s>]`, preview)
	assert.Contains(t, body, "<strong>kept</strong>")
	assert.NotContains(t, body, "data-markdown-toolbar", "no toolbar without an editor")
}

func TestPreviewRendersSubmittedContent(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := f.post("/preview", url.Values{"content": {"# Draft\n\n<script>alert(1)</script>"}}, withHeader("HX-Request", "true"))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "<h1>Draft</h1>")
	assert.NotContains(t, rec.Body.String(), "<script")
	assert.Empty(t, f.mem.calls, "a preview never writes or reads history")
}

func TestPreviewOfEmptyContent(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := f.post("/preview", url.Values{"content": {"  \n"}}, withHeader("HX-Request", "true"))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Nothing to preview.")
}

func TestPreviewRequiresCSRF(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := send(f.h, http.MethodPost, f.path("/preview"), "csrf=wrong&content=x", withCookie(f.c),
		withHeader("Content-Type", "application/x-www-form-urlencoded"))

	assert.Equal(t, http.StatusForbidden, rec.Code)
}
