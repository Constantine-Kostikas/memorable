package webui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/memory"
	"github.com/constantine-kostikas/memorable/internal/store"
)

const hostile = "<img src=x onerror=alert(1)>"

type recordingMemories struct {
	Memories
	mu        sync.Mutex
	listReq   memory.ListRequest
	searchReq memory.SearchRequest
	list      memory.ListResponse
	search    memory.SearchResponse
	err       error
}

func (r *recordingMemories) List(_ context.Context, req memory.ListRequest) (memory.ListResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listReq = req
	return r.list, r.err
}

func (r *recordingMemories) Search(_ context.Context, req memory.SearchRequest) (memory.SearchResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.searchReq = req
	return r.search, r.err
}

func (r *recordingMemories) Review(context.Context) (memory.ReviewQueue, error) {
	return memory.ReviewQueue{}, nil
}

func (r *recordingMemories) lastList() memory.ListRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.listReq
}

func (r *recordingMemories) lastSearch() memory.SearchRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.searchReq
}

type statsStore struct {
	fakeStore
	byProject map[string]int
}

func (s statsStore) Stats(context.Context) (store.Stats, error) {
	return store.Stats{ByProject: s.byProject}, nil
}

func strp(s string) *string { return &s }

func newMemoriesServer(t *testing.T, mem *recordingMemories) (http.Handler, *http.Cookie) {
	t.Helper()
	cfg := testConfig(&backendRecorder{})
	cfg.Backend = func(context.Context, string) (Backend, error) {
		return Backend{Memories: mem, Store: statsStore{byProject: map[string]int{"alpha": 2, "(global)": 1, "beta": 1}}}, nil
	}
	s, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(s.Close)
	h := s.Handler(testPort)
	return h, login(t, s, h)
}

func getPage(h http.Handler, c *http.Cookie, target string, opts ...requestOption) *httptest.ResponseRecorder {
	return send(h, http.MethodGet, target, "", append([]requestOption{withCookie(c)}, opts...)...)
}

func summary(title string) memory.Summary {
	return memory.Summary{
		ID: uuid.New(), Title: title, Snippet: "snippet of " + title, Type: memory.TypeProject,
		Scope: memory.ScopeProject, Status: memory.StatusActive, Score: 0.42, Tags: []string{"go"},
	}
}

func TestListPage(t *testing.T) {
	t.Parallel()
	scoped, global := summary("Port map"), summary("House rules")
	scoped.Project, global.Scope = strp("alpha"), memory.ScopeGlobal
	next := uuid.New()
	mem := &recordingMemories{list: memory.ListResponse{Items: []memory.Summary{scoped, global}, Next: &next}}
	h, c := newMemoriesServer(t, mem)

	rec := getPage(h, c, "/c/personal/memories")

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "<html")
	assert.Contains(t, body, "Port map")
	assert.Contains(t, body, "alpha")
	assert.Contains(t, body, "House rules")
	assert.Contains(t, body, "(global)")
	assert.Contains(t, body, "active")
	assert.Contains(t, body, "cursor="+next.String())
	req := mem.lastList()
	assert.True(t, req.AllProjects)
	assert.Equal(t, []memory.Status{memory.StatusActive}, req.Statuses)
	assert.Equal(t, 50, req.Limit)
	assert.Nil(t, req.Cursor)
}

func TestListFiltersPassed(t *testing.T) {
	t.Parallel()
	mem := &recordingMemories{}
	h, c := newMemoriesServer(t, mem)
	cursor := uuid.New()

	q := url.Values{"project": {"alpha"}, "type": {"feedback"}, "status": {"flagged"}, "tag": {"go"}, "cursor": {cursor.String()}}
	rec := getPage(h, c, "/c/personal/memories?"+q.Encode())

	require.Equal(t, http.StatusOK, rec.Code)
	req := mem.lastList()
	require.NotNil(t, req.Project)
	assert.Equal(t, "alpha", *req.Project)
	assert.False(t, req.AllProjects)
	assert.Equal(t, []memory.Type{memory.TypeFeedback}, req.Types)
	assert.Equal(t, []memory.Status{memory.StatusFlagged}, req.Statuses)
	assert.Equal(t, []string{"go"}, req.Tags)
	require.NotNil(t, req.Cursor)
	assert.Equal(t, cursor, *req.Cursor)
}

func TestListRejectsBadFilters(t *testing.T) {
	t.Parallel()
	h, c := newMemoriesServer(t, &recordingMemories{})
	for _, q := range []string{"type=bogus", "status=bogus", "cursor=not-a-uuid"} {
		rec := getPage(h, c, "/c/personal/memories?"+q)
		assert.Equal(t, http.StatusBadRequest, rec.Code, q)
	}
}

func TestNextLinkEscapesFilters(t *testing.T) {
	t.Parallel()
	next := uuid.New()
	mem := &recordingMemories{list: memory.ListResponse{Items: []memory.Summary{summary("x")}, Next: &next}}
	h, c := newMemoriesServer(t, mem)

	q := url.Values{"tag": {`a&b="><x`}}
	rec := getPage(h, c, "/c/personal/memories?"+q.Encode())

	body := rec.Body.String()
	assert.Contains(t, body, "tag=a%26b%3D%22%3E%3Cx")
	assert.NotContains(t, body, `"><x`)
}

func TestProjectFilterOptionsFromStats(t *testing.T) {
	t.Parallel()
	h, c := newMemoriesServer(t, &recordingMemories{})
	body := getPage(h, c, "/c/personal/memories").Body.String()
	assert.Contains(t, body, `value="alpha"`)
	assert.Contains(t, body, `value="beta"`)
	assert.NotContains(t, body, `value="(global)"`)
}

func TestSearchUsesPassiveExplain(t *testing.T) {
	t.Parallel()
	hit := summary("Port map")
	mem := &recordingMemories{search: memory.SearchResponse{
		Results: []memory.Summary{hit},
		Explain: map[uuid.UUID]memory.Explanation{hit.ID: {VectorRank: 2, FTSRank: 1, TrigramRank: 3, Similarity: 0.77, RRF: 0.031, Textual: true}},
	}}
	h, c := newMemoriesServer(t, mem)

	rec := getPage(h, c, "/c/personal/memories?q=ports&tag=go&type=project")

	require.Equal(t, http.StatusOK, rec.Code)
	req := mem.lastSearch()
	assert.Equal(t, "ports", req.Query)
	assert.True(t, req.Passive)
	assert.True(t, req.Explain)
	assert.Equal(t, 20, req.K)
	assert.True(t, req.AllProjects)
	assert.Equal(t, []string{"go"}, req.Tags)
	assert.Equal(t, []memory.Type{memory.TypeProject}, req.Types)
	body := rec.Body.String()
	assert.Contains(t, body, "Port map")
	assert.Contains(t, body, "0.77")
	assert.Contains(t, body, "0.031")
	assert.NotContains(t, body, "vector search unavailable")
}

func TestSearchProjectOverridesAllProjects(t *testing.T) {
	t.Parallel()
	mem := &recordingMemories{}
	h, c := newMemoriesServer(t, mem)
	getPage(h, c, "/c/personal/memories?q=ports&project=alpha")
	req := mem.lastSearch()
	require.NotNil(t, req.Project)
	assert.Equal(t, "alpha", *req.Project)
	assert.False(t, req.AllProjects)
}

func TestSearchFragmentForHtmx(t *testing.T) {
	t.Parallel()
	mem := &recordingMemories{search: memory.SearchResponse{Results: []memory.Summary{summary("Port map")}}}
	h, c := newMemoriesServer(t, mem)

	rec := getPage(h, c, "/c/personal/memories?q=ports", withHeader("HX-Request", "true"))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.NotContains(t, body, "<html")
	assert.Contains(t, body, "Port map")

	full := getPage(h, c, "/c/personal/memories?q=ports").Body.String()
	assert.Contains(t, full, `hx-trigger="input changed delay:300ms, search"`)
	assert.Contains(t, full, `hx-get="/c/personal/memories"`)
	assert.Contains(t, full, `hx-target="#memory-rows"`)
	assert.Contains(t, full, `id="memory-rows"`)
	assert.NotContains(t, full, "hx-on")
}

func TestSearchVectorUnavailableNote(t *testing.T) {
	t.Parallel()
	mem := &recordingMemories{search: memory.SearchResponse{
		Results:           []memory.Summary{summary("Port map")},
		VectorUnavailable: true,
	}}
	h, c := newMemoriesServer(t, mem)

	rec := getPage(h, c, "/c/personal/memories?q=ports")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "vector search unavailable")
	assert.Contains(t, rec.Body.String(), "Port map")
}

func TestHostileContentEscaped(t *testing.T) {
	t.Parallel()
	bad := summary(hostile)
	bad.Snippet = hostile
	bad.Tags = []string{hostile}
	mem := &recordingMemories{
		list:   memory.ListResponse{Items: []memory.Summary{bad}},
		search: memory.SearchResponse{Results: []memory.Summary{bad}},
	}
	h, c := newMemoriesServer(t, mem)

	for _, target := range []string{"/c/personal/memories", "/c/personal/memories?q=x"} {
		body := getPage(h, c, target).Body.String()
		assert.NotContains(t, body, hostile, target)
		assert.Contains(t, body, "&lt;img src=x onerror=alert(1)&gt;", target)
	}
}

func TestMemoriesBackendErrorIsSafe(t *testing.T) {
	t.Parallel()
	mem := &recordingMemories{err: errors.New("pq: password=hunter2 refused")}
	h, c := newMemoriesServer(t, mem)

	rec := getPage(h, c, "/c/personal/memories")

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), "hunter2")
}

func TestBrokenContextShowsErrorForThatContextOnly(t *testing.T) {
	t.Parallel()
	b := &backendRecorder{broken: "work"}
	cfg := testConfig(b)
	cfg.Backend = func(ctx context.Context, name string) (Backend, error) {
		if _, err := b.open(ctx, name); err != nil {
			return Backend{}, err
		}
		return Backend{Memories: &recordingMemories{}, Store: fakeStore{}}, nil
	}
	s, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(s.Close)
	h := s.Handler(testPort)
	c := login(t, s, h)

	assert.Equal(t, http.StatusServiceUnavailable, getPage(h, c, "/c/work/memories").Code)
	assert.Equal(t, http.StatusOK, getPage(h, c, "/c/personal/memories").Code)
}

func TestHtmxErrorsAreFragments(t *testing.T) {
	t.Parallel()
	b := &backendRecorder{broken: "work"}
	failing := &recordingMemories{err: errors.New("pq: password=hunter2 refused")}
	cfg := testConfig(b)
	cfg.Contexts = []string{"personal", "work", "failing"}
	cfg.Backend = func(ctx context.Context, name string) (Backend, error) {
		if _, err := b.open(ctx, name); err != nil {
			return Backend{}, err
		}
		if name == "failing" {
			return Backend{Memories: failing, Store: fakeStore{}}, nil
		}
		return Backend{Memories: &recordingMemories{}, Store: fakeStore{}}, nil
	}
	s, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(s.Close)
	h := s.Handler(testPort)
	c := login(t, s, h)

	tests := []struct {
		name    string
		target  string
		status  int
		message string
	}{
		{name: "unknown context", target: "/c/nope/memories", status: http.StatusNotFound, message: "Unknown context."},
		{name: "context that fails to open", target: "/c/work/memories", status: http.StatusServiceUnavailable, message: "could not be opened"},
		{name: "failing backend", target: "/c/failing/memories", status: http.StatusInternalServerError, message: "Something went wrong."},
		{name: "invalid filter", target: "/c/personal/memories?type=bogus", status: http.StatusUnprocessableEntity, message: "unknown type"},
		{name: "admin route without --admin", target: "/c/personal/prune", status: http.StatusNotFound, message: "Page not found."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fragment := getPage(h, c, tt.target, withHeader("HX-Request", "true"))
			assert.Equal(t, tt.status, fragment.Code)
			assert.NotContains(t, fragment.Body.String(), "<html")
			assert.Contains(t, fragment.Body.String(), tt.message)
			assert.NotContains(t, fragment.Body.String(), "hunter2")

			page := getPage(h, c, tt.target)
			assert.Contains(t, page.Body.String(), "<html", "a plain request still gets the full page")
		})
	}
}

func TestNavLinksAreContextScoped(t *testing.T) {
	t.Parallel()
	h, c := newMemoriesServer(t, &recordingMemories{})
	body := getPage(h, c, "/c/work/memories").Body.String()
	for _, page := range []string{"memories", "review", "profile", "stats"} {
		assert.Contains(t, body, `href="/c/work/`+page+`"`)
		assert.NotContains(t, body, `href="/`+page+`"`)
	}
}

func TestHistoryRestoreReturnsFullPage(t *testing.T) {
	t.Parallel()
	h, c := newMemoriesServer(t, &recordingMemories{})

	rec := getPage(h, c, "/c/personal/memories?q=ports",
		withHeader("HX-Request", "true"), withHeader("HX-History-Restore-Request", "true"))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "<html")
	assert.Equal(t, "HX-Request", rec.Header().Get("Vary"))
}

func TestHostileQueryAndProjectEscaped(t *testing.T) {
	t.Parallel()
	h, c := newMemoriesServer(t, &recordingMemories{})
	evil := `"><script>alert(1)</script>`

	q := url.Values{"q": {evil}, "project": {evil}}
	body := getPage(h, c, "/c/personal/memories?"+q.Encode()).Body.String()

	assert.NotContains(t, body, "<script>alert(1)")
	assert.NotContains(t, body, `"><script>`)
	assert.Contains(t, body, "&#34;&gt;&lt;script&gt;alert(1)&lt;/script&gt;")
}

func TestRelativeAge(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		ago  time.Duration
		want string
	}{
		{name: "seconds", ago: 30 * time.Second, want: "now"},
		{name: "future skew", ago: -time.Minute, want: "now"},
		{name: "minutes", ago: 5 * time.Minute, want: "5m"},
		{name: "hours", ago: 3 * time.Hour, want: "3h"},
		{name: "days", ago: 2 * 24 * time.Hour, want: "2d"},
		{name: "older than 30 days", ago: 45 * 24 * time.Hour, want: "2026-10-02"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, relativeAge(now, now.Add(-tt.ago)))
		})
	}
}

func TestMemoriesEmptyPane(t *testing.T) {
	t.Parallel()
	h, c := newMemoriesServer(t, &recordingMemories{list: memory.ListResponse{Items: []memory.Summary{summary("Port map")}}})

	body := getPage(h, c, "/c/personal/memories").Body.String()

	assert.Contains(t, body, `id="detail-pane"`)
	assert.Contains(t, body, "Select a memory, or press Ctrl K to jump to one.")
	assert.NotContains(t, body, `aria-current="true"`)
}

func TestLoadMoreAppends(t *testing.T) {
	t.Parallel()
	next := uuid.New()
	mem := &recordingMemories{list: memory.ListResponse{Items: []memory.Summary{summary("Port map")}, Next: &next}}
	h, c := newMemoriesServer(t, mem)

	body := getPage(h, c, "/c/personal/memories?type=project").Body.String()

	nextURL := "/c/personal/memories?cursor=" + next.String() + "&amp;type=project"
	assert.Contains(t, body, `href="`+nextURL+`"`)
	assert.Contains(t, body, `hx-get="`+nextURL+`"`)
	assert.Contains(t, body, `hx-target="#memory-rows" hx-swap="beforeend"`)
	assert.Contains(t, body, "Load more")

	fragment := getPage(h, c, "/c/personal/memories?type=project&cursor="+next.String(), withHeader("HX-Request", "true")).Body.String()
	assert.True(t, strings.HasPrefix(strings.TrimSpace(fragment), "<li"), "rows append into the list itself")
	assert.Contains(t, fragment, `id="load-more" hx-swap-oob="true"`)
}

func TestRowsCarryTypeTabAgeAndFilter(t *testing.T) {
	t.Parallel()
	row := summary("Port map")
	row.Project, row.Type, row.Status = strp("alpha"), memory.TypeReference, memory.StatusFlagged
	row.UpdatedAt = time.Now().Add(-3 * time.Hour)
	active := summary("House rules")
	active.UpdatedAt = time.Now().Add(-2 * 24 * time.Hour)
	mem := &recordingMemories{list: memory.ListResponse{Items: []memory.Summary{row, active}}}
	h, c := newMemoriesServer(t, mem)

	body := getPage(h, c, "/c/personal/memories?tag=go&status=flagged").Body.String()

	assert.Contains(t, body, "type-tab-reference")
	assert.Contains(t, body, "reference · alpha · 3h")
	assert.Contains(t, body, "project · (global) · 2d")
	assert.Contains(t, body, `href="/c/personal/memories/`+row.ID.String()+`?status=flagged&amp;tag=go"`)
	assert.Contains(t, body, `hx-target="#detail-pane" hx-swap="outerHTML" hx-push-url="true"`)
	assert.Contains(t, body, "chip-status-flagged")
	assert.NotContains(t, body, "chip-status-active", "the status chip shows only when not active")
}
