package webui

import (
	"bytes"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedAssets(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"static/app.css", "static/htmx.min.js", "static/app.js", "static/OFL.txt",
		"static/atkinson-hyperlegible-regular.woff2", "static/atkinson-hyperlegible-bold.woff2",
	} {
		data, err := assetFS.ReadFile(name)
		require.NoError(t, err, name)
		assert.NotEmpty(t, data, name)
	}

	tmpl, err := parseTemplates()
	require.NoError(t, err)

	var out bytes.Buffer
	err = tmpl.ExecuteTemplate(&out, "layout.html", pageData{
		Title:       "<script>x</script>",
		Context:     "personal",
		Contexts:    []string{"personal", "work"},
		CSRF:        "token-123",
		ReviewCount: 3,
	})
	require.NoError(t, err)

	body := out.String()
	assert.NotContains(t, body, "<script>x</script>")
	assert.Contains(t, body, "&lt;script&gt;x&lt;/script&gt;")
	assert.Contains(t, body, `name="htmx-config"`)
	assert.Contains(t, body, `name="csrf-token" content="token-123"`)
	assert.Contains(t, body, "Review (3)")
}

func TestErrorTemplateEscapesMessage(t *testing.T) {
	t.Parallel()

	tmpl, err := parseTemplates()
	require.NoError(t, err)

	var out bytes.Buffer
	err = tmpl.ExecuteTemplate(&out, "error.html", pageData{Title: "Not found", Message: "<b>nope</b>"})
	require.NoError(t, err)
	assert.NotContains(t, out.String(), "<b>nope</b>")
}

func TestStaticFontContentType(t *testing.T) {
	t.Parallel()

	h, c := newMemoriesServer(t, &recordingMemories{})
	rec := getPage(h, c, "/static/atkinson-hyperlegible-regular.woff2")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "font/woff2", rec.Header().Get("Content-Type"))
}

func TestTemplatesHaveNoInlineStyleOrScript(t *testing.T) {
	t.Parallel()

	inlineStyle := regexp.MustCompile(`(?i)\sstyle\s*=`)
	inlineHandler := regexp.MustCompile(`(?i)\shx-on`)
	scriptTag := regexp.MustCompile(`(?i)<script[^>]*>`)
	scriptSrc := regexp.MustCompile(`(?i)\ssrc\s*=`)

	files, err := fs.Glob(assetFS, "templates/*.html")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, file := range files {
		data, err := assetFS.ReadFile(file)
		require.NoError(t, err, file)
		name := path.Base(file)
		assert.False(t, inlineStyle.Match(data), "%s has an inline style attribute", name)
		assert.False(t, inlineHandler.Match(data), "%s has an hx-on attribute", name)
		for _, tag := range scriptTag.FindAll(data, -1) {
			assert.True(t, scriptSrc.Match(tag), "%s has a script tag without src: %s", name, tag)
		}
	}
}

func TestLayoutHasThemeToggle(t *testing.T) {
	t.Parallel()

	tmpl, err := parseTemplates()
	require.NoError(t, err)

	var out bytes.Buffer
	err = tmpl.ExecuteTemplate(&out, "layout.html", pageData{Title: "Memories", Context: "personal", Contexts: []string{"personal"}})
	require.NoError(t, err)

	body := out.String()
	assert.Contains(t, body, `<html lang="en">`)
	assert.NotRegexp(t, `<html[^>]*data-theme`, body)
	assert.Regexp(t, `<button type="button"[^>]*data-theme-toggle`, body)
}

func TestTypeClass(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ in, want string }{
		{"project", "project"},
		{"user", "user"},
		{"feedback", "feedback"},
		{"reference", "reference"},
		{"custom", "other"},
		{"", "other"},
	} {
		assert.Equal(t, tc.want, typeClass(tc.in), tc.in)
	}
}

func TestMemoryRowsCarryTypeTab(t *testing.T) {
	t.Parallel()

	tmpl, err := parseTemplates()
	require.NoError(t, err)

	var out bytes.Buffer
	err = tmpl.ExecuteTemplate(&out, "memory_rows.html", memoriesView{Rows: []memoryRow{
		{Href: "/a", Title: "A", Type: "user", TypeClass: typeClass("user"), Status: "active"},
		{Href: "/b", Title: "B", Type: "custom", TypeClass: typeClass("custom"), Status: "deleted"},
	}})
	require.NoError(t, err)

	body := out.String()
	assert.Contains(t, body, "type-tab-user")
	assert.Contains(t, body, "type-tab-other")
	assert.NotContains(t, body, "chip-status-active", "rows show a status chip only when not active")
	assert.Contains(t, body, "chip-status-deleted")
}

func TestAppJSMovesRowSelection(t *testing.T) {
	t.Parallel()

	js, err := assetFS.ReadFile("static/app.js")
	require.NoError(t, err)

	body := string(js)
	assert.Contains(t, body, "document.addEventListener('htmx:afterRequest', markSelectedRow)")
	assert.Contains(t, body, "'data-row-link'")
	assert.Contains(t, body, "setAttribute('aria-current', 'true')")
}
