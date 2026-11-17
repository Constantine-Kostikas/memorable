package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", memory.ErrValidation, fmt.Sprintf(format, args...))
}

func parseID(field, s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, invalid("%s %q is not a valid id", field, s)
	}
	return id, nil
}

func parseIDs(field string, ss []string) ([]uuid.UUID, error) {
	out := make([]uuid.UUID, 0, len(ss))
	for _, s := range ss {
		id, err := parseID(field, s)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

func parseTypes(ss []string) ([]memory.Type, error) {
	out := make([]memory.Type, 0, len(ss))
	for _, s := range ss {
		t, err := memory.ParseType(s)
		if err != nil {
			return nil, err //nolint:wrapcheck // already an ErrValidation with the bad value
		}
		out = append(out, t)
	}
	return out, nil
}

func parseTime(field, s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, invalid("%s %q must be an RFC 3339 time", field, s)
	}
	return &t, nil
}

func toSummary(m memory.Memory) memory.Summary {
	return memory.Summary{ID: m.ID, Slug: m.Slug, Title: m.Title, Snippet: memory.Snippet(m.Content, memory.SnippetRunes),
		Type: m.Type, Tags: m.Tags, Scope: m.Scope, Status: m.Status}
}

type writeItem struct {
	Title      string   `json:"title" jsonschema:"short title, at most 120 characters"`
	Content    string   `json:"content" jsonschema:"one self-contained fact, at most 2000 characters"`
	Type       string   `json:"type" jsonschema:"user, feedback, project, or reference"`
	Source     string   `json:"source" jsonschema:"user_stated, claude_inferred, tool_output (read from files, web, or tools), or imported"`
	Tags       []string `json:"tags,omitempty" jsonschema:"lowercase tags"`
	Slug       string   `json:"slug,omitempty" jsonschema:"stable readable key such as area/topic"`
	Scope      string   `json:"scope,omitempty" jsonschema:"project (default) or global"`
	Pinned     bool     `json:"pinned,omitempty" jsonschema:"always include at session start"`
	ExpiresAt  string   `json:"expires_at,omitempty" jsonschema:"RFC 3339 time when this stops being true"`
	Supersedes string   `json:"supersedes,omitempty" jsonschema:"id of the memory this replaces"`
	OnSimilar  string   `json:"on_similar,omitempty" jsonschema:"ask (default) or keep_both"`
}

func (w writeItem) toNew() (memory.NewMemory, error) {
	typ, err := memory.ParseType(w.Type)
	if err != nil {
		return memory.NewMemory{}, err //nolint:wrapcheck // already an ErrValidation
	}
	src, err := memory.ParseSource(w.Source)
	if err != nil {
		return memory.NewMemory{}, err //nolint:wrapcheck // already an ErrValidation
	}
	onSimilar, err := memory.ParseOnSimilar(w.OnSimilar)
	if err != nil {
		return memory.NewMemory{}, err //nolint:wrapcheck // already an ErrValidation
	}
	if w.Scope != "" && w.Scope != "project" && w.Scope != "global" {
		return memory.NewMemory{}, invalid("scope %q must be project or global", w.Scope)
	}
	expires, err := parseTime("expires_at", w.ExpiresAt)
	if err != nil {
		return memory.NewMemory{}, err
	}
	n := memory.NewMemory{Title: w.Title, Content: w.Content, Type: typ, Source: src, Tags: w.Tags,
		Global: w.Scope == "global", Pinned: w.Pinned, ExpiresAt: expires, OnSimilar: onSimilar}
	if w.Slug != "" {
		n.Slug = &w.Slug
	}
	if w.Supersedes != "" {
		id, err := parseID("supersedes", w.Supersedes)
		if err != nil {
			return memory.NewMemory{}, err
		}
		n.Supersedes = &id
	}
	return n, nil
}

type writeInput struct {
	Memories []writeItem `json:"memories" jsonschema:"1 to 20 memories"`
}

func writeTool(ctx context.Context, svc *memory.Service, in writeInput) (string, error) {
	lines := make([]string, len(in.Memories))
	var valid []memory.NewMemory
	var index []int
	for i, item := range in.Memories {
		n, err := item.toNew()
		if err != nil {
			lines[i] = fmt.Sprintf("%d: rejected: %s", i+1, err)
			continue
		}
		valid = append(valid, n)
		index = append(index, i)
	}
	if len(valid) > 0 {
		results, err := svc.Write(ctx, valid)
		if err != nil {
			return "", err //nolint:wrapcheck // mapped at the boundary
		}
		for j, r := range results {
			lines[index[j]] = renderWrite(index[j]+1, r)
		}
	}
	return strings.Join(lines, "\n"), nil
}

func renderWrite(n int, r memory.WriteResult) string {
	id := ""
	if r.Memory != nil {
		id = " id=" + r.Memory.ID.String()
		if r.Memory.Slug != nil {
			id += " slug=" + *r.Memory.Slug
		}
	}
	line := fmt.Sprintf("%d: %s%s", n, r.Outcome, id)
	if r.Reason != "" {
		line += ": " + r.Reason
	}
	if len(r.Candidates) > 0 {
		line += "\n" + renderSummaries("memory-candidates", nil, r.Candidates)
	}
	return line
}

type getInput struct {
	IDs      []string `json:"ids,omitempty" jsonschema:"memory ids"`
	Slugs    []string `json:"slugs,omitempty" jsonschema:"memory slugs"`
	SearchID string   `json:"search_id,omitempty" jsonschema:"search_id from memory_search, if the ids came from one"`
}

func getTool(ctx context.Context, svc *memory.Service, in getInput) (string, error) {
	ids, err := parseIDs("id", in.IDs)
	if err != nil {
		return "", err
	}
	req := memory.GetRequest{IDs: ids, Slugs: in.Slugs}
	if in.SearchID != "" {
		sid, err := parseID("search_id", in.SearchID)
		if err != nil {
			return "", err
		}
		req.SearchID = &sid
	}
	fetched, err := svc.Get(ctx, req)
	if err != nil {
		return "", err //nolint:wrapcheck // mapped at the boundary
	}
	parts := make([]string, 0, len(fetched))
	for _, f := range fetched {
		if f.Err != nil {
			msg, _ := userMessage(f.Err)
			parts = append(parts, fmt.Sprintf("<memory-error%s%s/>", attr("ref", f.Ref), attr("error", msg)))
			continue
		}
		part := renderMemory(f.Memory)
		for _, l := range f.Links {
			part += fmt.Sprintf("\nlink: %s %s -> %s", l.Kind, l.From, l.To)
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "\n"), nil
}

type updateInput struct {
	ID              string   `json:"id"`
	ExpectedVersion *int     `json:"expected_version,omitempty" jsonschema:"version you read; rejects the update if it changed"`
	Title           *string  `json:"title,omitempty"`
	Content         *string  `json:"content,omitempty"`
	Tags            []string `json:"tags,omitempty" jsonschema:"replaces all tags"`
	Type            string   `json:"type,omitempty"`
	Pinned          *bool    `json:"pinned,omitempty"`
	ExpiresAt       *string  `json:"expires_at,omitempty" jsonschema:"RFC 3339 time, or empty to clear"`
}

func updateTool(ctx context.Context, svc *memory.Service, in updateInput) (string, error) {
	id, err := parseID("id", in.ID)
	if err != nil {
		return "", err
	}
	p := memory.Patch{Title: in.Title, Content: in.Content, Pinned: in.Pinned}
	if in.Tags != nil {
		p.Tags = &in.Tags
	}
	if in.Type != "" {
		t, err := memory.ParseType(in.Type)
		if err != nil {
			return "", err //nolint:wrapcheck // already an ErrValidation
		}
		p.Type = &t
	}
	if in.ExpiresAt != nil {
		exp, err := parseTime("expires_at", *in.ExpiresAt)
		if err != nil {
			return "", err
		}
		p.ExpiresAt = &exp
	}
	m, err := svc.Update(ctx, id, in.ExpectedVersion, p)
	if err != nil {
		return "", err //nolint:wrapcheck // mapped at the boundary
	}
	return fmt.Sprintf("updated id=%s version=%d status=%s", m.ID, m.Version, m.Status), nil
}

type idInput struct {
	ID string `json:"id"`
}

func deleteTool(ctx context.Context, svc *memory.Service, in idInput) (string, error) {
	id, err := parseID("id", in.ID)
	if err != nil {
		return "", err
	}
	if err := svc.Delete(ctx, id); err != nil {
		return "", err //nolint:wrapcheck // mapped at the boundary
	}
	return "deleted id=" + id.String() + " (restorable with memory_restore)", nil
}

type restoreInput struct {
	ID      string `json:"id"`
	Version *int   `json:"version,omitempty" jsonschema:"restore this version's text; omit to undelete"`
}

func restoreTool(ctx context.Context, svc *memory.Service, in restoreInput) (string, error) {
	id, err := parseID("id", in.ID)
	if err != nil {
		return "", err
	}
	m, err := svc.Restore(ctx, id, in.Version)
	if err != nil {
		return "", err //nolint:wrapcheck // mapped at the boundary
	}
	return fmt.Sprintf("restored id=%s version=%d status=%s", m.ID, m.Version, m.Status), nil
}

func historyTool(ctx context.Context, svc *memory.Service, in idInput) (string, error) {
	id, err := parseID("id", in.ID)
	if err != nil {
		return "", err
	}
	versions, err := svc.History(ctx, id)
	if err != nil {
		return "", err //nolint:wrapcheck // mapped at the boundary
	}
	var b strings.Builder
	for _, v := range versions {
		fmt.Fprintf(&b, "<memory-version%s%s%s%s>\n%s\n</memory-version>\n", attr("version", fmt.Sprint(v.Version)),
			attr("change", v.ChangeKind), attr("at", v.ChangedAt.UTC().Format(time.RFC3339)), attr("title", v.Title),
			escapeBody(v.Content))
	}
	return strings.TrimSpace(b.String()), nil
}

type searchInput struct {
	Query             string   `json:"query" jsonschema:"what you are looking for, in natural language or keywords"`
	K                 int      `json:"k,omitempty" jsonschema:"results to return, default 5, max 20"`
	Tags              []string `json:"tags,omitempty" jsonschema:"only memories with all of these tags"`
	TagsAny           []string `json:"tags_any,omitempty" jsonschema:"memories with at least one of these tags"`
	Types             []string `json:"types,omitempty"`
	AllProjects       bool     `json:"all_projects,omitempty" jsonschema:"include other projects' memories"`
	IncludeFlagged    bool     `json:"include_flagged,omitempty"`
	IncludeSuperseded bool     `json:"include_superseded,omitempty"`
}

func searchTool(ctx context.Context, svc *memory.Service, in searchInput) (string, error) {
	types, err := parseTypes(in.Types)
	if err != nil {
		return "", err
	}
	res, err := svc.Search(ctx, memory.SearchRequest{Query: in.Query, K: in.K, Tags: in.Tags, TagsAny: in.TagsAny, Types: types,
		AllProjects: in.AllProjects, IncludeFlagged: in.IncludeFlagged, IncludeSuperseded: in.IncludeSuperseded})
	if err != nil {
		return "", err //nolint:wrapcheck // mapped at the boundary
	}
	return renderSummaries("memory-results", map[string]string{"search_id": res.SearchID.String()}, res.Results), nil
}

type listInput struct {
	Tags        []string `json:"tags,omitempty"`
	TagsAny     []string `json:"tags_any,omitempty" jsonschema:"memories with at least one of these tags"`
	Types       []string `json:"types,omitempty"`
	Statuses    []string `json:"statuses,omitempty" jsonschema:"active, unverified, flagged, superseded, or deleted"`
	AllProjects bool     `json:"all_projects,omitempty"`
	Cursor      string   `json:"cursor,omitempty" jsonschema:"next_cursor from the previous page"`
	Limit       int      `json:"limit,omitempty" jsonschema:"default 20, max 100"`
}

func listTool(ctx context.Context, svc *memory.Service, in listInput) (string, error) {
	types, err := parseTypes(in.Types)
	if err != nil {
		return "", err
	}
	req := memory.ListRequest{Tags: in.Tags, TagsAny: in.TagsAny, Types: types, AllProjects: in.AllProjects, Limit: in.Limit}
	if in.Statuses != nil {
		req.Statuses = make([]memory.Status, 0, len(in.Statuses))
		for _, s := range in.Statuses {
			st, err := memory.ParseStatus(s)
			if err != nil {
				return "", err //nolint:wrapcheck // already an ErrValidation
			}
			req.Statuses = append(req.Statuses, st)
		}
	}
	if in.Cursor != "" {
		c, err := parseID("cursor", in.Cursor)
		if err != nil {
			return "", err
		}
		req.Cursor = &c
	}
	res, err := svc.List(ctx, req)
	if err != nil {
		return "", err //nolint:wrapcheck // mapped at the boundary
	}
	attrs := map[string]string{}
	if res.Next != nil {
		attrs["next_cursor"] = res.Next.String()
	}
	return renderSummaries("memory-list", attrs, res.Items), nil
}

type topInput struct {
	Limit int `json:"limit,omitempty" jsonschema:"default 10, max 20"`
}

func topTool(ctx context.Context, svc *memory.Service, in topInput) (string, error) {
	if in.Limit == 0 {
		in.Limit = 10
	}
	items, err := svc.Top(ctx, in.Limit)
	if err != nil {
		return "", err //nolint:wrapcheck // mapped at the boundary
	}
	return renderSummaries("memory-top", nil, items), nil
}

type feedbackInput struct {
	ID   string `json:"id"`
	Kind string `json:"kind" jsonschema:"useful, wrong, or outdated"`
	Note string `json:"note,omitempty" jsonschema:"why, in a few words"`
}

func feedbackTool(ctx context.Context, svc *memory.Service, in feedbackInput) (string, error) {
	id, err := parseID("id", in.ID)
	if err != nil {
		return "", err
	}
	kind, err := memory.ParseFeedbackKind(in.Kind)
	if err != nil {
		return "", err //nolint:wrapcheck // already an ErrValidation
	}
	if err := svc.Feedback(ctx, id, kind, in.Note); err != nil {
		return "", err //nolint:wrapcheck // mapped at the boundary
	}
	return "feedback recorded", nil
}

type linkInput struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Kind   string `json:"kind" jsonschema:"related, supersedes, or contradicts"`
	Remove bool   `json:"remove,omitempty"`
}

func linkTool(ctx context.Context, svc *memory.Service, in linkInput) (string, error) {
	from, err := parseID("from", in.From)
	if err != nil {
		return "", err
	}
	to, err := parseID("to", in.To)
	if err != nil {
		return "", err
	}
	if err := svc.Link(ctx, memory.Link{From: from, To: to, Kind: in.Kind}, in.Remove); err != nil {
		return "", err //nolint:wrapcheck // mapped at the boundary
	}
	if in.Remove {
		return "link removed", nil
	}
	return "link added", nil
}

type emptyInput struct{}

func reviewTool(ctx context.Context, svc *memory.Service, _ emptyInput) (string, error) {
	q, err := svc.Review(ctx)
	if err != nil {
		return "", err //nolint:wrapcheck // mapped at the boundary
	}
	var b strings.Builder
	for _, bucket := range []struct {
		name string
		mems []memory.Memory
	}{{"flagged", q.Flagged}, {"expired", q.Expired}, {"stale", q.Stale}, {"unverified", q.Unverified}} {
		sums := make([]memory.Summary, 0, len(bucket.mems))
		for _, m := range bucket.mems {
			sums = append(sums, toSummary(m))
		}
		b.WriteString(bucket.name + ":\n" + renderSummaries("memory-review", nil, sums) + "\n")
	}
	for _, l := range q.Conflicts {
		fmt.Fprintf(&b, "contradiction: %s <-> %s\n", l.From, l.To)
	}
	fmt.Fprintf(&b, "quarantined awaiting human review: %d (the user runs `memorable review`)", q.QuarantinedCount)
	return b.String(), nil
}

func (s *Server) addTools() {
	addTool(s, "memory_write", "Save durable facts: preferences, corrections, decisions with their reason, pointers. "+
		"Never secrets or transient task state. Similar existing memories return needs_decision.", writeTool)
	addTool(s, "memory_get", "Fetch full memories by id or slug (max 20). Pass search_id when the ids came from memory_search.", getTool)
	addTool(s, "memory_update", "Edit a memory; pass expected_version to avoid overwriting a concurrent change.", updateTool)
	addTool(s, "memory_delete", "Soft-delete a memory that is wrong and not worth keeping.", deleteTool)
	addTool(s, "memory_restore", "Undelete a memory, or restore an earlier version's text.", restoreTool)
	addTool(s, "memory_history", "List all versions of a memory.", historyTool)
	addTool(s, "memory_search", "Hybrid semantic and keyword search. Returns compact results; an empty result means nothing relevant.", searchTool)
	addTool(s, "memory_list", "Browse memories by tag, type, or status, paginated.", listTool)
	addTool(s, "memory_top", "Most used and pinned memories for this project.", topTool)
	addTool(s, "memory_feedback", "Report a memory as useful, wrong, or outdated. Wrong or outdated hides it until reviewed.", feedbackTool)
	addTool(s, "memory_link", "Link two memories as related, supersedes, or contradicts.", linkTool)
	addTool(s, "memory_review", "Memories needing attention: flagged, expired, stale, unverified, contradictions.", reviewTool)
}
