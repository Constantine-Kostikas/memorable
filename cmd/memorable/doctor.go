package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os/exec"
	"time"

	"github.com/spf13/cobra"

	"github.com/constantine-kostikas/memorable/internal/config"
	"github.com/constantine-kostikas/memorable/internal/doctor"
	"github.com/constantine-kostikas/memorable/internal/store"
)

func runDocker(ctx context.Context, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, "docker", args...).Output() //nolint:gosec // fixed binary, arguments without a shell
	if err != nil {
		return out, errors.New("docker: " + err.Error()) //nolint:err113 // shown to the user
	}
	return out, nil
}

func doctorDeps(d deps, flags *rootFlags) doctor.Deps {
	cfg, _, err := loadConfig(d, flags.configPath)
	deps := doctor.Deps{Config: cfg, ConfigErr: err, Docker: runDocker, HTTP: &http.Client{Timeout: 5 * time.Second},
		Dial: (&net.Dialer{}).DialContext}
	if err != nil {
		return deps
	}
	schema := store.ProfileSchema
	if names := cfg.ContextNames(); len(names) > 0 {
		schema = config.SchemaName(names[0])
	}
	if cwd, err := d.cwd(); err == nil {
		res, rerr := resolveScope(cfg, []string{cwd}, d.getenv)
		deps.ContextErr = rerr
		if rerr == nil {
			deps.ContextName = res.Context
			schema = config.SchemaName(res.Context)
			if res.ReadOnly {
				deps.ContextName += " (read-only fallback)"
			}
		}
	}
	deps.Password = func() (string, error) {
		s, err := secretStore(d.getenv)
		if err != nil {
			return "", err
		}
		return s.Get(appAccount(cfg.Database)) //nolint:wrapcheck // shown to the user
	}
	deps.Open = func(ctx context.Context, password string) (doctor.Probe, error) {
		return store.Open(ctx, cfg.Database, password, schema, ownerName(d.getenv)) //nolint:wrapcheck // shown to the user
	}
	return deps
}

func newDoctorCmd(d deps, flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "doctor",
		Short:   "Check config, database, and Ollama, with fixes for anything broken",
		GroupID: groupSetup,
		Args:    argsUsage(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := newPrinter(cmd, flags, d.getenv)
			if err != nil {
				return err
			}
			checks := doctor.Run(cmd.Context(), doctorDeps(d, flags))
			if p.IsJSON() {
				if err := p.JSON(checks); err != nil {
					return err
				}
			} else {
				rows := make([][]string, 0, len(checks))
				for _, c := range checks {
					mark := "✓"
					switch c.Status {
					case doctor.StatusFail:
						mark = "✗"
					case doctor.StatusSkipped:
						mark = "-"
					}
					rows = append(rows, []string{mark, c.Name, c.Detail})
				}
				p.Table([]string{"", "CHECK", "DETAIL"}, rows)
				for _, c := range checks {
					if c.Status == doctor.StatusFail {
						p.Warn(c.Name + ": " + c.Hint)
					}
				}
			}
			if doctor.Failed(checks) {
				return errors.New("some checks failed")
			}
			if !p.IsJSON() {
				p.Success("everything looks good")
			}
			return nil
		},
	}
}
