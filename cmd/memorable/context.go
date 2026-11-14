package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/spf13/cobra"

	"github.com/constantine-kostikas/memorable/internal/config"
	"github.com/constantine-kostikas/memorable/internal/scope"
)

type contextView struct {
	Context  string `json:"context"`
	ReadOnly bool   `json:"read_only"`
	Project  string `json:"project,omitempty"`
	Schema   string `json:"schema"`
	Config   string `json:"config"`
}

func newContextCmd(d deps, flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "context",
		Short:   "Show which memory context the current directory uses",
		GroupID: groupSetup,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, path, err := loadConfig(d, flags.configPath)
			if err != nil {
				return err
			}
			cwd, err := d.cwd()
			if err != nil {
				return fmt.Errorf("reading working directory: %w", err)
			}
			res, err := resolveScope(cfg, []string{cwd}, d.getenv)
			if err != nil {
				return err
			}
			v := contextView{Context: res.Context, ReadOnly: res.ReadOnly, Schema: config.SchemaName(res.Context), Config: path}
			if res.Project != nil {
				v.Project = *res.Project
			}
			return printContext(cmd, flags, v)
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "add <path> <name>",
		Short: "Map a directory to a context (creating the context if needed)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return contextAdd(cmd, d, flags, args[0], args[1])
		},
	})
	return cmd
}

func printContext(cmd *cobra.Command, flags *rootFlags, v contextView) error {
	p, err := newPrinter(cmd, flags, nil)
	if err != nil {
		return err
	}
	if p.IsJSON() {
		return p.JSON(v)
	}
	mode := "read-write"
	if v.ReadOnly {
		mode = "read-only fallback"
	}
	p.Panel("Context", fmt.Sprintf("context  %s (%s)\nschema   %s\nproject  %s\nconfig   %s", v.Context, mode, v.Schema, v.Project, v.Config))
	return nil
}

func contextAdd(cmd *cobra.Command, d deps, flags *rootFlags, path, name string) error {
	cfgPath, err := resolveConfigPath(flags.configPath, d.getenv)
	if err != nil {
		return err
	}
	cfg, err := config.Load(cfgPath, d.getenv)
	if err != nil {
		return err //nolint:wrapcheck // already contextual
	}
	abs, err := config.ExpandPath(path)
	if err == nil {
		abs, err = filepath.Abs(abs)
	}
	if err != nil {
		return fmt.Errorf("resolving %s: %w", path, err)
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return usageError{fmt.Errorf("%s is not a directory", abs)}
	}
	ctx := cfg.Contexts[name]
	if !slices.Contains(ctx.Paths, abs) {
		ctx.Paths = append(ctx.Paths, abs)
	}
	cfg.Contexts[name] = ctx
	if err := cfg.Validate(); err != nil {
		return usageError{err}
	}
	if err := config.Save(cfgPath, *cfg); err != nil {
		return err //nolint:wrapcheck // already contextual
	}
	p, err := newPrinter(cmd, flags, nil)
	if err != nil {
		return err
	}
	p.Success(fmt.Sprintf("%s now maps to context %q (schema %s)", abs, name, config.SchemaName(name)))
	if _, err := scope.Resolve(cfg, []string{abs}); errors.Is(err, scope.ErrNoContext) {
		p.Warn("the path still resolves to no context; check overlapping paths")
	}
	if _, err := os.Stat(cfgPath); err == nil {
		p.Warn("run `memorable migrate` to create the schema for a new context")
	}
	return nil
}
