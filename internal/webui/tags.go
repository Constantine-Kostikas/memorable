package webui

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

const tagSuggestLimit = 8

type tagOptionsView struct {
	Text    string
	Options []memory.TagCount
}

// handleTags suggests existing tags for the list's tag filter, prefix matches first.
func (s *Server) handleTags(w http.ResponseWriter, r *http.Request, rc requestContext) {
	text := strings.TrimSpace(r.URL.Query().Get("tag"))
	counts, err := rc.Backend.Memories.TagCounts(r.Context(), true, memory.DefaultTagLimit)
	if err != nil {
		s.failResponse(w, r, rc, fmt.Errorf("tag suggestions: %w", err))
		return
	}
	s.renderStatus(w, http.StatusOK, "tag_options.html", tagOptionsView{Text: text, Options: matchTags(counts, text, tagSuggestLimit)})
}

// matchTags keeps tags containing text, case-insensitively: prefix matches first, then other matches,
// each in count order. Blank text keeps the most used tags.
func matchTags(counts []memory.TagCount, text string, limit int) []memory.TagCount {
	needle := strings.ToLower(strings.TrimSpace(text))
	var prefix, inner []memory.TagCount
	for _, c := range counts {
		tag := strings.ToLower(c.Tag)
		switch {
		case strings.HasPrefix(tag, needle):
			prefix = append(prefix, c)
		case strings.Contains(tag, needle):
			inner = append(inner, c)
		}
	}
	matches := append(prefix, inner...)
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches
}
