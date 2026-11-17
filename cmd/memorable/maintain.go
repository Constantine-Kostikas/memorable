package main

import (
	"context"
	"errors"
	"fmt"

	"charm.land/huh/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"

	"github.com/constantine-kostikas/memorable/internal/config"
	"github.com/constantine-kostikas/memorable/internal/memory"
	"github.com/constantine-kostikas/memorable/internal/store"
	"github.com/constantine-kostikas/memorable/internal/worker"
)

func parseUUIDArg(s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, usageError{fmt.Errorf("%q is not a memory id", s)}
	}
	return id, nil
}

func newMigrateCmd(d deps, flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "migrate",
		Short:   "Create roles and schemas and apply migrations (admin)",
		GroupID: groupAdmin,
		Args:    argsUsage(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := newPrinter(cmd, flags, d.getenv)
			if err != nil {
				return err
			}
			cfg, _, err := loadConfig(d, flags.configPath)
			if err != nil {
				return err
			}
			pw, err := d.adminPassword(cmd)
			if err != nil {
				return err
			}
			rows, err := migrateAll(cmd.Context(), d, cfg, pw)
			if err != nil {
				return err
			}
			if p.IsJSON() {
				return p.JSON(rows)
			}
			table := make([][]string, 0, len(rows))
			for _, r := range rows {
				table = append(table, []string{r.Schema, fmt.Sprint(r.Applied), fmt.Sprint(r.Version)})
			}
			p.Table([]string{"SCHEMA", "APPLIED", "VERSION"}, table)
			p.Success("database is up to date")
			return nil
		},
	}
}

type migrateRow struct {
	Schema  string  `json:"schema"`
	Applied []int64 `json:"applied"`
	Version int64   `json:"version"`
}

// migrateAll bootstraps roles and schemas and migrates every schema; it creates and stores the app password if missing.
func migrateAll(ctx context.Context, d deps, cfg *config.Config, adminPassword string) ([]migrateRow, error) {
	secrets, err := secretStore(d.getenv)
	if err != nil {
		return nil, err
	}
	account := appAccount(cfg.Database)
	appPW, err := secrets.Get(account)
	if errors.Is(err, memory.ErrNotFound) {
		appPW = generatePassword()
		if err := secrets.Set(account, appPW); err != nil {
			return nil, err //nolint:wrapcheck // already contextual
		}
	} else if err != nil {
		return nil, err //nolint:wrapcheck // already contextual
	}
	dsn := store.DSN(cfg.Database, cfg.Database.AdminUser, adminPassword)
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("%w: connecting as admin: %w", memory.ErrUnavailable, err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if err := store.Bootstrap(ctx, conn, cfg, appPW); err != nil {
		return nil, err //nolint:wrapcheck // already contextual
	}
	type target struct {
		schema string
		kind   store.SchemaKind
	}
	targets := []target{{store.ProfileSchema, store.SchemaProfile}}
	for _, name := range cfg.ContextNames() {
		targets = append(targets, target{config.SchemaName(name), store.SchemaContext})
	}
	rows := make([]migrateRow, 0, len(targets))
	for _, t := range targets {
		applied, err := store.Migrate(ctx, dsn, t.schema, t.kind, cfg.Embedding.Dimensions, cfg.Database.AppUser)
		if err != nil {
			return rows, err //nolint:wrapcheck // already contextual
		}
		if applied == nil {
			applied = []int64{}
		}
		rows = append(rows, migrateRow{Schema: t.schema, Applied: applied, Version: store.LatestVersion(t.kind)})
	}
	return rows, nil
}

func newApproveCmd(d deps, flags *rootFlags) *cobra.Command {
	var list, yes bool
	var contextName string
	cmd := &cobra.Command{
		Use:     "approve [id]",
		Short:   "Review quarantined memories and release one (admin)",
		Example: "  memorable approve --list\n  memorable approve 0193a5b2-7c4d-7e8f-9a0b-1c2d3e4f5a6b",
		GroupID: groupAdmin,
		Args:    argsUsage(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := newPrinter(cmd, flags, d.getenv)
			if err != nil {
				return err
			}
			if !list && len(args) == 0 {
				return usageError{errors.New("pass a memory id, or --list")}
			}
			var id uuid.UUID
			if len(args) == 1 {
				if id, err = parseUUIDArg(args[0]); err != nil {
					return err
				}
			}
			t, err := openAdmin(cmd.Context(), cmd, d, flags, contextName)
			if err != nil {
				return err
			}
			defer t.Close()
			mems, err := t.ops.QuarantinedList(cmd.Context())
			if err != nil {
				return err //nolint:wrapcheck // shown to the user as is
			}
			if list {
				if p.IsJSON() {
					views := make([]memoryView, 0, len(mems))
					for _, m := range mems {
						views = append(views, toMemoryView(m))
					}
					return p.JSON(views)
				}
				if len(mems) == 0 {
					p.Success("nothing in quarantine")
				}
				for _, m := range mems {
					p.Panel(m.Title, p.Muted(m.ID.String()+" · "+m.Source.String())+"\n\n"+m.Content)
				}
				return nil
			}
			for _, m := range mems {
				if m.ID == id {
					p.Panel(m.Title, m.Content)
					if err := confirm(cmd, d, "Release this memory from quarantine?", yes); err != nil {
						return err
					}
					if err := t.ops.Approve(cmd.Context(), id); err != nil {
						return err //nolint:wrapcheck // shown to the user as is
					}
					p.Success("approved " + id.String())
					return nil
				}
			}
			return fmt.Errorf("memory %s is not quarantined in context %s", id, t.context)
		},
	}
	cmd.Flags().BoolVar(&list, "list", false, "show quarantined memories")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation")
	cmd.Flags().StringVar(&contextName, "context", "", "context to act on (default: current directory's)")
	return cmd
}

func newPurgeCmd(d deps, flags *rootFlags) *cobra.Command {
	var yes bool
	var contextName string
	cmd := &cobra.Command{
		Use:     "purge <id>",
		Short:   "Irreversibly erase a memory, its history, and its references (admin)",
		GroupID: groupAdmin,
		Args:    argsUsage(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := newPrinter(cmd, flags, d.getenv)
			if err != nil {
				return err
			}
			id, err := parseUUIDArg(args[0])
			if err != nil {
				return err
			}
			if err := confirm(cmd, d, "Permanently erase memory "+id.String()+"? This cannot be undone.", yes); err != nil {
				return err
			}
			t, err := openAdmin(cmd.Context(), cmd, d, flags, contextName)
			if err != nil {
				return err
			}
			defer t.Close()
			if err := t.ops.Purge(cmd.Context(), id); err != nil {
				return err //nolint:wrapcheck // shown to the user as is
			}
			p.Success("purged " + id.String())
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation")
	cmd.Flags().StringVar(&contextName, "context", "", "context to act on (default: current directory's)")
	return cmd
}

func newPruneCmd(d deps, flags *rootFlags) *cobra.Command {
	var contextName string
	cmd := &cobra.Command{
		Use:     "prune",
		Short:   "Delete access events older than the retention period (admin)",
		GroupID: groupAdmin,
		Args:    argsUsage(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := newPrinter(cmd, flags, d.getenv)
			if err != nil {
				return err
			}
			t, err := openAdmin(cmd.Context(), cmd, d, flags, contextName)
			if err != nil {
				return err
			}
			defer t.Close()
			n, err := t.ops.Prune(cmd.Context())
			if err != nil {
				return err //nolint:wrapcheck // shown to the user as is
			}
			p.Success(fmt.Sprintf("deleted %d access events older than %d days", n, t.cfg.Retention.AccessEventsDays))
			return nil
		},
	}
	cmd.Flags().StringVar(&contextName, "context", "", "context to act on (default: current directory's)")
	return cmd
}

func newReviewCmd(d deps, flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "review",
		Short:   "Work through flagged, expired, stale, and unverified memories",
		GroupID: groupMemory,
		Args:    argsUsage(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := newPrinter(cmd, flags, d.getenv)
			if err != nil {
				return err
			}
			return withApp(cmd, d, flags, func(ctx context.Context, a *app) error {
				q, err := a.svc.Review(ctx)
				if err != nil {
					return err //nolint:wrapcheck // shown to the user as is
				}
				buckets := []struct {
					name string
					mems []memory.Memory
				}{{"flagged", q.Flagged}, {"expired", q.Expired}, {"stale", q.Stale}, {"unverified", q.Unverified}}
				if p.IsJSON() {
					out := map[string]any{"quarantined": q.QuarantinedCount, "contradictions": len(q.Conflicts)}
					for _, b := range buckets {
						views := make([]memoryView, 0, len(b.mems))
						for _, m := range b.mems {
							views = append(views, toMemoryView(m))
						}
						out[b.name] = views
					}
					return p.JSON(out)
				}
				rows := [][]string{}
				for _, b := range buckets {
					rows = append(rows, []string{b.name, fmt.Sprint(len(b.mems))})
				}
				rows = append(rows, []string{"contradictions", fmt.Sprint(len(q.Conflicts))},
					[]string{"quarantined", fmt.Sprint(q.QuarantinedCount)})
				p.Table([]string{"QUEUE", "COUNT"}, rows)
				if q.QuarantinedCount > 0 {
					p.Warn("quarantined memories need `memorable approve --list`")
				}
				if !d.isTTY(cmd) {
					return nil
				}
				for _, b := range buckets {
					for _, m := range b.mems {
						if err := reviewOne(cmd, p, a, b.name, m); errors.Is(err, errStopReview) {
							return nil
						} else if err != nil {
							return err
						}
					}
				}
				return nil
			})
		},
	}
}

func reviewOne(cmd *cobra.Command, p interface{ Panel(string, string) }, a *app, bucket string, m memory.Memory) error {
	p.Panel(fmt.Sprintf("[%s] %s", bucket, m.Title), m.Content)
	choice := "keep"
	opts := []huh.Option[string]{huh.NewOption("Keep as is", "keep"), huh.NewOption("Delete", "delete")}
	if m.Status == memory.StatusFlagged || m.Status == memory.StatusUnverified {
		opts = append(opts, huh.NewOption("Mark active (verified)", "activate"))
	}
	opts = append(opts, huh.NewOption("Stop reviewing", "stop"))
	if err := runForm(cmd.InOrStdin(), cmd.ErrOrStderr(), huh.NewSelect[string]().Title("Action").Options(opts...).Value(&choice)); err != nil {
		return err
	}
	ctx := cmd.Context()
	switch choice {
	case "delete":
		return a.svc.Delete(ctx, m.ID) //nolint:wrapcheck // shown to the user as is
	case "activate":
		return a.svc.Activate(ctx, m.ID) //nolint:wrapcheck // shown to the user as is
	case "stop":
		return errStopReview
	}
	return nil
}

var errStopReview = errors.New("review stopped")

func newReembedCmd(d deps, flags *rootFlags) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:     "reembed",
		Short:   "Compute pending embeddings, or re-embed everything after a model change",
		Example: "  memorable reembed            # process pending jobs\n  memorable reembed --all      # after changing the model or dimensions",
		GroupID: groupAdmin,
		Args:    argsUsage(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := newPrinter(cmd, flags, d.getenv)
			if err != nil {
				return err
			}
			return withApp(cmd, d, flags, func(ctx context.Context, a *app) error {
				if all {
					if err := reembedAll(ctx, cmd, d, flags, p, a); err != nil {
						return err
					}
				} else if _, err := a.store.RequeueMissing(ctx); err != nil {
					return err //nolint:wrapcheck // shown to the user as is
				}
				w := worker.New(a.store, a.store, a.emb, newLogger(cmd.ErrOrStderr(), d.getenv))
				total := 0
				for {
					n, err := w.RunOnce(ctx)
					if err != nil {
						return err //nolint:wrapcheck // shown to the user as is
					}
					if n == 0 {
						break
					}
					total += n
				}
				p.Success(fmt.Sprintf("processed %d embedding jobs", total))
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "re-embed every memory (needs the admin password when dimensions change)")
	return cmd
}

func reembedAll(ctx context.Context, cmd *cobra.Command, d deps, flags *rootFlags, p interface{ Warn(string) }, a *app) error {
	current, err := a.store.EmbeddingDimensions(ctx)
	if err != nil {
		return err //nolint:wrapcheck // shown to the user as is
	}
	if current != a.cfg.Embedding.Dimensions {
		p.Warn(fmt.Sprintf("embedding column is vector(%d), config says %d: resizing (admin)", current, a.cfg.Embedding.Dimensions))
		t, err := openAdmin(ctx, cmd, d, flags, a.res.Context)
		if err != nil {
			return err
		}
		defer t.Close()
		schemas := []string{store.ProfileSchema}
		for _, name := range a.cfg.ContextNames() {
			schemas = append(schemas, config.SchemaName(name))
		}
		// Every context shares the configured model, so every schema is resized; other contexts
		// re-embed when their next session starts (serve requeues missing embeddings).
		for _, schema := range schemas {
			if err := store.ResizeEmbeddings(ctx, t.dsn, schema, a.cfg.Embedding.Dimensions); err != nil {
				return err //nolint:wrapcheck // shown to the user as is
			}
		}
		if err := reembedProfile(ctx, t.admin, a.emb); err != nil {
			return err
		}
	}
	_, err = a.store.EnqueueAll(ctx)
	return err //nolint:wrapcheck // shown to the user as is
}

func reembedProfile(ctx context.Context, admin *store.Admin, emb memory.Embedder) error {
	mems, err := admin.ProfileList(ctx)
	if err != nil {
		return err //nolint:wrapcheck // shown to the user as is
	}
	for _, m := range mems {
		vecs, err := emb.Embed(ctx, memory.EmbedDocument, []string{memory.DocumentText(m.Title, m.Tags, m.Content)})
		if err != nil {
			return err //nolint:wrapcheck // shown to the user as is
		}
		if err := admin.ProfileSetEmbedding(ctx, m.ID, vecs[0], emb.Model()); err != nil {
			return err //nolint:wrapcheck // shown to the user as is
		}
	}
	return nil
}
