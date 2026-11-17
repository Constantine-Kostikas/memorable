package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

// withApp builds the session for the current directory, runs fn, and closes it.
func withApp(cmd *cobra.Command, d deps, flags *rootFlags, fn func(context.Context, *app) error) error {
	cwd, err := d.cwd()
	if err != nil {
		return fmt.Errorf("reading working directory: %w", err)
	}
	a, err := d.build(cmd.Context(), flags.configPath, []string{cwd}, newLogger(cmd.ErrOrStderr(), d.getenv))
	if err != nil {
		return err
	}
	defer a.Close()
	return fn(cmd.Context(), a)
}

func parseTypeFlags(vals []string) ([]memory.Type, error) {
	out := make([]memory.Type, 0, len(vals))
	for _, v := range vals {
		t, err := memory.ParseType(v)
		if err != nil {
			return nil, usageError{err}
		}
		out = append(out, t)
	}
	return out, nil
}

func newSearchCmd(d deps, flags *rootFlags) *cobra.Command {
	var k int
	var tags, tagsAny, types []string
	var all bool
	cmd := &cobra.Command{
		Use:     "search <query>",
		Short:   "Hybrid semantic and keyword search",
		Example: "  memorable search \"database port\"\n  memorable search postgres --tag infra -k 10 -o json",
		GroupID: groupMemory,
		Args:    argsUsage(cobra.MinimumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := newPrinter(cmd, flags, d.getenv)
			if err != nil {
				return err
			}
			typ, err := parseTypeFlags(types)
			if err != nil {
				return err
			}
			return withApp(cmd, d, flags, func(ctx context.Context, a *app) error {
				res, err := a.svc.Search(ctx, memory.SearchRequest{Query: strings.Join(args, " "), K: k, Tags: tags, TagsAny: tagsAny, Types: typ, AllProjects: all, Origin: memory.OriginCLI})
				if err != nil {
					return err //nolint:wrapcheck // shown to the user as is
				}
				v := searchView{SearchID: res.SearchID.String(), Results: toSummaryViews(res.Results)}
				if p.IsJSON() {
					return p.JSON(v)
				}
				if len(v.Results) == 0 {
					p.Warn("no relevant memories")
					return nil
				}
				h, rows := summaryRows(v.Results, true)
				p.Table(h, rows)
				return nil
			})
		},
	}
	cmd.Flags().IntVarP(&k, "k", "k", memory.DefaultK, "number of results")
	cmd.Flags().StringSliceVar(&tags, "tag", nil, "require tag (repeatable)")
	cmd.Flags().StringSliceVar(&tagsAny, "any-tag", nil, "match any of these tags (repeatable)")
	cmd.Flags().StringSliceVar(&types, "type", nil, "user, feedback, project, or reference")
	cmd.Flags().BoolVar(&all, "all-projects", false, "include other projects")
	return cmd
}

func newGetCmd(d deps, flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "get <id|slug>...",
		Short:   "Show full memories",
		Example: "  memorable get infra/ports\n  memorable get 0193a5b2-7c4d-7e8f-9a0b-1c2d3e4f5a6b -o json",
		GroupID: groupMemory,
		Args:    argsUsage(cobra.RangeArgs(1, memory.MaxBatch)),
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
				views := make([]memoryView, 0, len(fetched))
				for _, f := range fetched {
					if f.Err != nil {
						if !p.IsJSON() {
							p.Error(fmt.Sprintf("%s: %v", f.Ref, f.Err))
						}
						continue
					}
					views = append(views, toMemoryView(f.Memory))
				}
				if p.IsJSON() {
					return p.JSON(views)
				}
				for _, v := range views {
					meta := fmt.Sprintf("%s · %s · %s · %s · v%d", v.Type, v.Scope, v.Status, v.Source, v.Version)
					if v.Slug != "" {
						meta = v.Slug + " · " + meta
					}
					p.Panel(v.Title, p.Muted(v.ID+"\n"+meta)+"\n\n"+v.Content)
				}
				return nil
			})
		},
	}
}

func newListCmd(d deps, flags *rootFlags) *cobra.Command {
	var tags, tagsAny, types, statuses []string
	var all bool
	var limit int
	var cursor string
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "Browse memories",
		Example: "  memorable list --status flagged\n  memorable list --tag infra --all-projects",
		GroupID: groupMemory,
		Args:    argsUsage(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := newPrinter(cmd, flags, d.getenv)
			if err != nil {
				return err
			}
			typ, err := parseTypeFlags(types)
			if err != nil {
				return err
			}
			req := memory.ListRequest{Tags: tags, TagsAny: tagsAny, Types: typ, AllProjects: all, Limit: limit}
			for _, s := range statuses {
				st, err := memory.ParseStatus(s)
				if err != nil {
					return usageError{err}
				}
				req.Statuses = append(req.Statuses, st)
			}
			if cursor != "" {
				c, err := uuid.Parse(cursor)
				if err != nil {
					return usageError{fmt.Errorf("invalid --cursor: %w", err)}
				}
				req.Cursor = &c
			}
			return withApp(cmd, d, flags, func(ctx context.Context, a *app) error {
				res, err := a.svc.List(ctx, req)
				if err != nil {
					return err //nolint:wrapcheck // shown to the user as is
				}
				items := toSummaryViews(res.Items)
				if p.IsJSON() {
					next := ""
					if res.Next != nil {
						next = res.Next.String()
					}
					return p.JSON(map[string]any{"items": items, "next_cursor": next})
				}
				h, rows := summaryRows(items, false)
				p.Table(h, rows)
				if res.Next != nil {
					p.Line(p.Muted("more: --cursor " + res.Next.String()))
				}
				return nil
			})
		},
	}
	cmd.Flags().StringSliceVar(&tags, "tag", nil, "require tag (repeatable)")
	cmd.Flags().StringSliceVar(&tagsAny, "any-tag", nil, "match any of these tags (repeatable)")
	cmd.Flags().StringSliceVar(&types, "type", nil, "user, feedback, project, or reference")
	cmd.Flags().StringSliceVar(&statuses, "status", nil, "active, unverified, flagged, superseded, or deleted")
	cmd.Flags().BoolVar(&all, "all-projects", false, "include other projects")
	cmd.Flags().IntVar(&limit, "limit", 20, "page size (max 100)")
	cmd.Flags().StringVar(&cursor, "cursor", "", "continue from a previous page")
	return cmd
}

func newTopCmd(d deps, flags *rootFlags) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:     "top",
		Short:   "Pinned and most used memories for this project",
		GroupID: groupMemory,
		Args:    argsUsage(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := newPrinter(cmd, flags, d.getenv)
			if err != nil {
				return err
			}
			return withApp(cmd, d, flags, func(ctx context.Context, a *app) error {
				items, err := a.svc.Top(ctx, limit)
				if err != nil {
					return err //nolint:wrapcheck // shown to the user as is
				}
				views := toSummaryViews(items)
				if p.IsJSON() {
					return p.JSON(views)
				}
				h, rows := summaryRows(views, true)
				p.Table(h, rows)
				return nil
			})
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 10, "number of memories (max 20)")
	return cmd
}

type tagCountView struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

func newTagsCmd(d deps, flags *rootFlags) *cobra.Command {
	var all bool
	var limit int
	cmd := &cobra.Command{
		Use:     "tags",
		Short:   "List the tags in use with counts",
		Example: "  memorable tags\n  memorable tags --all-projects -o json",
		GroupID: groupMemory,
		Args:    argsUsage(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := newPrinter(cmd, flags, d.getenv)
			if err != nil {
				return err
			}
			return withApp(cmd, d, flags, func(ctx context.Context, a *app) error {
				counts, err := a.svc.TagCounts(ctx, all, limit)
				if err != nil {
					return err //nolint:wrapcheck // shown to the user as is
				}
				views := make([]tagCountView, 0, len(counts))
				for _, c := range counts {
					views = append(views, tagCountView{Tag: c.Tag, Count: c.Count})
				}
				if p.IsJSON() {
					return p.JSON(views)
				}
				for _, v := range views {
					p.Line(fmt.Sprintf("%s\t%d", v.Tag, v.Count))
				}
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&all, "all-projects", false, "include other projects")
	cmd.Flags().IntVar(&limit, "limit", memory.DefaultTagLimit, "number of tags (max 100)")
	return cmd
}

func newStatsCmd(d deps, flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "stats",
		Short:   "Counts per status, type, and project, plus embedding coverage",
		GroupID: groupMemory,
		Args:    argsUsage(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := newPrinter(cmd, flags, d.getenv)
			if err != nil {
				return err
			}
			return withApp(cmd, d, flags, func(ctx context.Context, a *app) error {
				st, err := a.store.Stats(ctx)
				if err != nil {
					return err //nolint:wrapcheck // shown to the user as is
				}
				if p.IsJSON() {
					return p.JSON(st)
				}
				p.Panel("memorable · "+a.res.Context, fmt.Sprintf("memories   %d\nembedded   %d\npending    %d embedding jobs\nrecall     %d prompts, %d memories shown, %d fetched (last %d days)",
					st.Total, st.Embedded, st.PendingJobs, st.Recall.Injections, st.Recall.Shown, st.Recall.Fetched, st.Recall.Days))
				for _, group := range []struct {
					name string
					m    map[string]int
				}{{"STATUS", st.ByStatus}, {"TYPE", st.ByType}, {"PROJECT", st.ByProject}} {
					keys := make([]string, 0, len(group.m))
					for k := range group.m {
						keys = append(keys, k)
					}
					sort.Strings(keys)
					rows := make([][]string, 0, len(keys))
					for _, k := range keys {
						rows = append(rows, []string{k, fmt.Sprint(group.m[k])})
					}
					p.Table([]string{group.name, "COUNT"}, rows)
				}
				return nil
			})
		},
	}
}
