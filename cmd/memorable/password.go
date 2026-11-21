package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"

	"github.com/constantine-kostikas/memorable/internal/config"
	"github.com/constantine-kostikas/memorable/internal/memory"
	"github.com/constantine-kostikas/memorable/internal/secret"
	"github.com/constantine-kostikas/memorable/internal/store"
)

const roleApp = "app"

const resyncHint = "run `memorable migrate` to set the role to the stored password"

type passwordResult struct {
	Role    string `json:"role"`
	Rotated bool   `json:"rotated"`
}

func newPasswordCmd(d deps, flags *rootFlags) *cobra.Command {
	var role string
	cmd := &cobra.Command{
		Use:     "password --role app",
		Short:   "Rotate a database role's password (admin)",
		Long:    "Generates a new password for the app role, sets it in Postgres, and stores it in the keyring.\nRestart Claude Code sessions and any `memorable ui` afterward: they reconnect with the old password until then.",
		Example: "  memorable password --role app",
		GroupID: groupAdmin,
		Args:    argsUsage(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := newPrinter(cmd, flags, d.getenv)
			if err != nil {
				return err
			}
			switch role {
			case roleApp:
			case "":
				return usageError{errors.New("pass --role app")}
			case "admin":
				return usageError{errors.New("only the app role can be rotated; the admin password is yours to manage")}
			default:
				return usageError{fmt.Errorf("unknown role %q: use app", role)}
			}
			cfg, _, err := loadConfig(d, flags.configPath)
			if err != nil {
				return err
			}
			secrets, err := secretStore(d.getenv)
			if err != nil {
				return err
			}
			pw, err := d.adminPassword(cmd)
			if err != nil {
				return err
			}
			if err := rotateAppPassword(cmd.Context(), cfg, pw, secrets, generatePassword()); err != nil {
				return err
			}
			if p.IsJSON() {
				return p.JSON(passwordResult{Role: role, Rotated: true})
			}
			p.Success("rotated the app password and stored it in the keyring")
			p.Warn("restart Claude Code sessions and any `memorable ui` so they connect with the new password")
			return nil
		},
	}
	cmd.Flags().StringVar(&role, "role", "", "role to rotate: app")
	return cmd
}

// rotateAppPassword changes the app role's password and the stored copy together. The role change
// commits only after the new password reads back from the secret store, so a failed store write
// leaves the old password working.
func rotateAppPassword(ctx context.Context, cfg *config.Config, adminPassword string, secrets *secret.Store, newPassword string) error {
	account := appAccount(cfg.Database)
	oldPassword, err := secrets.Get(account)
	if err != nil && !errors.Is(err, memory.ErrNotFound) {
		return err //nolint:wrapcheck // already contextual
	}
	conn, err := pgx.Connect(ctx, store.DSN(cfg.Database, cfg.Database.AdminUser, adminPassword))
	if err != nil {
		return fmt.Errorf("%w: connecting as admin: %w", memory.ErrUnavailable, err)
	}
	defer func() { _ = conn.Close(ctx) }()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := store.SetRolePassword(ctx, tx, cfg.Database.AppUser, newPassword); err != nil {
		if errors.Is(err, store.ErrRoleMissing) {
			return fmt.Errorf("%w; run `memorable migrate` to create it", err)
		}
		return err //nolint:wrapcheck // already contextual
	}
	if err := secrets.Set(account, newPassword); err != nil {
		return fmt.Errorf("storing the new password (role unchanged): %w", err)
	}
	if got, err := secrets.Get(account); err != nil || got != newPassword {
		restoreSecret(secrets, account, oldPassword)
		return errors.New("the keyring still returns the old password after storing the new one (role unchanged)")
	}
	if err := tx.Commit(ctx); err != nil {
		restoreSecret(secrets, account, oldPassword)
		return fmt.Errorf("committing the role change: %w; if the app cannot connect, %s", err, resyncHint)
	}
	return nil
}

func restoreSecret(secrets *secret.Store, account, oldPassword string) {
	if oldPassword == "" {
		_ = secrets.Delete(account)
		return
	}
	_ = secrets.Set(account, oldPassword)
}
