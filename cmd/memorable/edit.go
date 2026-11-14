package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

const frontMatterFence = "+++"

type editDoc struct {
	Title     string   `toml:"title"`
	Type      string   `toml:"type"`
	Tags      []string `toml:"tags"`
	Pinned    bool     `toml:"pinned"`
	ExpiresAt string   `toml:"expires_at"`
	Content   string   `toml:"-"`
}

func editorDocument(m memory.Memory) string {
	d := editDoc{Title: m.Title, Type: m.Type.String(), Tags: m.Tags, Pinned: m.Pinned}
	if d.Tags == nil {
		d.Tags = []string{}
	}
	if m.ExpiresAt != nil {
		d.ExpiresAt = m.ExpiresAt.UTC().Format(time.RFC3339)
	}
	var b bytes.Buffer
	b.WriteString(frontMatterFence + "\n")
	_ = toml.NewEncoder(&b).Encode(d)
	b.WriteString(frontMatterFence + "\n")
	b.WriteString(m.Content)
	b.WriteString("\n")
	return b.String()
}

func parseEditorDocument(s string) (editDoc, error) {
	lines := strings.SplitAfter(s, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != frontMatterFence {
		return editDoc{}, errors.New("the file must start with a +++ front matter block")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == frontMatterFence {
			end = i
			break
		}
	}
	if end < 0 {
		return editDoc{}, errors.New("the front matter block is not closed with +++")
	}
	var d editDoc
	if _, err := toml.Decode(strings.Join(lines[1:end], ""), &d); err != nil {
		return editDoc{}, fmt.Errorf("parsing front matter: %w", err)
	}
	d.Content = strings.TrimSuffix(strings.Join(lines[end+1:], ""), "\n")
	return d, nil
}

func patchFrom(m memory.Memory, d editDoc) (memory.Patch, error) {
	var p memory.Patch
	if d.Title != m.Title {
		p.Title = &d.Title
	}
	if d.Content != m.Content {
		p.Content = &d.Content
	}
	if !slices.Equal(d.Tags, m.Tags) && (len(d.Tags) != 0 || len(m.Tags) != 0) {
		p.Tags = &d.Tags
	}
	if d.Type != m.Type.String() {
		t, err := memory.ParseType(d.Type)
		if err != nil {
			return p, usageError{err}
		}
		p.Type = &t
	}
	if d.Pinned != m.Pinned {
		p.Pinned = &d.Pinned
	}
	cur := ""
	if m.ExpiresAt != nil {
		cur = m.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if d.ExpiresAt != cur {
		var exp *time.Time
		if d.ExpiresAt != "" {
			t, err := time.Parse(time.RFC3339, d.ExpiresAt)
			if err != nil {
				return p, usageError{fmt.Errorf("expires_at must be RFC 3339: %w", err)}
			}
			exp = &t
		}
		p.ExpiresAt = &exp
	}
	return p, nil
}

func isEmptyPatch(p memory.Patch) bool {
	return p.Title == nil && p.Content == nil && p.Tags == nil && p.Type == nil && p.Pinned == nil && p.ExpiresAt == nil
}

// editInEditor writes doc to a private temp file, opens $EDITOR, and returns the edited text.
func editInEditor(cmd *cobra.Command, d deps, doc string) (string, error) {
	f, err := os.CreateTemp("", "memorable-*.md")
	if err != nil {
		return "", fmt.Errorf("creating temp file: %w", err)
	}
	path := f.Name()
	defer func() { _ = os.Remove(path) }()
	if _, err := f.WriteString(doc); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("writing temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("writing temp file: %w", err)
	}
	editor := strings.Fields(d.getenv("EDITOR"))
	if len(editor) == 0 {
		editor = []string{"vi"}
	}
	//nolint:gosec // the user's own $EDITOR, arguments passed without a shell
	ed := exec.CommandContext(cmd.Context(), editor[0], append(editor[1:], path)...)
	ed.Stdin, ed.Stdout, ed.Stderr = cmd.InOrStdin(), cmd.ErrOrStderr(), cmd.ErrOrStderr()
	if err := ed.Run(); err != nil {
		return "", fmt.Errorf("running %s: %w", editor[0], err)
	}
	b, err := os.ReadFile(path) //nolint:gosec // our own temp file
	if err != nil {
		return "", fmt.Errorf("reading edited file: %w", err)
	}
	return string(b), nil
}

func newEditCmd(d deps, flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "edit <id|slug>",
		Short:   "Edit a memory in $EDITOR (rescanned and re-embedded)",
		GroupID: groupMemory,
		Args:    argsUsage(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := newPrinter(cmd, flags, d.getenv)
			if err != nil {
				return err
			}
			ids, slugs := splitRefs(args)
			return withApp(cmd, d, flags, func(ctx context.Context, a *app) error {
				fetched, err := a.svc.Get(ctx, memory.GetRequest{IDs: ids, Slugs: slugs, Passive: true})
				if err != nil {
					return err //nolint:wrapcheck // shown to the user as is
				}
				if fetched[0].Err != nil {
					return fetched[0].Err
				}
				m := fetched[0].Memory
				edited, err := editInEditor(cmd, d, editorDocument(m))
				if err != nil {
					return err
				}
				doc, err := parseEditorDocument(edited)
				if err != nil {
					return usageError{err}
				}
				patch, err := patchFrom(m, doc)
				if err != nil {
					return err
				}
				if isEmptyPatch(patch) {
					p.Line("no changes")
					return nil
				}
				out, err := a.svc.Update(ctx, m.ID, &m.Version, patch)
				if err != nil {
					return err //nolint:wrapcheck // shown to the user as is
				}
				p.Success(fmt.Sprintf("updated %s (version %d, %s)", out.ID, out.Version, out.Status))
				return nil
			})
		},
	}
}
