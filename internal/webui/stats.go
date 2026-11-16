package webui

import (
	"fmt"
	"net/http"
	"sort"
)

type countRow struct {
	Name  string
	Count int
}

type statsView struct {
	Context     string
	Total       int
	Embedded    int
	PendingJobs int
	Groups      []statsGroup
}

type statsGroup struct {
	Heading string
	// Typed groups name memory types and show them in their type colors.
	Typed bool
	Rows  []countRow
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request, rc requestContext) {
	stats, err := rc.Backend.Store.Stats(r.Context())
	if err != nil {
		s.fail(w, r, rc, fmt.Errorf("load stats: %w", err))
		return
	}
	view := statsView{
		Context: rc.Name, Total: stats.Total, Embedded: stats.Embedded, PendingJobs: stats.PendingJobs,
		Groups: []statsGroup{
			{Heading: "By status", Rows: countRows(stats.ByStatus)},
			{Heading: "By type", Typed: true, Rows: countRows(stats.ByType)},
			{Heading: "By project", Rows: countRows(stats.ByProject)},
		},
	}
	page := s.contextPage(r, rc, "Stats", "")
	page.Page, page.Stats = pageStats, &view
	s.renderTemplate(w, "layout.html", page)
}

func countRows(counts map[string]int) []countRow {
	rows := make([]countRow, 0, len(counts))
	for name, n := range counts {
		rows = append(rows, countRow{Name: name, Count: n})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Count != rows[j].Count {
			return rows[i].Count > rows[j].Count
		}
		return rows[i].Name < rows[j].Name
	})
	return rows
}
