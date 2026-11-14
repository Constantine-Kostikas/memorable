// Package claudecfg edits Claude Code configuration: MCP registration, CLAUDE.md, and settings.json.
package claudecfg

import (
	"context"
	_ "embed"
	"fmt"
	"slices"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/pretty"
	"github.com/tidwall/sjson"
)

// Snippet is the CLAUDE.md block that teaches Claude to use memorable (docs/claude-md-snippet.md mirrors it).
//
//go:embed snippet.md
var Snippet string

const (
	startMarker = "<!-- memorable:start -->"
	endMarker   = "<!-- memorable:end -->"
	// HookCommand is the SessionStart hook command.
	HookCommand = "memorable hook session-start"
)

// Runner runs a command and returns its stdout.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// RegisterMCP registers memorable as a user-scope MCP server unless it already is.
func RegisterMCP(ctx context.Context, run Runner, binPath string) (bool, error) {
	if _, err := run(ctx, "claude", "mcp", "get", "memorable"); err == nil {
		return true, nil
	}
	if _, err := run(ctx, "claude", "mcp", "add", "--scope", "user", "memorable", "--", binPath, "serve"); err != nil {
		return false, fmt.Errorf("registering the MCP server: %w", err)
	}
	return false, nil
}

// UpsertClaudeMD places snippet between memorable markers, replacing an existing block or appending one.
func UpsertClaudeMD(existing, snippet string) string {
	block := startMarker + "\n" + strings.TrimSpace(snippet) + "\n" + endMarker
	start := strings.Index(existing, startMarker)
	end := strings.Index(existing, endMarker)
	if start >= 0 && end > start {
		return existing[:start] + block + existing[end+len(endMarker):]
	}
	if strings.TrimSpace(existing) == "" {
		return block + "\n"
	}
	return strings.TrimRight(existing, "\n") + "\n\n" + block + "\n"
}

func normalizeJSON(in []byte) ([]byte, error) {
	if len(strings.TrimSpace(string(in))) == 0 {
		return []byte("{}"), nil
	}
	if !gjson.ValidBytes(in) {
		return nil, fmt.Errorf("settings.json is not valid JSON")
	}
	return in, nil
}

func addToArray(doc []byte, path string, items []string) ([]byte, error) {
	if len(items) == 0 {
		return doc, nil
	}
	var current []string
	for _, v := range gjson.GetBytes(doc, path).Array() {
		current = append(current, v.String())
	}
	changed := false
	for _, it := range items {
		if !slices.Contains(current, it) {
			current = append(current, it)
			changed = true
		}
	}
	if !changed && gjson.GetBytes(doc, path).Exists() {
		return doc, nil
	}
	out, err := sjson.SetBytes(doc, path, current)
	if err != nil {
		return nil, fmt.Errorf("updating %s: %w", path, err)
	}
	return out, nil
}

// MergePermissions adds rules to permissions.allow, ask, and deny, keeping existing keys and their order.
func MergePermissions(settings []byte, allow, ask, deny []string) ([]byte, error) {
	doc, err := normalizeJSON(settings)
	if err != nil {
		return nil, err
	}
	for path, items := range map[string][]string{"permissions.allow": allow, "permissions.ask": ask, "permissions.deny": deny} {
		if doc, err = addToArray(doc, path, items); err != nil {
			return nil, err
		}
	}
	return pretty.PrettyOptions(doc, &pretty.Options{Indent: "  ", Width: 120}), nil
}

// AddSessionStartHook registers command as a SessionStart hook unless it already is.
func AddSessionStartHook(settings []byte, command string) ([]byte, error) {
	doc, err := normalizeJSON(settings)
	if err != nil {
		return nil, err
	}
	for _, entry := range gjson.GetBytes(doc, "hooks.SessionStart").Array() {
		for _, h := range entry.Get("hooks").Array() {
			if h.Get("command").String() == command {
				return pretty.PrettyOptions(doc, &pretty.Options{Indent: "  ", Width: 120}), nil
			}
		}
	}
	entry := map[string]any{"hooks": []map[string]string{{"type": "command", "command": command}}}
	out, err := sjson.SetBytes(doc, "hooks.SessionStart.-1", entry)
	if err != nil {
		return nil, fmt.Errorf("adding hook: %w", err)
	}
	return pretty.PrettyOptions(out, &pretty.Options{Indent: "  ", Width: 120}), nil
}

// Change is a planned file edit.
type Change struct {
	Path   string
	Before string
	After  string
}

// Diff renders a line diff of c, or "" when nothing changes.
func Diff(c Change) string {
	if c.Before == c.After {
		return ""
	}
	a := strings.Split(strings.TrimSuffix(c.Before, "\n"), "\n")
	b := strings.Split(strings.TrimSuffix(c.After, "\n"), "\n")
	if c.Before == "" {
		a = nil
	}
	// Longest common subsequence table; config files are small.
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s\n", c.Path, c.Path)
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			out.WriteString(" " + a[i] + "\n")
			i++
			j++
		case j < len(b) && (i == len(a) || lcs[i][j+1] >= lcs[i+1][j]):
			out.WriteString("+" + b[j] + "\n")
			j++
		default:
			out.WriteString("-" + a[i] + "\n")
			i++
		}
	}
	return out.String()
}

// Rules returns the recommended Claude Code permission rules; credentialsPath is denied to Read.
func Rules(credentialsPath string) (allow, ask, deny []string) {
	for _, t := range []string{"search", "get", "list", "top", "history", "review", "write", "update", "feedback", "link"} {
		allow = append(allow, "mcp__memorable__memory_"+t)
	}
	ask = []string{"mcp__memorable__memory_delete", "mcp__memorable__memory_restore"}
	deny = []string{
		"Bash(memorable purge:*)", "Bash(memorable approve:*)", "Bash(memorable profile:*)",
		"Bash(docker exec memorable-postgres:*)", "Bash(docker inspect memorable-postgres:*)", "Bash(psql:*)",
		"Read(" + credentialsPath + ")",
	}
	return allow, ask, deny
}
