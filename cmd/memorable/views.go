package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

type summaryView struct {
	ID      string   `json:"id"`
	Slug    string   `json:"slug,omitempty"`
	Title   string   `json:"title"`
	Snippet string   `json:"snippet"`
	Type    string   `json:"type"`
	Tags    []string `json:"tags"`
	Scope   string   `json:"scope"`
	Status  string   `json:"status"`
	Score   float64  `json:"score"`
}

type searchView struct {
	SearchID string        `json:"search_id"`
	Results  []summaryView `json:"results"`
}

type memoryView struct {
	ID        string     `json:"id"`
	Slug      string     `json:"slug,omitempty"`
	Title     string     `json:"title"`
	Content   string     `json:"content"`
	Type      string     `json:"type"`
	Tags      []string   `json:"tags"`
	Source    string     `json:"source"`
	Status    string     `json:"status"`
	Scope     string     `json:"scope"`
	Project   string     `json:"project,omitempty"`
	Pinned    bool       `json:"pinned"`
	Version   int        `json:"version"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func toSummaryView(s memory.Summary) summaryView {
	return summaryView{ID: s.ID.String(), Slug: deref(s.Slug), Title: s.Title, Snippet: s.Snippet, Type: s.Type.String(),
		Tags: s.Tags, Scope: s.Scope.String(), Status: s.Status.String(), Score: s.Score}
}

func toSummaryViews(ss []memory.Summary) []summaryView {
	out := make([]summaryView, 0, len(ss))
	for _, s := range ss {
		out = append(out, toSummaryView(s))
	}
	return out
}

func toMemoryView(m memory.Memory) memoryView {
	return memoryView{ID: m.ID.String(), Slug: deref(m.Slug), Title: m.Title, Content: m.Content, Type: m.Type.String(),
		Tags: m.Tags, Source: m.Source.String(), Status: m.Status.String(), Scope: m.Scope.String(),
		Project: deref(m.Project), Pinned: m.Pinned, Version: m.Version, ExpiresAt: m.ExpiresAt,
		CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt}
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func summaryRows(items []summaryView, withScore bool) ([]string, [][]string) {
	headers := []string{"ID", "SLUG", "TITLE", "TYPE", "SCOPE", "STATUS"}
	if withScore {
		headers = append(headers, "SCORE")
	}
	rows := make([][]string, 0, len(items))
	for _, s := range items {
		r := []string{shortID(s.ID), s.Slug, truncate(s.Title, 50), s.Type, s.Scope, s.Status}
		if withScore {
			r = append(r, fmt.Sprintf("%.3f", s.Score))
		}
		rows = append(rows, r)
	}
	return headers, rows
}

func truncate(s string, n int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n-1]) + "…"
}

// splitRefs separates UUID arguments from slugs.
func splitRefs(args []string) ([]uuid.UUID, []string) {
	var ids []uuid.UUID
	var slugs []string
	for _, a := range args {
		if id, err := uuid.Parse(a); err == nil {
			ids = append(ids, id)
			continue
		}
		slugs = append(slugs, a)
	}
	return ids, slugs
}

// argsUsage wraps a positional-argument validator so violations exit with code 2.
func argsUsage(v cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := v(cmd, args); err != nil {
			return usageError{err}
		}
		return nil
	}
}
