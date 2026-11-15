package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"charm.land/huh/v2"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/constantine-kostikas/memorable/internal/adminops"
	"github.com/constantine-kostikas/memorable/internal/config"
	"github.com/constantine-kostikas/memorable/internal/embed"
	"github.com/constantine-kostikas/memorable/internal/memory"
	"github.com/constantine-kostikas/memorable/internal/scan"
	"github.com/constantine-kostikas/memorable/internal/store"
)

// generatePassword returns 32 random bytes, base64url encoded.
func generatePassword() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

var errNeedsTerminal = errors.New("this needs an interactive terminal")

func stdinIsTTY(cmd *cobra.Command) bool {
	f, ok := cmd.InOrStdin().(*os.File)
	return ok && term.IsTerminal(f.Fd())
}

func runForm(in io.Reader, out io.Writer, field huh.Field) error {
	if err := huh.NewForm(huh.NewGroup(field)).WithInput(in).WithOutput(out).Run(); err != nil {
		return fmt.Errorf("prompt: %w", err)
	}
	return nil
}

// promptAdminPassword asks for the admin role's password. There is deliberately no flag or env var for it,
// so an agent cannot run admin commands unattended.
func promptAdminPassword(in io.Reader, out io.Writer, isTTY bool) (string, error) {
	if !isTTY {
		return "", fmt.Errorf("the admin password is only read from a terminal: %w", errNeedsTerminal)
	}
	var pw string
	err := runForm(in, out, huh.NewInput().Title("memorable admin password").EchoMode(huh.EchoModePassword).Value(&pw))
	return pw, err
}

func confirm(cmd *cobra.Command, d deps, title string, yes bool) error {
	if yes {
		return nil
	}
	if !d.isTTY(cmd) {
		return usageError{fmt.Errorf("%s needs confirmation: rerun with --yes", title)}
	}
	ok := false
	if err := runForm(cmd.InOrStdin(), cmd.ErrOrStderr(), huh.NewConfirm().Title(title).Value(&ok)); err != nil {
		return err
	}
	if !ok {
		return errors.New("canceled")
	}
	return nil
}

// adminTarget is an authenticated admin session bound to one context schema.
type adminTarget struct {
	cfg     *config.Config
	schema  string
	context string
	dsn     string
	admin   *store.Admin
	ops     *adminops.Ops
}

func (t *adminTarget) Close() {
	if t.admin != nil {
		t.admin.Close()
	}
}

// openAdmin resolves the target context (flag, else current directory) and authenticates as admin.
func openAdmin(ctx context.Context, cmd *cobra.Command, d deps, flags *rootFlags, contextName string) (*adminTarget, error) {
	cfg, _, err := loadConfig(d, flags.configPath)
	if err != nil {
		return nil, err
	}
	if contextName == "" {
		cwd, err := d.cwd()
		if err != nil {
			return nil, fmt.Errorf("reading working directory: %w", err)
		}
		res, err := resolveScope(cfg, []string{cwd}, d.getenv)
		if err != nil {
			return nil, err
		}
		contextName = res.Context
	}
	if _, ok := cfg.Contexts[contextName]; !ok {
		return nil, usageError{fmt.Errorf("context %q is not configured", contextName)}
	}
	pw, err := d.adminPassword(cmd)
	if err != nil {
		return nil, err
	}
	schema := config.SchemaName(contextName)
	admin, err := store.OpenAdmin(ctx, cfg.Database, pw, schema, ownerName(d.getenv))
	if err != nil {
		return nil, err //nolint:wrapcheck // already contextual
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		return nil, err //nolint:wrapcheck // already an ErrUnavailable
	}
	sec, err := scan.NewSecretScanner()
	if err != nil {
		admin.Close()
		return nil, err //nolint:wrapcheck // already contextual
	}
	var emb memory.Embedder
	if ollama, err := embed.NewOllama(cfg.Embedding, nil); err == nil {
		emb = ollama
	}
	return &adminTarget{cfg: cfg, schema: schema, context: contextName,
		dsn: store.DSN(cfg.Database, cfg.Database.AdminUser, pw), admin: admin,
		ops: adminops.New(admin, emb, sec, cfg.Retention.AccessEventsDays, time.Now)}, nil
}
