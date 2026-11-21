package webui

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

func (d *detailMemories) TagCounts(_ context.Context, allProjects bool, limit int) ([]memory.TagCount, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, call{Name: "TagCounts"})
	d.tagAllProjects = allProjects
	if limit < len(d.tagCounts) {
		return d.tagCounts[:limit], nil
	}
	return d.tagCounts, nil
}

func tagNames(counts []memory.TagCount) []string {
	names := make([]string, 0, len(counts))
	for _, c := range counts {
		names = append(names, c.Tag)
	}
	return names
}

func TestMatchTags(t *testing.T) {
	t.Parallel()
	counts := []memory.TagCount{{Tag: "web-ui", Count: 4}, {Tag: "go", Count: 3}, {Tag: "golang", Count: 2}, {Tag: "cargo", Count: 1}}

	assert.Equal(t, []string{"go", "golang", "cargo"}, tagNames(matchTags(counts, "GO", 8)))
	assert.Equal(t, []string{"web-ui", "go"}, tagNames(matchTags(counts, "", 2)))
	assert.Equal(t, []string{"web-ui", "go"}, tagNames(matchTags(counts, "  ", 2)))
	assert.Empty(t, matchTags(counts, "zzz", 8))
}

func TestTagsEndpoint(t *testing.T) {
	t.Parallel()
	f := newLookupFixture(t)
	counts := []memory.TagCount{
		{Tag: "web-ui", Count: 9}, {Tag: "go", Count: 8}, {Tag: "golang", Count: 7}, {Tag: "cargo", Count: 6},
		{Tag: "profile-only", Count: 5}, {Tag: "<b>", Count: 1},
	}
	for i := range 10 {
		counts = append(counts, memory.TagCount{Tag: fmt.Sprintf("extra-%d", i), Count: 1})
	}
	mem := f.mems["personal"]
	mem.tagCounts = counts

	body := f.tagsBody(t, "go")
	assert.Contains(t, body, `data-value="go"`)
	assert.Contains(t, body, `data-value="golang"`)
	assert.NotContains(t, body, `data-value="web-ui"`)
	assert.True(t, mem.tagAllProjects)

	assert.Contains(t, f.tagsBody(t, "zzz"), "No matching tags.")
	assert.Len(t, regexp.MustCompile(`role="option"`).FindAllString(f.tagsBody(t, ""), -1), tagSuggestLimit)
	assert.Contains(t, f.tagsBody(t, "PROFILE"), `data-value="profile-only"`)

	escaped := f.tagsBody(t, "<")
	assert.NotContains(t, escaped, "<b>")
	assert.Contains(t, escaped, "&lt;b&gt;")
}

func (f lookupFixture) tagsBody(t *testing.T, text string) string {
	t.Helper()
	rec := send(f.h, http.MethodGet, "/c/personal/tags?tag="+url.QueryEscape(text), "", withCookie(f.c), withHeader("HX-Request", "true"))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return rec.Body.String()
}
