package webui

import (
	"bytes"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedAssets(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"static/app.css", "static/htmx.min.js", "static/app.js", "static/OFL.txt",
		"static/markdown-toolbar-element.js", "static/markdown-toolbar-element.LICENSE.txt",
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
	assert.Contains(t, body, `<script type="module" src="/static/markdown-toolbar-element.js"></script>`)
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

func renderLayout(t *testing.T, page pageData) string {
	t.Helper()

	tmpl, err := parseTemplates()
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, tmpl.ExecuteTemplate(&out, "layout.html", page))
	return out.String()
}

func dialogMarkup(t *testing.T, body, id string) string {
	t.Helper()

	start := strings.Index(body, `<dialog id="`+id+`"`)
	require.NotEqual(t, -1, start, "dialog %s missing", id)
	end := strings.Index(body[start:], "</dialog>")
	require.NotEqual(t, -1, end, "dialog %s is not closed", id)
	return body[start : start+end]
}

func TestLayoutHasPaletteAndShortcutDialogs(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		minutes   int
		wantPrune bool
	}{
		{name: "admin locked", minutes: 0, wantPrune: false},
		{name: "admin unlocked", minutes: 12, wantPrune: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			body := renderLayout(t, pageData{
				Title: "Memories", Context: "personal", Contexts: []string{"personal", "work"},
				AdminUnlockedMinutes: tc.minutes,
			})

			palette := dialogMarkup(t, body, "palette")
			assert.Contains(t, palette, `hx-get="/c/personal/lookup"`)
			for _, page := range []string{"review", "profile", "stats"} {
				assert.Equal(t, 1, strings.Count(palette, `data-href="/c/personal/`+page+`"`), page)
			}
			assert.Equal(t, tc.wantPrune, strings.Contains(palette, `data-href="/c/personal/prune"`))
			for _, name := range []string{"personal", "work"} {
				assert.Equal(t, 1, strings.Count(palette, `data-href="/c/`+name+`/memories"`), name)
			}
			assert.Regexp(t, `<li role="option" data-href="/c/personal/review"`, palette)

			shortcuts := dialogMarkup(t, body, "shortcuts")
			for _, key := range []string{"Ctrl K", "/", "j", "k", "e", "F", "Esc", "Ctrl S", "?"} {
				assert.Contains(t, shortcuts, "<kbd class=\"kbd\">"+key+"</kbd>", key)
			}
		})
	}
}

func TestLayoutWithoutContextHasNoPalette(t *testing.T) {
	t.Parallel()

	body := renderLayout(t, pageData{Title: "Error", Message: "nope"})
	assert.NotContains(t, body, `<dialog id="palette"`)
	assert.NotContains(t, body, "data-palette-open")
}

func headerOf(t *testing.T, body string) string {
	t.Helper()
	header := regexp.MustCompile(`(?s)<header.*?</header>`).FindString(body)
	require.NotEmpty(t, header, "top bar missing")
	return header
}

func TestTopBarSearchWorksWithoutJS(t *testing.T) {
	t.Parallel()

	for _, data := range []pageData{
		{Title: "Error", Message: "nope"},
		{Title: "Review", Page: pageReview, Review: &reviewView{}},
		{Title: "Profile", Page: pageProfile, Profile: &profileView{}},
		{Title: "Stats", Page: pageStats, Stats: &statsView{}},
	} {
		data.Context, data.Contexts = "work", []string{"personal", "work"}
		page := data.Page
		header := headerOf(t, renderLayout(t, data))

		form := regexp.MustCompile(`(?s)<form role="search" method="get" action="/c/work/memories"[^>]*>.*?</form>`).FindString(header)
		require.NotEmpty(t, form, "top-bar search form missing on page %q", page)
		assert.Regexp(t, `<input type="search" name="q"`, form)
		assert.Contains(t, form, `aria-label="Search memories"`)
		assert.Regexp(t, `<button type="button"[^>]*data-palette-open[^>]*hidden`, header, "the palette hint stays hidden until JS shows it")
	}
}

func TestWorkspaceTopBarHasNoSecondSearch(t *testing.T) {
	t.Parallel()

	header := headerOf(t, renderLayout(t, pageData{
		Title: "Memories", Page: pageMemories, Context: "work", Contexts: []string{"personal", "work"},
		Memories: &memoriesView{Context: "work"},
	}))

	assert.NotContains(t, header, `role="search"`, "the list pane has the workspace's search")
	assert.NotContains(t, header, `name="q"`)
	assert.Regexp(t, `<button type="button"[^>]*data-palette-open[^>]*hidden`, header, "Jump stays on the workspace")
}

func TestAdminBadgeHasNarrowVariant(t *testing.T) {
	t.Parallel()

	header := headerOf(t, renderLayout(t, pageData{Title: "Stats", Page: pageStats, Stats: &statsView{}, Context: "work", Contexts: []string{"work"}, AdminUnlockedMinutes: 15}))

	assert.Contains(t, header, `<span class="hidden min-[600px]:inline">Admin unlocked, locks in 15 min</span>`)
	assert.Contains(t, header, `<span class="min-[600px]:hidden">Admin, 15 min</span>`)
}

func TestDetailPaneCarriesMemoryHref(t *testing.T) {
	t.Parallel()

	tmpl, err := parseTemplates()
	require.NoError(t, err)

	for _, view := range []detailView{
		{Base: "/c/work/memories/0b4b7c8e-0000-4000-8000-000000000001", Editable: true},
		{Base: "/c/work/memories/0b4b7c8e-0000-4000-8000-000000000001", Quarantined: true},
	} {
		var out bytes.Buffer
		require.NoError(t, tmpl.ExecuteTemplate(&out, "detail.html", view))
		assert.Regexp(t, `<section id="detail-pane" data-memory-href="/c/work/memories/0b4b7c8e-0000-4000-8000-000000000001"`, out.String())
	}
}
