package webui

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

const lookupLimit = 8

type lookupOption struct {
	ID    string
	Href  string
	Title string
	Type  string
	Slug  string
}

type lookupView struct {
	Query   string
	Options []lookupOption
}

// handleLookup answers the link and supersede pickers (and the palette) with up to eight title matches
// from a passive search in the request's own context. Profile memories are never offered, matching resolveRef.
func (s *Server) handleLookup(w http.ResponseWriter, r *http.Request, rc requestContext) {
	query := r.URL.Query()
	view := lookupView{Query: strings.TrimSpace(query.Get("q"))}
	if view.Query != "" {
		res, err := rc.Backend.Memories.Search(r.Context(), memory.SearchRequest{
			Query: view.Query, K: lookupLimit, AllProjects: true, Passive: true,
		})
		if err != nil {
			s.failResponse(w, r, rc, fmt.Errorf("lookup: %w", err))
			return
		}
		excluded := lookupExclusion(query)
		for _, m := range res.Results {
			if m.Scope == memory.ScopeProfile || m.ID == excluded {
				continue
			}
			view.Options = append(view.Options, lookupOptionOf(rc.Name, m))
		}
	}
	s.renderStatus(w, http.StatusOK, "lookup_results.html", view)
}

// lookupExclusion is the memory a picker must not offer: the one being linked or superseded.
func lookupExclusion(query url.Values) uuid.UUID {
	switch query.Get("for") {
	case "link", "supersede":
	default:
		return uuid.Nil
	}
	id, err := uuid.Parse(query.Get("exclude"))
	if err != nil {
		return uuid.Nil
	}
	return id
}

func lookupOptionOf(contextName string, m memory.Summary) lookupOption {
	option := lookupOption{ID: m.ID.String(), Href: memoryBase(contextName, m.ID), Title: m.Title, Type: m.Type.String()}
	if m.Slug != nil {
		option.Slug = *m.Slug
	}
	return option
}
