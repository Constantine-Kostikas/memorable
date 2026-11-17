package mcpserver

import (
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

// wrapperTag matches any opening or closing tag that starts with "memory", so stored text can never close
// or open one of the data wrappers this server emits.
var wrapperTag = regexp.MustCompile(`(?i)<(/?memory)`)

func escapeBody(s string) string { return wrapperTag.ReplaceAllString(s, "&lt;$1") }

func attr(name, value string) string { return " " + name + `="` + html.EscapeString(value) + `"` }

func renderMemory(m memory.Memory) string {
	var b strings.Builder
	b.WriteString("<memory")
	b.WriteString(attr("id", m.ID.String()))
	if m.Slug != nil {
		b.WriteString(attr("slug", *m.Slug))
	}
	b.WriteString(attr("title", m.Title))
	b.WriteString(attr("type", m.Type.String()))
	b.WriteString(attr("source", m.Source.String()))
	b.WriteString(attr("status", m.Status.String()))
	b.WriteString(attr("scope", m.Scope.String()))
	b.WriteString(attr("version", strconv.Itoa(m.Version)))
	if len(m.Tags) > 0 {
		b.WriteString(attr("tags", strings.Join(m.Tags, ",")))
	}
	if m.Pinned {
		b.WriteString(attr("pinned", "true"))
	}
	if m.ExpiresAt != nil {
		b.WriteString(attr("expires_at", m.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z")))
	}
	b.WriteString(">\n")
	b.WriteString(escapeBody(m.Content))
	b.WriteString("\n</memory>")
	return b.String()
}

// RenderTags formats tag counts as "a (12), b (7)".
func RenderTags(counts []memory.TagCount) string {
	parts := make([]string, 0, len(counts))
	for _, c := range counts {
		parts = append(parts, c.Tag+" ("+strconv.Itoa(c.Count)+")")
	}
	return strings.Join(parts, ", ")
}

func summaryLine(s memory.Summary) string {
	slug := ""
	if s.Slug != nil {
		slug = " slug=" + *s.Slug
	}
	return fmt.Sprintf("- id=%s%s [%s/%s/%s] score=%.3f | %s: %s",
		s.ID, slug, s.Type, s.Scope, s.Status, s.Score, escapeBody(oneLine(s.Title)), escapeBody(oneLine(s.Snippet)))
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// renderSummaries wraps compact results in a data element named tag with optional attributes.
func renderSummaries(tag string, attrs map[string]string, items []memory.Summary) string {
	var b strings.Builder
	b.WriteString("<" + tag)
	for _, k := range []string{"search_id", "next_cursor"} {
		if v, ok := attrs[k]; ok {
			b.WriteString(attr(k, v))
		}
	}
	fmt.Fprintf(&b, " count=\"%d\">\n", len(items))
	for _, s := range items {
		b.WriteString(summaryLine(s))
		b.WriteString("\n")
	}
	b.WriteString("</" + tag + ">")
	return b.String()
}

// RenderSummaries wraps compact results in a data element; stored text cannot break out of it.
func RenderSummaries(tag string, items []memory.Summary) string {
	return renderSummaries(tag, nil, items)
}
