package transfer

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

// ParseJSON reads a memorable JSON export, keeping live memories as imports.
func ParseJSON(r io.Reader) ([]memory.NewMemory, error) {
	var f exportFile
	if err := json.NewDecoder(r).Decode(&f); err != nil {
		return nil, fmt.Errorf("parsing export: %w", err)
	}
	if f.Format != exportFormat || f.Version != exportVersion {
		return nil, fmt.Errorf("not a memorable export (format %q version %d)", f.Format, f.Version)
	}
	out := make([]memory.NewMemory, 0, len(f.Memories))
	for _, e := range f.Memories {
		if e.Status == memory.StatusDeleted.String() || e.Status == memory.StatusSuperseded.String() {
			continue
		}
		typ, err := memory.ParseType(e.Type)
		if err != nil {
			return nil, fmt.Errorf("memory %s: %w", e.ID, err)
		}
		n := memory.NewMemory{Title: e.Title, Content: e.Content, Type: typ, Tags: e.Tags, Source: memory.SourceImported,
			Global: e.Project == "", Pinned: e.Pinned, ExpiresAt: e.ExpiresAt}
		if e.Slug != "" {
			slug := e.Slug
			n.Slug = &slug
		}
		out = append(out, n)
	}
	return out, nil
}

var (
	slugInvalid = regexp.MustCompile(`[^a-z0-9/-]+`)
	errNoFront  = errors.New("missing --- front matter")
)

// frontMatter parses "key: value" lines, flattening one level of nesting (metadata.type becomes type).
func frontMatter(r io.Reader) (map[string]string, string, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	if !sc.Scan() || strings.TrimSpace(sc.Text()) != "---" {
		return nil, "", errNoFront
	}
	fields := map[string]string{}
	closed := false
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "---" {
			closed = true
			break
		}
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.Trim(strings.TrimSpace(value), `"'`)
		if _, exists := fields[key]; !exists || value != "" {
			fields[key] = value
		}
	}
	if !closed {
		return nil, "", errNoFront
	}
	var body strings.Builder
	for sc.Scan() {
		body.WriteString(sc.Text())
		body.WriteString("\n")
	}
	if err := sc.Err(); err != nil {
		return nil, "", fmt.Errorf("reading file: %w", err)
	}
	return fields, strings.TrimSpace(body.String()), nil
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// ParseClaudeMemoryFile reads a Claude Code memory file or a memorable Markdown export.
func ParseClaudeMemoryFile(r io.Reader, fileName string) (memory.NewMemory, error) {
	fields, body, err := frontMatter(r)
	if err != nil {
		return memory.NewMemory{}, fmt.Errorf("%s: %w", fileName, err)
	}
	name := fields["name"]
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(fileName), filepath.Ext(fileName))
	}
	title := fields["description"]
	if title == "" {
		title = name
	}
	typ, err := memory.ParseType(fields["type"])
	if err != nil {
		typ = memory.TypeReference
	}
	n := memory.NewMemory{Title: truncateRunes(oneLine(title), memory.MaxTitleRunes), Content: body, Type: typ,
		Source: memory.SourceImported, Pinned: fields["pinned"] == "true", Global: fields["scope"] == "global"}
	if tags := strings.Trim(fields["tags"], "[]"); tags != "" {
		for _, t := range strings.Split(tags, ",") {
			if t = strings.TrimSpace(t); t != "" {
				n.Tags = append(n.Tags, t)
			}
		}
	}
	slug := strings.Trim(slugInvalid.ReplaceAllString(strings.ToLower(name), "-"), "-/")
	if len(slug) >= 2 && len(slug) <= 80 {
		n.Slug = &slug
	}
	return n, nil
}

// ParsePath reads a JSON export file, one Markdown file, or a directory of Markdown files (MEMORY.md is skipped).
// Files that cannot be parsed are reported in skipped.
func ParsePath(path string) ([]memory.NewMemory, []string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if !info.IsDir() {
		f, err := os.Open(path) //nolint:gosec // a path the user passed explicitly
		if err != nil {
			return nil, nil, fmt.Errorf("opening %s: %w", path, err)
		}
		defer func() { _ = f.Close() }()
		if strings.EqualFold(filepath.Ext(path), ".json") {
			items, err := ParseJSON(f)
			return items, nil, err
		}
		n, err := ParseClaudeMemoryFile(f, path)
		if err != nil {
			return nil, []string{err.Error()}, nil
		}
		return []memory.NewMemory{n}, nil, nil
	}
	matches, err := filepath.Glob(filepath.Join(path, "*.md"))
	if err != nil {
		return nil, nil, fmt.Errorf("listing %s: %w", path, err)
	}
	sort.Strings(matches)
	var items []memory.NewMemory
	var skipped []string
	for _, m := range matches {
		if strings.EqualFold(filepath.Base(m), "MEMORY.md") {
			continue
		}
		f, err := os.Open(m) //nolint:gosec // files inside a directory the user passed
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s: %v", m, err))
			continue
		}
		n, err := ParseClaudeMemoryFile(f, m)
		_ = f.Close()
		if err != nil {
			skipped = append(skipped, err.Error())
			continue
		}
		items = append(items, n)
	}
	return items, skipped, nil
}

// Row is the result of importing one item.
type Row struct {
	Title   string
	Outcome memory.Outcome
	Reason  string
	ID      string
}

// Report summarizes an import.
type Report struct {
	Rows   []Row
	Counts map[memory.Outcome]int
}

// Import writes items through the service in batches, so every item is validated and scanned.
func Import(ctx context.Context, svc *memory.Service, items []memory.NewMemory) (Report, error) {
	rep := Report{Counts: map[memory.Outcome]int{}}
	for start := 0; start < len(items); start += memory.MaxBatch {
		batch := items[start:min(start+memory.MaxBatch, len(items))]
		results, err := svc.Write(ctx, batch)
		if err != nil {
			return rep, fmt.Errorf("importing items %d to %d: %w", start+1, start+len(batch), err)
		}
		for i, r := range results {
			row := Row{Title: batch[i].Title, Outcome: r.Outcome, Reason: r.Reason} //nolint:gosec // Write returns one result per item
			if r.Memory != nil {
				row.ID = r.Memory.ID.String()
			}
			rep.Rows = append(rep.Rows, row)
			rep.Counts[r.Outcome]++
		}
	}
	return rep, nil
}
