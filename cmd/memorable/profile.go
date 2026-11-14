package main

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/constantine-kostikas/memorable/internal/embed"
	"github.com/constantine-kostikas/memorable/internal/memory"
	"github.com/constantine-kostikas/memorable/internal/scan"
)

// profileEmbedding computes an embedding for profile text; nil when Ollama is unavailable (text search still works).
func profileEmbedding(ctx context.Context, t *adminTarget, title, content string) ([]float32, string) {
	emb, err := embed.NewOllama(t.cfg.Embedding, nil)
	if err != nil {
		return nil, ""
	}
	vecs, err := emb.Embed(ctx, memory.EmbedDocument, []string{memory.DocumentText(title, content)})
	if err != nil {
		return nil, ""
	}
	return vecs[0], emb.Model()
}

func scanProfileText(title, content string, tags []string) error {
	sec, err := scan.NewSecretScanner()
	if err != nil {
		return err //nolint:wrapcheck // already contextual
	}
	text := title + "\n" + content
	for _, t := range tags {
		text += " " + t
	}
	if fs := sec.Scan(text); len(fs) > 0 {
		return fmt.Errorf("%w: %s", memory.ErrSecretDetected, fs[0].Rule)
	}
	return nil
}

func newProfileCmd(d deps, flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "profile",
		Short:   "Manage profile memories, readable from every context (admin)",
		GroupID: groupAdmin,
	}
	var contextName string
	cmd.PersistentFlags().StringVar(&contextName, "context", "", "context used to authenticate (default: current directory's)")

	var title, typ, content string
	var tags []string
	var pinned bool
	add := &cobra.Command{
		Use:     "add",
		Short:   "Add a profile memory",
		Example: "  memorable profile add --title \"Spelling\" --content \"Use US spelling.\" --pinned",
		Args:    argsUsage(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := newPrinter(cmd, flags, d.getenv)
			if err != nil {
				return err
			}
			if content == "" {
				edited, err := editInEditor(cmd, d, editorDocument(memory.Memory{Title: title, Type: memory.TypeUser, Tags: tags, Pinned: pinned}))
				if err != nil {
					return err
				}
				doc, err := parseEditorDocument(edited)
				if err != nil {
					return usageError{err}
				}
				title, typ, tags, pinned, content = doc.Title, doc.Type, doc.Tags, doc.Pinned, doc.Content
			}
			t, err := memory.ParseType(typ)
			if err != nil {
				return usageError{err}
			}
			n := memory.NewMemory{Title: title, Content: content, Type: t, Tags: tags, Source: memory.SourceUserStated, Pinned: pinned}
			if err := n.Validate(); err != nil {
				return usageError{err}
			}
			if err := scanProfileText(title, content, tags); err != nil {
				return err
			}
			target, err := openAdmin(cmd.Context(), cmd, d, flags, contextName)
			if err != nil {
				return err
			}
			defer target.Close()
			m := &memory.Memory{ID: uuid.Must(uuid.NewV7()), Owner: ownerName(d.getenv), Type: t, Title: title, Content: content,
				Tags: tags, Source: memory.SourceUserStated, Pinned: pinned, ContentHash: memory.ContentHash(title, content)}
			vec, model := profileEmbedding(cmd.Context(), target, title, content)
			if err := target.admin.ProfileInsert(cmd.Context(), m, vec, model); err != nil {
				return err //nolint:wrapcheck // shown to the user as is
			}
			p.Success("added profile memory " + m.ID.String())
			if vec == nil {
				p.Warn("Ollama unavailable: stored without an embedding; run `memorable reembed --all` later")
			}
			return nil
		},
	}
	add.Flags().StringVar(&title, "title", "", "title")
	add.Flags().StringVar(&typ, "type", "user", "user, feedback, project, or reference")
	add.Flags().StringVar(&content, "content", "", "content (opens $EDITOR when omitted)")
	add.Flags().StringSliceVar(&tags, "tag", nil, "tag (repeatable)")
	add.Flags().BoolVar(&pinned, "pinned", false, "always include at session start")

	list := &cobra.Command{
		Use:   "list",
		Short: "List profile memories",
		Args:  argsUsage(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := newPrinter(cmd, flags, d.getenv)
			if err != nil {
				return err
			}
			target, err := openAdmin(cmd.Context(), cmd, d, flags, contextName)
			if err != nil {
				return err
			}
			defer target.Close()
			mems, err := target.admin.ProfileList(cmd.Context())
			if err != nil {
				return err //nolint:wrapcheck // shown to the user as is
			}
			views := make([]memoryView, 0, len(mems))
			for _, m := range mems {
				views = append(views, toMemoryView(m))
			}
			if p.IsJSON() {
				return p.JSON(views)
			}
			rows := make([][]string, 0, len(views))
			for _, v := range views {
				rows = append(rows, []string{shortID(v.ID), truncate(v.Title, 40), v.Type, fmt.Sprint(v.Pinned), truncate(v.Content, 60)})
			}
			p.Table([]string{"ID", "TITLE", "TYPE", "PINNED", "CONTENT"}, rows)
			return nil
		},
	}

	edit := &cobra.Command{
		Use:   "edit <id>",
		Short: "Edit a profile memory in $EDITOR",
		Args:  argsUsage(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := newPrinter(cmd, flags, d.getenv)
			if err != nil {
				return err
			}
			id, err := parseUUIDArg(args[0])
			if err != nil {
				return err
			}
			target, err := openAdmin(cmd.Context(), cmd, d, flags, contextName)
			if err != nil {
				return err
			}
			defer target.Close()
			mems, err := target.admin.ProfileList(cmd.Context())
			if err != nil {
				return err //nolint:wrapcheck // shown to the user as is
			}
			var cur *memory.Memory
			for i := range mems {
				if mems[i].ID == id {
					cur = &mems[i]
				}
			}
			if cur == nil {
				return fmt.Errorf("profile memory %s: %w", id, memory.ErrNotFound)
			}
			edited, err := editInEditor(cmd, d, editorDocument(*cur))
			if err != nil {
				return err
			}
			doc, err := parseEditorDocument(edited)
			if err != nil {
				return usageError{err}
			}
			patch, err := patchFrom(*cur, doc)
			if err != nil {
				return err
			}
			if isEmptyPatch(patch) {
				p.Line("no changes")
				return nil
			}
			if err := scanProfileText(doc.Title, doc.Content, doc.Tags); err != nil {
				return err
			}
			var vec []float32
			model := ""
			if patch.Title != nil || patch.Content != nil {
				vec, model = profileEmbedding(cmd.Context(), target, doc.Title, doc.Content)
			}
			out, err := target.admin.ProfileUpdate(cmd.Context(), id, patch, vec, model)
			if err != nil {
				return err //nolint:wrapcheck // shown to the user as is
			}
			p.Success(fmt.Sprintf("updated profile memory %s (version %d)", out.ID, out.Version))
			return nil
		},
	}

	var yes bool
	del := &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a profile memory",
		Args:  argsUsage(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := newPrinter(cmd, flags, d.getenv)
			if err != nil {
				return err
			}
			id, err := parseUUIDArg(args[0])
			if err != nil {
				return err
			}
			if err := confirm(cmd, d, "Delete profile memory "+id.String()+"?", yes); err != nil {
				return err
			}
			target, err := openAdmin(cmd.Context(), cmd, d, flags, contextName)
			if err != nil {
				return err
			}
			defer target.Close()
			if err := target.admin.ProfileDelete(cmd.Context(), id); err != nil {
				return err //nolint:wrapcheck // shown to the user as is
			}
			p.Success("deleted profile memory " + id.String())
			return nil
		},
	}
	del.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation")

	cmd.AddCommand(add, list, edit, del)
	return cmd
}
