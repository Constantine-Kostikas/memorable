package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

func compareHistory() []memory.Version {
	at := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	return []memory.Version{
		{Version: 3, Title: "T", Content: "a\nc\nd", Tags: []string{"design"}, Type: memory.TypeReference, ChangeKind: "update", ChangedAt: at.Add(2 * time.Hour)},
		{Version: 1, Title: "T", Content: "a\nb", Tags: []string{"ui"}, Type: memory.TypeProject, ChangeKind: "create", ChangedAt: at},
		{Version: 2, Title: "T", Content: "a\nb\nc", Tags: []string{"ui"}, Type: memory.TypeProject, ChangeKind: "update", ChangedAt: at.Add(time.Hour)},
	}
}

func TestDefaultCompare(t *testing.T) {
	t.Parallel()
	from, to, ok := defaultCompare(compareHistory())
	assert.True(t, ok)
	assert.Equal(t, [2]int{2, 3}, [2]int{from, to})

	_, _, ok = defaultCompare(compareHistory()[:1])
	assert.False(t, ok)
}

func TestBuildCompare(t *testing.T) {
	t.Parallel()
	h := compareHistory()

	view, err := buildCompare(h, 3, 1, 3)
	require.NoError(t, err)
	assert.Equal(t, 1, view.From)
	assert.Equal(t, 3, view.To)
	assert.Equal(t, 2, view.Apart)
	assert.Equal(t, 2, view.Added)
	assert.Equal(t, 1, view.Removed)
	assert.Equal(t, []string{"Type: project to reference", "Tags: +design, -ui"}, view.PropertyChanges)
	assert.False(t, view.OOB)
	require.Len(t, view.Options, 3)
	assert.Equal(t, compareOption{Version: 3, Label: "v3 · 2026-11-16 (current)", Current: true}, view.Options[0])
	assert.Equal(t, "v1 · 2026-11-16", view.Options[2].Label)

	swapped, err := buildCompare(h, 3, 3, 1)
	require.NoError(t, err)
	assert.Equal(t, [2]int{1, 3}, [2]int{swapped.From, swapped.To})
	assert.True(t, swapped.OOB)
	assert.Equal(t, view.Diff, swapped.Diff)

	for _, pair := range [][2]int{{2, 2}, {1, 99}, {-1, 2}} {
		_, err := buildCompare(h, 3, pair[0], pair[1])
		assert.ErrorIs(t, err, errCompareInput, pair)
	}
}

func TestCompareEndpoint(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)
	get := func(query string) *httptest.ResponseRecorder {
		return send(f.h, http.MethodGet, f.path("/compare?"+query), "", withCookie(f.c), withHeader("HX-Request", "true"))
	}

	rec := get("from=1&to=2")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "- old line")
	assert.Contains(t, rec.Body.String(), "&#43; line two")
	assert.Contains(t, rec.Body.String(), "1 line added, 1 removed")

	for _, query := range []string{"from=abc&to=2", "from=-1&to=2", "from=1&to=99", "from=1&to=1", "to=2"} {
		bad := get(query)
		assert.Equal(t, http.StatusUnprocessableEntity, bad.Code, query)
		assert.Contains(t, bad.Body.String(), "Pick two different versions of this memory.", query)
	}

	swapped := get("from=2&to=1")
	require.Equal(t, http.StatusOK, swapped.Code)
	assert.Contains(t, swapped.Body.String(), `hx-swap-oob="true"`)
	assert.Contains(t, swapped.Body.String(), "- old line")
}

func TestCompareHidesQuarantined(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, func(m *detailMemories, id uuid.UUID) {
		m.fetched = []memory.Fetched{{Ref: id.String(), Memory: memory.Memory{ID: id, Status: memory.StatusQuarantined}, Err: memory.ErrQuarantined}}
	})

	rec := send(f.h, http.MethodGet, f.path("/compare?from=1&to=2"), "", withCookie(f.c), withHeader("HX-Request", "true"))

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.NotContains(t, rec.Body.String(), "line")
	assert.Empty(t, f.mem.called("History"))
}
