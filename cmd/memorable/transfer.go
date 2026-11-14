package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/constantine-kostikas/memorable/internal/backup"
	"github.com/constantine-kostikas/memorable/internal/memory"
	"github.com/constantine-kostikas/memorable/internal/scan"
	"github.com/constantine-kostikas/memorable/internal/transfer"
)

func newExportCmd(d deps, flags *rootFlags) *cobra.Command {
	var format, out string
	cmd := &cobra.Command{
		Use:     "export",
		Short:   "Export this context's memories as JSON or Markdown",
		Example: "  memorable export > memories.json\n  memorable export --format md --out ./memories",
		GroupID: groupMemory,
		Args:    argsUsage(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if format != "json" && format != "md" {
				return usageError{fmt.Errorf("--format must be json or md, got %q", format)}
			}
			if format == "md" && out == "" {
				return usageError{fmt.Errorf("--format md needs --out <directory>")}
			}
			return withApp(cmd, d, flags, func(ctx context.Context, a *app) error {
				owner := a.svc.Settings().Owner
				var n int
				var err error
				switch format {
				case "md":
					n, err = transfer.ExportMarkdown(ctx, a.store, owner, out)
				default:
					w := cmd.OutOrStdout()
					if out != "" {
						f, ferr := os.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600) //nolint:gosec // user-chosen output path
						if ferr != nil {
							return fmt.Errorf("creating %s: %w", out, ferr)
						}
						defer func() { _ = f.Close() }()
						w = f
					}
					n, err = transfer.ExportJSON(ctx, a.store, owner, a.res.Context, w)
				}
				if err != nil {
					return err //nolint:wrapcheck // shown to the user as is
				}
				if out != "" {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "exported %d memories to %s\n", n, out)
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&format, "format", "json", "json or md")
	cmd.Flags().StringVar(&out, "out", "", "output file (json) or directory (md); json defaults to stdout")
	return cmd
}

func newImportCmd(d deps, flags *rootFlags) *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "import <path>",
		Short: "Import a JSON export, a Markdown file, or a directory of Claude Code memory files",
		Example: "  memorable import ~/.claude/projects/-home-me-code-app/memory\n" +
			"  memorable import memories.json --dry-run",
		GroupID: groupMemory,
		Args:    argsUsage(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := newPrinter(cmd, flags, d.getenv)
			if err != nil {
				return err
			}
			items, skipped, err := transfer.ParsePath(args[0])
			if err != nil {
				return err //nolint:wrapcheck // shown to the user as is
			}
			for _, s := range skipped {
				p.Warn("skipped " + s)
			}
			if dryRun {
				rows := make([][]string, 0, len(items))
				for _, it := range items {
					slug := ""
					if it.Slug != nil {
						slug = *it.Slug
					}
					rows = append(rows, []string{slug, truncate(it.Title, 50), it.Type.String()})
				}
				if p.IsJSON() {
					return p.JSON(map[string]any{"would_import": len(items), "skipped": skipped})
				}
				p.Table([]string{"SLUG", "TITLE", "TYPE"}, rows)
				p.Line(fmt.Sprintf("%d memories would be imported (dry run)", len(items)))
				return nil
			}
			return withApp(cmd, d, flags, func(ctx context.Context, a *app) error {
				sec, err := scan.NewSecretScanner()
				if err != nil {
					return err //nolint:wrapcheck // shown to the user as is
				}
				bulk := memory.NewService(a.store, a.store, a.emb, sec, scan.Injection, a.svc.Settings(), memory.WithoutRateLimit())
				rep, err := transfer.Import(ctx, bulk, items)
				if err != nil {
					return err //nolint:wrapcheck // shown to the user as is
				}
				if p.IsJSON() {
					return p.JSON(rep)
				}
				rows := make([][]string, 0, len(rep.Rows))
				for _, r := range rep.Rows {
					rows = append(rows, []string{r.Outcome.String(), truncate(r.Title, 40), truncate(r.Reason, 60)})
				}
				p.Table([]string{"OUTCOME", "TITLE", "REASON"}, rows)
				p.Success(fmt.Sprintf("created %d, duplicates %d, needs decision %d, quarantined %d, rejected %d",
					rep.Counts[memory.OutcomeCreated], rep.Counts[memory.OutcomeDuplicate], rep.Counts[memory.OutcomeNeedsDecision],
					rep.Counts[memory.OutcomeQuarantined], rep.Counts[memory.OutcomeRejected]))
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be imported")
	return cmd
}

func execRunner(ctx context.Context, w io.Writer, name string, args ...string) error {
	c := exec.CommandContext(ctx, name, args...) //nolint:gosec // fixed binary, arguments without a shell
	var stderr bytes.Buffer
	c.Stdout, c.Stderr = w, &stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func newBackupCmd(d deps, flags *rootFlags) *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:     "backup",
		Short:   "Dump the database with pg_dump inside the container and keep the newest dumps",
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
			if dir == "" {
				if dir, err = backup.DefaultDir(d.getenv); err != nil {
					return err //nolint:wrapcheck // shown to the user as is
				}
			}
			path, err := backup.Backup(cmd.Context(), execRunner, backup.Options{Container: cfg.Database.Container,
				User: cfg.Database.AdminUser, Database: cfg.Database.Name, Dir: dir, Keep: cfg.Retention.BackupsKeep}, time.Now())
			if err != nil {
				return err //nolint:wrapcheck // shown to the user as is
			}
			if p.IsJSON() {
				return p.JSON(map[string]string{"path": path})
			}
			p.Success("backup written to " + path)
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "backup directory (default ~/.local/share/memorable/backups)")
	return cmd
}
