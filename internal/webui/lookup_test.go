package webui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

type lookupFixture struct {
	h    http.Handler
	c    *http.Cookie
	mems map[string]*detailMemories
}

func newLookupFixture(t *testing.T, results ...memory.Summary) lookupFixture {
	t.Helper()
	mems := map[string]*detailMemories{
		"personal": {results: results},
		"work":     {results: results},
	}
	cfg := testConfig(&backendRecorder{})
	cfg.Backend = func(_ context.Context, name string) (Backend, error) {
		mem, ok := mems[name]
		if !ok {
			return Backend{}, errors.New("unknown context")
		}
		return Backend{Memories: mem, Store: fakeStore{}}, nil
	}
	s, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(s.Close)
	h := s.Handler(testPort)
	return lookupFixture{h: h, c: login(t, s, h), mems: mems}
}

func (f lookupFixture) get(contextName string, query url.Values) *httptest.ResponseRecorder {
	return send(f.h, http.MethodGet, "/c/"+contextName+"/lookup?"+query.Encode(), "",
		withCookie(f.c), withHeader("HX-Request", "true"))
}

func (f lookupFixture) body(t *testing.T, contextName string, query url.Values) string {
	t.Helper()
	rec := f.get(contextName, query)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return rec.Body.String()
}

func lookupHit(title string, scope memory.Scope) memory.Summary {
	slug := strings.ToLower(strings.ReplaceAll(title, " ", "-"))
	return memory.Summary{ID: uuid.New(), Title: title, Slug: &slug, Type: memory.TypeReference, Scope: scope, Status: memory.StatusActive}
}

func options(body string) []string {
	return regexp.MustCompile(`<li role="option"[^>]*>`).FindAllString(body, -1)
}

func TestLookupUsesPassiveSearchInContext(t *testing.T) {
	t.Parallel()
	hit := lookupHit("Web server ports", memory.ScopeProject)
	f := newLookupFixture(t, hit)

	body := f.body(t, "work", url.Values{"q": {" web "}, "for": {"link"}})

	searches := f.mems["work"].lastSearches()
	require.Len(t, searches, 1)
	assert.Equal(t, "web", searches[0].Query)
	assert.True(t, searches[0].Passive)
	assert.Equal(t, 8, searches[0].K)
	assert.True(t, searches[0].AllProjects)
	assert.Empty(t, f.mems["personal"].lastSearches(), "only the URL's context is searched")
	opts := options(body)
	require.Len(t, opts, 1)
	assert.Contains(t, opts[0], `data-id="`+hit.ID.String()+`"`)
	assert.Contains(t, opts[0], `data-href="/c/work/memories/`+hit.ID.String()+`"`)
	assert.Contains(t, body, "Web server ports")
	assert.Contains(t, body, "web-server-ports")
	assert.Contains(t, body, "chip-type-reference")
}

func TestLookupExcludesProfile(t *testing.T) {
	t.Parallel()
	f := newLookupFixture(t, lookupHit("Profile fact", memory.ScopeProfile), lookupHit("Project fact", memory.ScopeProject))

	body := f.body(t, "personal", url.Values{"q": {"fact"}})

	assert.Len(t, options(body), 1)
	assert.NotContains(t, body, "Profile fact")
	assert.Contains(t, body, "Project fact")
}

func TestLookupExcludesCurrentMemoryForPickers(t *testing.T) {
	t.Parallel()
	current, other := lookupHit("Current", memory.ScopeProject), lookupHit("Other", memory.ScopeProject)
	f := newLookupFixture(t, current, other)

	for _, purpose := range []string{"link", "supersede"} {
		body := f.body(t, "personal", url.Values{"q": {"x"}, "for": {purpose}, "exclude": {current.ID.String()}})
		assert.NotContains(t, body, "Current", purpose)
		assert.Contains(t, body, "Other", purpose)
	}
	body := f.body(t, "personal", url.Values{"q": {"x"}, "for": {"jump"}, "exclude": {current.ID.String()}})
	assert.Contains(t, body, "Current", "jump lists every match")
}

func TestLookupEscapes(t *testing.T) {
	t.Parallel()
	f := newLookupFixture(t, lookupHit("<b>x</b>", memory.ScopeProject))

	body := f.body(t, "personal", url.Values{"q": {"x"}})

	assert.NotContains(t, body, "<b>x</b>")
	assert.Contains(t, body, "&lt;b&gt;x&lt;/b&gt;")
}

func TestLookupEmptyQueryListsNothing(t *testing.T) {
	t.Parallel()
	f := newLookupFixture(t, lookupHit("Anything", memory.ScopeProject))

	body := f.body(t, "personal", url.Values{"q": {"   "}, "for": {"jump"}})

	assert.Empty(t, f.mems["personal"].lastSearches())
	assert.Empty(t, options(body))
	assert.NotContains(t, body, "Anything")
}

func TestLookupUnknownContextIsNotFound(t *testing.T) {
	t.Parallel()
	f := newLookupFixture(t)

	rec := f.get("nowhere", url.Values{"q": {"x"}})

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestPickersSubmitTheirRefWithoutJS(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	body := send(f.h, http.MethodGet, f.path(""), "", withCookie(f.c), withHeader("HX-Request", "true")).Body.String()

	lookup := regexp.QuoteMeta("/c/personal/lookup?for=")
	exclude := regexp.QuoteMeta("&amp;exclude=" + f.id.String())
	assert.Regexp(t, `<input[^>]*name="to"[^>]*data-lookup[^>]*hx-get="`+lookup+`link`+exclude+`"`, body)
	assert.Regexp(t, `<input[^>]*name="replacement"[^>]*data-lookup[^>]*hx-get="`+lookup+`supersede`+exclude+`"`, body)
	assert.Contains(t, body, `hx-trigger="input changed delay:150ms"`)
	assert.NotContains(t, editorForm(t, body), "data-lookup", "pickers live outside the editor form")
}

func TestTagChipsWrapTheTagsField(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	body := send(f.h, http.MethodGet, f.path(""), "", withCookie(f.c), withHeader("HX-Request", "true")).Body.String()

	editor := editorForm(t, body)
	assert.Regexp(t, `<input[^>]*name="tags" value="go, ops" data-original="go, ops"[^>]*data-tags-source`, editor)
	assert.Regexp(t, `<div[^>]*data-tag-chips[^>]*hidden`, editor, "chips appear only when app.js runs")
	assert.Regexp(t, `<input[^>]*data-tag-entry`, editor)
	assert.NotRegexp(t, `<input[^>]*data-tag-entry[^>]*name=`, editor, "the chip entry is never submitted")
}
