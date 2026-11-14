// Package transfer exports and imports memories as JSON or Markdown.
package transfer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

const (
	exportFormat  = "memorable-export"
	exportVersion = 1
	pageSize      = 100
)

// Source reads memories for export.
type Source interface {
	List(ctx context.Context, f memory.Filter, statuses []memory.Status, cursor *uuid.UUID, limit int) ([]memory.Memory, *uuid.UUID, error)
	History(ctx context.Context, id uuid.UUID) ([]memory.Version, error)
	LinksFor(ctx context.Context, ids []uuid.UUID) ([]memory.Link, error)
}

type exportFile struct {
	Format     string           `json:"format"`
	Version    int              `json:"version"`
	Context    string           `json:"context"`
	ExportedAt time.Time        `json:"exported_at"`
	Memories   []exportedMemory `json:"memories"`
}

type exportedVersion struct {
	Version    int       `json:"version"`
	Title      string    `json:"title"`
	Content    string    `json:"content"`
	Tags       []string  `json:"tags"`
	Type       string    `json:"type"`
	ChangeKind string    `json:"change_kind"`
	ChangedAt  time.Time `json:"changed_at"`
}

type exportedLink struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}

type exportedMemory struct {
	ID        string            `json:"id"`
	Slug      string            `json:"slug,omitempty"`
	Project   string            `json:"project,omitempty"`
	Type      string            `json:"type"`
	Title     string            `json:"title"`
	Content   string            `json:"content"`
	Tags      []string          `json:"tags"`
	Source    string            `json:"source"`
	Status    string            `json:"status"`
	Pinned    bool              `json:"pinned"`
	ExpiresAt *time.Time        `json:"expires_at,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	Versions  []exportedVersion `json:"versions"`
	Links     []exportedLink    `json:"links"`
}

var allStatuses = []memory.Status{memory.StatusActive, memory.StatusUnverified, memory.StatusFlagged,
	memory.StatusQuarantined, memory.StatusSuperseded, memory.StatusDeleted}

func all(ctx context.Context, src Source, owner string) ([]memory.Memory, error) {
	f := memory.Filter{Owner: owner, AllProjects: true, Now: time.Unix(0, 0)}
	var out []memory.Memory
	var cursor *uuid.UUID
	for {
		page, next, err := src.List(ctx, f, allStatuses, cursor, pageSize)
		if err != nil {
			return nil, fmt.Errorf("listing memories: %w", err)
		}
		out = append(out, page...)
		if next == nil {
			return out, nil
		}
		cursor = next
	}
}

func str(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ExportJSON writes every memory of owner, with history and links, as one JSON document.
func ExportJSON(ctx context.Context, src Source, owner, contextName string, w io.Writer) (int, error) {
	mems, err := all(ctx, src, owner)
	if err != nil {
		return 0, err
	}
	file := exportFile{Format: exportFormat, Version: exportVersion, Context: contextName, ExportedAt: time.Now().UTC()}
	for _, m := range mems {
		e := exportedMemory{ID: m.ID.String(), Slug: str(m.Slug), Project: str(m.Project), Type: m.Type.String(),
			Title: m.Title, Content: m.Content, Tags: m.Tags, Source: m.Source.String(), Status: m.Status.String(),
			Pinned: m.Pinned, ExpiresAt: m.ExpiresAt, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
			Versions: []exportedVersion{}, Links: []exportedLink{}}
		versions, err := src.History(ctx, m.ID)
		if err != nil {
			return 0, fmt.Errorf("history of %s: %w", m.ID, err)
		}
		for _, v := range versions {
			e.Versions = append(e.Versions, exportedVersion{Version: v.Version, Title: v.Title, Content: v.Content,
				Tags: v.Tags, Type: v.Type.String(), ChangeKind: v.ChangeKind, ChangedAt: v.ChangedAt})
		}
		links, err := src.LinksFor(ctx, []uuid.UUID{m.ID})
		if err != nil {
			return 0, fmt.Errorf("links of %s: %w", m.ID, err)
		}
		for _, l := range links {
			if l.From == m.ID {
				e.Links = append(e.Links, exportedLink{From: l.From.String(), To: l.To.String(), Kind: l.Kind})
			}
		}
		file.Memories = append(file.Memories, e)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(file); err != nil {
		return 0, fmt.Errorf("writing export: %w", err)
	}
	return len(file.Memories), nil
}

var unsafeFileChars = regexp.MustCompile(`[^a-z0-9-]+`)

func fileName(m memory.Memory) string {
	if m.Slug != nil {
		return unsafeFileChars.ReplaceAllString(*m.Slug, "-") + ".md"
	}
	return m.ID.String() + ".md"
}

// ExportMarkdown writes one Markdown file per live memory into dir, in a format ParsePath reads back.
func ExportMarkdown(ctx context.Context, src Source, owner, dir string) (int, error) {
	mems, err := all(ctx, src, owner)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, fmt.Errorf("creating %s: %w", dir, err)
	}
	n := 0
	for _, m := range mems {
		if m.Status == memory.StatusDeleted || m.Status == memory.StatusSuperseded {
			continue
		}
		name := m.ID.String()
		if m.Slug != nil {
			name = *m.Slug
		}
		var b strings.Builder
		fmt.Fprintf(&b, "---\nname: %s\ndescription: %s\ntype: %s\n", name, oneLine(m.Title), m.Type)
		if len(m.Tags) > 0 {
			fmt.Fprintf(&b, "tags: [%s]\n", strings.Join(m.Tags, ", "))
		}
		if m.Pinned {
			b.WriteString("pinned: true\n")
		}
		if m.Project == nil {
			b.WriteString("scope: global\n")
		}
		fmt.Fprintf(&b, "---\n%s\n", m.Content)
		if err := os.WriteFile(filepath.Join(dir, fileName(m)), []byte(b.String()), 0o600); err != nil {
			return n, fmt.Errorf("writing %s: %w", fileName(m), err)
		}
		n++
	}
	return n, nil
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
