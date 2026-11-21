package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"charm.land/huh/v2"
	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"

	"github.com/constantine-kostikas/memorable/internal/config"
	"github.com/constantine-kostikas/memorable/internal/memory"
	"github.com/constantine-kostikas/memorable/internal/secret"
	"github.com/constantine-kostikas/memorable/internal/store"
)

const (
	roleApp   = "app"
	roleAdmin = "admin"
)

const minAdminPasswordLength = 6

const resyncHint = "run `memorable migrate` to set the role to the stored password"

type changePasswordResult struct {
	Role    string `json:"role"`
	Changed bool   `json:"changed"`
}

func newChangePasswordCmd(d deps, flags *rootFlags) *cobra.Command {
	var role string
	cmd := &cobra.Command{
		Use:   "change-password --role admin|app",
		Short: "Change a database role's password (admin)",
		Long: "--role admin changes the password you type at admin prompts. It asks for the current password,\n" +
			"then the new one twice, from a terminal only.\n\n" +
			"--role app replaces the password memorable uses for everyday access with a generated one and\n" +
			"stores it in the keyring. Restart Claude Code sessions and any `memorable ui` afterward.",
		Example: "  memorable change-password --role admin\n  memorable change-password --role app",
		GroupID: groupAdmin,
		Args:    argsUsage(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := newPrinter(cmd, flags, d.getenv)
			if err != nil {
				return err
			}
			switch role {
			case roleApp, roleAdmin:
			case "":
				return usageError{errors.New("pass --role admin or --role app")}
			default:
				return usageError{fmt.Errorf("unknown role %q: use admin or app", role)}
			}
			cfg, _, err := loadConfig(d, flags.configPath)
			if err != nil {
				return err
			}
			current, err := d.adminPassword(cmd)
			if err != nil {
				return err
			}
			if role == roleAdmin {
				err = changeAdminPassword(cmd.Context(), cfg, current, func() (string, error) {
					return d.newAdminPassword(cmd, current)
				})
			} else {
				var secrets *secret.Store
				if secrets, err = secretStore(d.getenv); err == nil {
					err = rotateAppPassword(cmd.Context(), cfg, current, secrets, generatePassword())
				}
			}
			if err != nil {
				return err
			}
			if p.IsJSON() {
				return p.JSON(changePasswordResult{Role: role, Changed: true})
			}
			if role == roleAdmin {
				p.Success("changed the admin password")
				p.Warn("update your password manager: memorable does not store it")
				return nil
			}
			p.Success("changed the app password and stored it in the keyring")
			p.Warn("restart Claude Code sessions and any `memorable ui` so they connect with the new password")
			return nil
		},
	}
	cmd.Flags().StringVar(&role, "role", "", "role to change: admin or app")
	return cmd
}

// promptNewAdminPassword asks for the new admin password twice. Like the current password, it is
// only read from a terminal.
func promptNewAdminPassword(in io.Reader, out io.Writer, isTTY bool, current string) (string, error) {
	if !isTTY {
		return "", fmt.Errorf("the admin password is only read from a terminal: %w", errNeedsTerminal)
	}
	var pw, again string
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("new admin password").EchoMode(huh.EchoModePassword).Value(&pw).
			Validate(func(s string) error { return validateNewAdminPassword(s, current) }),
		huh.NewInput().Title("confirm new admin password").EchoMode(huh.EchoModePassword).Value(&again).
			Validate(func(s string) error {
				if s != pw {
					return errors.New("the passwords do not match")
				}
				return nil
			}),
	)).WithInput(in).WithOutput(out)
	if err := form.Run(); err != nil {
		return "", fmt.Errorf("prompt: %w", err)
	}
	return pw, nil
}

func validateNewAdminPassword(pw, current string) error {
	switch {
	case utf8.RuneCountInString(pw) < minAdminPasswordLength:
		return fmt.Errorf("use at least %d characters", minAdminPasswordLength)
	case pw == current:
		return errors.New("the new password matches the current one")
	}
	return nil
}

// changeAdminPassword checks the current password by connecting with it before asking for the new
// one, so a typo in the current password does not waste the new one.
func changeAdminPassword(ctx context.Context, cfg *config.Config, current string, newPassword func() (string, error)) error {
	conn, err := pgx.Connect(ctx, store.DSN(cfg.Database, cfg.Database.AdminUser, current))
	if err != nil {
		return fmt.Errorf("%w: connecting as admin: %w", memory.ErrUnavailable, err)
	}
	defer func() { _ = conn.Close(ctx) }()
	pw, err := newPassword()
	if err != nil {
		return err
	}
	if err := validateNewAdminPassword(pw, current); err != nil {
		return usageError{err}
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := store.SetRolePassword(ctx, tx, cfg.Database.AdminUser, pw); err != nil {
		return err //nolint:wrapcheck // already contextual
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing the password change (current password unchanged): %w", err)
	}
	return nil
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
