package webui

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
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
