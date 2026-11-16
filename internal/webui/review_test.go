package webui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
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

type reviewMemories struct {
	Memories
	queue memory.ReviewQueue
	err   error
}

func (r reviewMemories) Review(context.Context) (memory.ReviewQueue, error) { return r.queue, r.err }

// Get finds nothing, so conflict sides render as unavailable.
func (r reviewMemories) Get(_ context.Context, req memory.GetRequest) ([]memory.Fetched, error) {
	out := make([]memory.Fetched, 0, len(req.IDs))
	for _, id := range req.IDs {
		out = append(out, memory.Fetched{Ref: id.String(), Err: memory.ErrNotFound})
	}
	return out, nil
}

type pageStore struct {
	stats      store.Stats
	statsErr   error
	profile    []memory.Memory
	profileErr error
}

func (p pageStore) Stats(context.Context) (store.Stats, error) { return p.stats, p.statsErr }

func (p pageStore) ProfileList(context.Context) ([]memory.Memory, error) {
	return p.profile, p.profileErr
}

func newPageServer(t *testing.T, mem Memories, st StoreReads, logs *bytes.Buffer) (http.Handler, *http.Cookie) {
	t.Helper()
	cfg := testConfig(&backendRecorder{})
	if logs != nil {
		cfg.Logger = slog.New(slog.NewTextHandler(logs, nil))
	}
	cfg.Backend = func(context.Context, string) (Backend, error) { return Backend{Memories: mem, Store: st}, nil }
	s, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(s.Close)
	h := s.Handler(testPort)
	return h, login(t, s, h)
}

func queued(title string) memory.Memory {
	return memory.Memory{
		ID: uuid.New(), Title: title, Content: "content of " + title, Type: memory.TypeProject, Status: memory.StatusFlagged,
		Scope: memory.ScopeGlobal, UpdatedAt: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC),
	}
}

func TestReviewSections(t *testing.T) {
	t.Parallel()
	flagged, expired, stale, unverified := queued("Flagged one"), queued("Expired one"), queued("Stale one"), queued(hostile)
	conflict := memory.Link{From: uuid.New(), To: uuid.New(), Kind: memory.LinkContradicts}
	mem := reviewMemories{queue: memory.ReviewQueue{
		Flagged: []memory.Memory{flagged}, Expired: []memory.Memory{expired}, Stale: []memory.Memory{stale},
		Unverified: []memory.Memory{unverified}, Conflicts: []memory.Link{conflict}, QuarantinedCount: 2,
	}}
	h, c := newPageServer(t, mem, fakeStore{}, nil)

	rec := getPage(h, c, "/c/personal/review")

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	for _, want := range []string{"Flagged", "Expired", "Stale", "Unverified", "Conflicts", "Quarantined"} {
		assert.Contains(t, body, want)
	}
	for _, m := range []memory.Memory{flagged, expired, stale} {
		assert.Contains(t, body, m.Title)
		assert.Contains(t, body, `href="/c/personal/memories/`+m.ID.String()+`"`)
	}
	assert.Contains(t, body, "&lt;img src=x onerror=alert(1)&gt;")
	assert.NotContains(t, body, hostile)
	assert.Contains(t, body, `href="/c/personal/memories/`+conflict.From.String()+`"`)
	assert.Contains(t, body, `href="/c/personal/memories/`+conflict.To.String()+`"`)
	assert.Contains(t, body, "Review (7)")
}

func TestReviewQuarantinedCountOnly(t *testing.T) {
	t.Parallel()
	mem := reviewMemories{queue: memory.ReviewQueue{QuarantinedCount: 3, Flagged: []memory.Memory{queued("Visible flagged")}}}
	h, c := newPageServer(t, mem, fakeStore{}, nil)

	rec := getPage(h, c, "/c/personal/review")

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "3 quarantined")
	assert.Contains(t, body, "memorable approve")
	assert.NotContains(t, mainContent(t, body), "<form", "the page content offers no actions; the top bar's search form is chrome")
	assert.Equal(t, 1, bytes.Count([]byte(body), []byte("Visible flagged")))
}

func TestReviewCountOnEveryPage(t *testing.T) {
	t.Parallel()
	mem := reviewMemories{queue: memory.ReviewQueue{Flagged: []memory.Memory{queued("a")}, QuarantinedCount: 1}}
	h, c := newPageServer(t, mem, pageStore{}, nil)

	for _, path := range []string{"/c/personal/review", "/c/personal/profile", "/c/personal/stats", "/c/personal/memories/not-a-uuid"} {
		rec := getPage(h, c, path)
		assert.Contains(t, rec.Body.String(), "Review (2)", path)
	}
}

func TestReviewCountFailureDoesNotFailPage(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	mem := reviewMemories{err: errors.New("boom")}
	h, c := newPageServer(t, mem, pageStore{stats: store.Stats{Total: 1}}, &logs)

	rec := getPage(h, c, "/c/personal/stats")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Regexp(t, `Review</a>`, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "Review (")
	assert.Equal(t, 1, bytes.Count(logs.Bytes(), []byte("boom")))
}

func TestReviewFailureShowsSafeError(t *testing.T) {
	t.Parallel()
	mem := reviewMemories{err: memory.ErrUnavailable}
	h, c := newPageServer(t, mem, fakeStore{}, nil)

	rec := getPage(h, c, "/c/personal/review")

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), "Memory store unavailable")
}

func TestStatsPage(t *testing.T) {
	t.Parallel()
	st := pageStore{stats: store.Stats{
		Total: 12, Embedded: 9, PendingJobs: 4,
		ByStatus:  map[string]int{"active": 10, "flagged": 2},
		ByType:    map[string]int{"project": 7, "user": 5},
		ByProject: map[string]int{"alpha": 6, "(global)": 6, hostile: 1},
	}}
	h, c := newPageServer(t, reviewMemories{}, st, nil)

	rec := getPage(h, c, "/c/personal/stats")

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	for _, want := range []string{"Total", ">12<", "Embedded", ">9<", "Pending jobs", ">4<", "active", ">10<", "flagged", "project", "alpha", "(global)"} {
		assert.Contains(t, body, want)
	}
	assert.Contains(t, body, "&lt;img src=x")
	assert.NotContains(t, body, hostile)
}

func TestStatsFailure(t *testing.T) {
	t.Parallel()
	h, c := newPageServer(t, reviewMemories{}, pageStore{statsErr: memory.ErrUnavailable}, nil)

	rec := getPage(h, c, "/c/personal/stats")

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestProfileReadOnly(t *testing.T) {
	t.Parallel()
	slug := "style"
	m := memory.Memory{ID: uuid.New(), Slug: &slug, Title: "Style " + hostile, Content: "No em dashes. " + hostile, Type: memory.TypeUser, Scope: memory.ScopeProfile, Status: memory.StatusActive}
	h, c := newPageServer(t, reviewMemories{}, pageStore{profile: []memory.Memory{m}}, nil)

	rec := getPage(h, c, "/c/personal/profile")

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `href="/c/personal/memories/`+m.ID.String()+`"`)
	assert.Contains(t, body, "No em dashes.")
	assert.Contains(t, body, "style")
	assert.NotContains(t, body, hostile)
	main := mainContent(t, body)
	assert.NotContains(t, main, "<form")
	assert.NotContains(t, main, "<button")
}

func TestProfileEmpty(t *testing.T) {
	t.Parallel()
	h, c := newPageServer(t, reviewMemories{}, pageStore{}, nil)

	rec := getPage(h, c, "/c/personal/profile")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "No profile memories")
}

func TestBrokenContextOnReviewPages(t *testing.T) {
	t.Parallel()
	b := &backendRecorder{broken: "work"}
	s, err := New(testConfig(b))
	require.NoError(t, err)
	t.Cleanup(s.Close)
	h := s.Handler(testPort)
	c := login(t, s, h)

	for _, page := range []string{"review", "stats", "profile"} {
		assert.Equal(t, http.StatusServiceUnavailable, getPage(h, c, "/c/work/"+page).Code, page)
		assert.Equal(t, http.StatusOK, getPage(h, c, "/c/personal/"+page).Code, page)
	}
}

// mainContent is the page's <main> element, without the top bar and the dialogs after it.
func mainContent(t *testing.T, body string) string {
	t.Helper()
	_, main, found := strings.Cut(body, "<main")
	require.True(t, found)
	main, _, found = strings.Cut(main, "</main>")
	require.True(t, found)
	return main
}

// conflictMemories serves a review queue and answers Get for any number of ids, recording each request.
type conflictMemories struct {
	reviewMemories
	byID   map[uuid.UUID]memory.Fetched
	getErr error
	// supersedeErr is what Supersede returns.
	supersedeErr error

	mu    sync.Mutex
	gets  []memory.GetRequest
	calls []call
}

func (c *conflictMemories) Get(_ context.Context, r memory.GetRequest) ([]memory.Fetched, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gets = append(c.gets, r)
	if c.getErr != nil {
		return nil, c.getErr
	}
	out := make([]memory.Fetched, 0, len(r.IDs))
	for _, id := range r.IDs {
		f, ok := c.byID[id]
		if !ok {
			f = memory.Fetched{Ref: id.String(), Err: memory.ErrNotFound}
		}
		out = append(out, f)
	}
	return out, nil
}

func (c *conflictMemories) Delete(_ context.Context, id uuid.UUID) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, call{Name: "Delete", Args: []any{id}})
	return nil
}

func (c *conflictMemories) Supersede(_ context.Context, old, replacement uuid.UUID) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, call{Name: "Supersede", Args: []any{old, replacement}})
	return c.supersedeErr
}

func (c *conflictMemories) recordedGets() []memory.GetRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]memory.GetRequest(nil), c.gets...)
}

func (c *conflictMemories) recordedCalls() []call {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]call(nil), c.calls...)
}

func activeMemory(title, content string) memory.Memory {
	return memory.Memory{
		ID: uuid.New(), Title: title, Content: content, Type: memory.TypeProject, Status: memory.StatusActive,
		Scope: memory.ScopeGlobal, UpdatedAt: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC),
	}
}

func newConflictMemories(link memory.Link, sides ...memory.Fetched) *conflictMemories {
	mem := &conflictMemories{
		reviewMemories: reviewMemories{queue: memory.ReviewQueue{Conflicts: []memory.Link{link}}},
		byID:           map[uuid.UUID]memory.Fetched{},
	}
	for _, f := range sides {
		mem.byID[f.Memory.ID] = f
	}
	return mem
}

func fetchedOf(m memory.Memory) memory.Fetched { return memory.Fetched{Ref: m.ID.String(), Memory: m} }

func newConflictServer(t *testing.T, mem *conflictMemories) (*Server, http.Handler, *http.Cookie) {
	t.Helper()
	cfg := testConfig(&backendRecorder{})
	cfg.Backend = func(context.Context, string) (Backend, error) { return Backend{Memories: mem, Store: fakeStore{}}, nil }
	s, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(s.Close)
	h := s.Handler(testPort)
	return s, h, login(t, s, h)
}

func TestConflictShowsBothTitles(t *testing.T) {
	t.Parallel()
	from, to := activeMemory("Deploy on Fridays", "We deploy on Fridays."), activeMemory("Never deploy Fridays "+hostile, "No Friday deploys.")
	mem := newConflictMemories(memory.Link{From: from.ID, To: to.ID, Kind: memory.LinkContradicts}, fetchedOf(from), fetchedOf(to))
	_, h, c := newConflictServer(t, mem)

	rec := getPage(h, c, "/c/personal/review")

	require.Equal(t, http.StatusOK, rec.Code)
	body := mainContent(t, rec.Body.String())
	assert.Contains(t, body, "Deploy on Fridays")
	assert.Contains(t, body, "Never deploy Fridays &lt;img")
	assert.NotContains(t, body, hostile)
	assert.Contains(t, body, "We deploy on Fridays.")
	assert.Contains(t, body, "No Friday deploys.")
	assert.Contains(t, body, "sheet-ruled")
	for _, m := range []memory.Memory{from, to} {
		assert.Regexp(t, `<a href="/c/personal/memories/`+m.ID.String()+`"[^>]*>Open</a>`, body)
		assert.Contains(t, body, `action="/c/personal/memories/`+m.ID.String()+`/delete"`)
	}
	assert.NotContains(t, body, "hx-target=\"#detail-pane\"")
}

func TestConflictKeepThisOneSupersedesTheOther(t *testing.T) {
	t.Parallel()
	from, to := activeMemory("Keep me", "a"), activeMemory("Replace me", "b")
	mem := newConflictMemories(memory.Link{From: from.ID, To: to.ID, Kind: memory.LinkContradicts}, fetchedOf(from), fetchedOf(to))
	s, h, c := newConflictServer(t, mem)

	body := mainContent(t, getPage(h, c, "/c/personal/review").Body.String())
	assert.Regexp(t, `(?s)action="/c/personal/memories/`+to.ID.String()+`/supersede".*?name="replacement" value="`+from.ID.String()+`".*?Keep this one`, body)
	assert.Regexp(t, `(?s)action="/c/personal/memories/`+from.ID.String()+`/supersede".*?name="replacement" value="`+to.ID.String()+`".*?Keep this one`, body)

	form := url.Values{"csrf": {s.session.csrf}, "replacement": {from.ID.String()}, "return": {"review"}}
	rec := send(h, http.MethodPost, "/c/personal/memories/"+to.ID.String()+"/supersede", form.Encode(),
		withCookie(c), withHeader("Content-Type", "application/x-www-form-urlencoded"))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "<html")
	assert.Contains(t, rec.Body.String(), `id="review"`)
	assert.Contains(t, rec.Body.String(), "Memory superseded.")
	calls := mem.recordedCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, call{Name: "Supersede", Args: []any{to.ID, from.ID}}, calls[0])
}

func TestConflictDeleteFromReviewAnswersHTMXWithReviewFragment(t *testing.T) {
	t.Parallel()
	from, to := activeMemory("One", "a"), activeMemory("Two", "b")
	mem := newConflictMemories(memory.Link{From: from.ID, To: to.ID, Kind: memory.LinkContradicts}, fetchedOf(from), fetchedOf(to))
	s, h, c := newConflictServer(t, mem)

	form := url.Values{"csrf": {s.session.csrf}, "return": {"review"}}
	rec := send(h, http.MethodPost, "/c/personal/memories/"+from.ID.String()+"/delete", form.Encode(),
		withCookie(c), withHeader("Content-Type", "application/x-www-form-urlencoded"), withHeader("HX-Request", "true"))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "<html")
	assert.True(t, strings.HasPrefix(strings.TrimSpace(rec.Body.String()), `<div id="review"`), rec.Body.String())
	assert.Contains(t, rec.Body.String(), "Memory deleted.")
	assert.Equal(t, []call{{Name: "Delete", Args: []any{from.ID}}}, mem.recordedCalls())
}

func TestConflictFailedKeepAnswersHTMXWithReviewFragmentAndError(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		err    error
		status int
		want   string
	}{
		{name: "conflict", err: fmt.Errorf("supersede: %w", memory.ErrConflict), status: http.StatusConflict, want: conflictMessage},
		{name: "validation", err: fmt.Errorf("%w: cannot supersede a memory with itself", memory.ErrValidation),
			status: http.StatusUnprocessableEntity, want: "cannot supersede a memory with itself"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			from, to := activeMemory("Keep me", "a"), activeMemory("Replace me", "b")
			mem := newConflictMemories(memory.Link{From: from.ID, To: to.ID, Kind: memory.LinkContradicts}, fetchedOf(from), fetchedOf(to))
			mem.supersedeErr = tc.err
			s, h, c := newConflictServer(t, mem)

			form := url.Values{"csrf": {s.session.csrf}, "replacement": {from.ID.String()}, "return": {"review"}}
			rec := send(h, http.MethodPost, "/c/personal/memories/"+to.ID.String()+"/supersede", form.Encode(),
				withCookie(c), withHeader("Content-Type", "application/x-www-form-urlencoded"), withHeader("HX-Request", "true"))

			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			body := rec.Body.String()
			assert.NotContains(t, body, "<html")
			assert.True(t, strings.HasPrefix(strings.TrimSpace(body), `<div id="review"`), body)
			assert.Regexp(t, `<p role="alert"[^>]*>[^<]*`+regexp.QuoteMeta(template.HTMLEscapeString(tc.want)), body)
			assert.NotContains(t, body, "Memory superseded.")
		})
	}
}

func TestConflictHidesQuarantinedSide(t *testing.T) {
	t.Parallel()
	visible := activeMemory("Visible side", "visible content")
	quarantinedID := uuid.New()
	hidden := memory.Fetched{
		Ref:    quarantinedID.String(),
		Memory: memory.Memory{ID: quarantinedID, Title: "Secret title", Content: "secret content", Status: memory.StatusQuarantined},
		Err:    memory.ErrQuarantined,
	}
	mem := newConflictMemories(memory.Link{From: visible.ID, To: quarantinedID, Kind: memory.LinkContradicts}, fetchedOf(visible), hidden)
	_, h, c := newConflictServer(t, mem)

	rec := getPage(h, c, "/c/personal/review")

	require.Equal(t, http.StatusOK, rec.Code)
	body := mainContent(t, rec.Body.String())
	assert.Contains(t, body, quarantinedID.String())
	assert.Contains(t, body, "quarantined, content hidden")
	assert.NotContains(t, body, "Secret title")
	assert.NotContains(t, body, "secret content")
	assert.NotContains(t, body, `action="/c/personal/memories/`+quarantinedID.String()+`/delete"`)
	assert.NotContains(t, body, `action="/c/personal/memories/`+quarantinedID.String()+`/supersede"`)
	assert.NotContains(t, body, `name="replacement" value="`+quarantinedID.String()+`"`)
	assert.Contains(t, body, `action="/c/personal/memories/`+visible.ID.String()+`/delete"`)
	assert.NotContains(t, body, `action="/c/personal/memories/`+visible.ID.String()+`/supersede"`,
		"keeping the visible side would supersede the hidden one")
}

func TestConflictUnavailableSideDoesNotFailPage(t *testing.T) {
	t.Parallel()
	loaded, missing := activeMemory("Loaded side", "here"), uuid.New()
	mem := newConflictMemories(memory.Link{From: loaded.ID, To: missing, Kind: memory.LinkContradicts}, fetchedOf(loaded))
	_, h, c := newConflictServer(t, mem)

	rec := getPage(h, c, "/c/personal/review")

	require.Equal(t, http.StatusOK, rec.Code)
	body := mainContent(t, rec.Body.String())
	assert.Contains(t, body, "Loaded side")
	assert.Contains(t, body, missing.String())
	assert.Contains(t, body, "unavailable")
	assert.NotContains(t, body, `action="/c/personal/memories/`+missing.String()+`/delete"`)
}

func TestConflictWholeGetFailureDoesNotFailPage(t *testing.T) {
	t.Parallel()
	from, to := activeMemory("A", "a"), activeMemory("B", "b")
	mem := newConflictMemories(memory.Link{From: from.ID, To: to.ID, Kind: memory.LinkContradicts}, fetchedOf(from), fetchedOf(to))
	mem.getErr = memory.ErrUnavailable
	_, h, c := newConflictServer(t, mem)

	rec := getPage(h, c, "/c/personal/review")

	require.Equal(t, http.StatusOK, rec.Code)
	body := mainContent(t, rec.Body.String())
	assert.Contains(t, body, from.ID.String())
	assert.Contains(t, body, to.ID.String())
	assert.Equal(t, 2, strings.Count(body, "unavailable"))
	assert.NotContains(t, body, "/delete\"")
}

func TestConflictPreviewTruncates(t *testing.T) {
	t.Parallel()
	lines := make([]string, 0, 20)
	for i := 1; i <= 20; i++ {
		lines = append(lines, fmt.Sprintf("L%02d", i))
	}
	long, short := activeMemory("Long", strings.Join(lines, "\n")+"\n"), activeMemory("Short", "only line\n")
	mem := newConflictMemories(memory.Link{From: long.ID, To: short.ID, Kind: memory.LinkContradicts}, fetchedOf(long), fetchedOf(short))
	_, h, c := newConflictServer(t, mem)

	body := mainContent(t, getPage(h, c, "/c/personal/review").Body.String())

	assert.Contains(t, body, "L01")
	assert.Contains(t, body, "L12")
	assert.NotContains(t, body, "L13")
	assert.NotContains(t, body, "L20")
	assert.Equal(t, 1, strings.Count(body, "…"), "only the truncated side gets an ellipsis line")
}

func TestConflictFetchesPassively(t *testing.T) {
	t.Parallel()
	a, b, x, y := activeMemory("A", "a"), activeMemory("B", "b"), activeMemory("X", "x"), activeMemory("Y", "y")
	mem := newConflictMemories(memory.Link{From: a.ID, To: b.ID, Kind: memory.LinkContradicts}, fetchedOf(a), fetchedOf(b), fetchedOf(x), fetchedOf(y))
	mem.queue.Conflicts = append(mem.queue.Conflicts, memory.Link{From: x.ID, To: y.ID, Kind: memory.LinkContradicts})
	_, h, c := newConflictServer(t, mem)

	require.Equal(t, http.StatusOK, getPage(h, c, "/c/personal/review").Code)

	gets := mem.recordedGets()
	require.Len(t, gets, 2, "one Get per conflict")
	assert.Equal(t, []uuid.UUID{a.ID, b.ID}, gets[0].IDs)
	assert.Equal(t, []uuid.UUID{x.ID, y.ID}, gets[1].IDs)
	for _, g := range gets {
		assert.True(t, g.Passive)
	}
}

func TestReviewRowsLinkToWorkspaceWithoutPane(t *testing.T) {
	t.Parallel()
	flagged := queued("Flagged row")
	h, c := newPageServer(t, reviewMemories{queue: memory.ReviewQueue{Flagged: []memory.Memory{flagged}}}, fakeStore{}, nil)

	body := mainContent(t, getPage(h, c, "/c/personal/review").Body.String())

	assert.Contains(t, body, `class="memory-row type-tab-project"`)
	assert.Contains(t, body, `href="/c/personal/memories/`+flagged.ID.String()+`"`)
	assert.NotContains(t, body, "hx-get")
	assert.NotContains(t, body, "data-row-link")
	assert.Contains(t, body, "Flagged by feedback.")
}

func TestStatsByTypeUsesTypeColors(t *testing.T) {
	t.Parallel()
	st := pageStore{stats: store.Stats{ByType: map[string]int{"project": 7, "user": 5}, ByStatus: map[string]int{"project": 1}}}
	h, c := newPageServer(t, reviewMemories{}, st, nil)

	body := mainContent(t, getPage(h, c, "/c/personal/stats").Body.String())

	assert.Contains(t, body, "chip-type-project")
	assert.Contains(t, body, "chip-type-user")
	assert.Equal(t, 1, strings.Count(body, "chip-type-project"), "only the by-type group is colored")
}
