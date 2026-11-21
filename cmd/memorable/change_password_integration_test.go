//go:build integration

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/config"
	"github.com/constantine-kostikas/memorable/internal/secret"
	"github.com/constantine-kostikas/memorable/internal/store"
	"github.com/constantine-kostikas/memorable/internal/store/storetest"
)

// rotationFixture bootstraps a database with its own app role: roles are cluster-wide, so rotating
// the shared one would break tests running in parallel.
func rotationFixture(t *testing.T, appUser string) (*config.Config, *secret.Store, string) {
	t.Helper()
	ctx := context.Background()
	db := storetest.NewDatabase(t)
	cfg := config.Default()
	cfg.Database = storetest.DatabaseConfig(db)
	cfg.Database.AppUser = appUser
	cfg.Contexts = map[string]config.Context{"personal": {Paths: []string{t.TempDir()}}}
	require.NoError(t, storetest.Bootstrap(ctx, t, db, &cfg))
	credentials := filepath.Join(t.TempDir(), "memorable", "credentials.json")
	secrets := secret.New(credentials, secret.WithoutKeyring())
	require.NoError(t, secrets.Set(appAccount(cfg.Database), storetest.AppPassword))
	return &cfg, secrets, credentials
}

func appCanConnect(t *testing.T, cfg *config.Config, password string) bool {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), store.DSN(cfg.Database, cfg.Database.AppUser, password))
	if err != nil {
		return false
	}
	_ = conn.Close(context.Background())
	return true
}

func TestRotateAppPassword(t *testing.T) {
	t.Parallel()
	cfg, secrets, _ := rotationFixture(t, "memorable_app_rotate")

	require.NoError(t, rotateAppPassword(context.Background(), cfg, storetest.AdminPassword, secrets, "rotated-password"))

	stored, err := secrets.Get(appAccount(cfg.Database))
	require.NoError(t, err)
	assert.Equal(t, "rotated-password", stored)
	assert.True(t, appCanConnect(t, cfg, "rotated-password"))
	assert.False(t, appCanConnect(t, cfg, storetest.AppPassword))
}

func TestRotateAppPassword_StoreFailureKeepsOldPassword(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	cfg, secrets, credentials := rotationFixture(t, "memorable_app_rotate_fail")
	dir := filepath.Dir(credentials)
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := rotateAppPassword(context.Background(), cfg, storetest.AdminPassword, secrets, "rotated-password")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "role unchanged")
	assert.True(t, appCanConnect(t, cfg, storetest.AppPassword))
	assert.False(t, appCanConnect(t, cfg, "rotated-password"))
}

func TestRotateAppPassword_MissingRole(t *testing.T) {
	t.Parallel()
	cfg, secrets, _ := rotationFixture(t, "memorable_app_rotate_missing")
	cfg.Database.AppUser = "memorable_app_never_created"

	err := rotateAppPassword(context.Background(), cfg, storetest.AdminPassword, secrets, "rotated-password")
	require.ErrorIs(t, err, store.ErrRoleMissing)
	assert.Contains(t, err.Error(), "memorable migrate")
}

// adminFixture creates a superuser of its own to change: the shared admin role is cluster-wide.
func adminFixture(t *testing.T, adminUser, password string) *config.Config {
	t.Helper()
	ctx := context.Background()
	db := storetest.NewDatabase(t)
	conn, err := pgx.Connect(ctx, db.AdminDSN)
	require.NoError(t, err)
	defer func() { _ = conn.Close(ctx) }()
	var stmt string
	require.NoError(t, conn.QueryRow(ctx, "SELECT format('CREATE ROLE %I WITH LOGIN SUPERUSER PASSWORD %L', $1::text, $2::text)",
		adminUser, password).Scan(&stmt))
	_, err = conn.Exec(ctx, stmt)
	require.NoError(t, err)
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), db.AdminDSN)
		if err == nil {
			_, _ = c.Exec(context.Background(), "DROP ROLE IF EXISTS "+pgx.Identifier{adminUser}.Sanitize())
			_ = c.Close(context.Background())
		}
	})
	cfg := config.Default()
	cfg.Database = storetest.DatabaseConfig(db)
	cfg.Database.AdminUser = adminUser
	return &cfg
}

func adminCanConnect(t *testing.T, cfg *config.Config, password string) bool {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), store.DSN(cfg.Database, cfg.Database.AdminUser, password))
	if err != nil {
		return false
	}
	_ = conn.Close(context.Background())
	return true
}

func TestChangeAdminPassword(t *testing.T) {
	t.Parallel()
	const current, next = "current-admin-password", "a-brand-new-admin-password"
	cfg := adminFixture(t, "memorable_admin_change", current)

	require.NoError(t, changeAdminPassword(context.Background(), cfg, current, func() (string, error) { return next, nil }))

	assert.True(t, adminCanConnect(t, cfg, next))
	assert.False(t, adminCanConnect(t, cfg, current))
}

func TestChangeAdminPassword_WrongCurrentAsksNothing(t *testing.T) {
	t.Parallel()
	const current = "current-admin-password"
	cfg := adminFixture(t, "memorable_admin_change_wrong", current)
	asked := false

	err := changeAdminPassword(context.Background(), cfg, "not-the-password", func() (string, error) {
		asked = true
		return "a-brand-new-admin-password", nil
	})

	require.Error(t, err)
	assert.False(t, asked, "the new password is asked for only after the current one is verified")
	assert.True(t, adminCanConnect(t, cfg, current))
}

func TestChangeAdminPassword_RejectsWeakPassword(t *testing.T) {
	t.Parallel()
	const current = "current-admin-password"
	cfg := adminFixture(t, "memorable_admin_change_weak", current)

	err := changeAdminPassword(context.Background(), cfg, current, func() (string, error) { return "short", nil })

	require.Error(t, err)
	assert.Equal(t, 2, exitCode(err))
	assert.True(t, adminCanConnect(t, cfg, current))
}

func TestChangeAdminPassword_PromptErrorKeepsPassword(t *testing.T) {
	t.Parallel()
	const current = "current-admin-password"
	cfg := adminFixture(t, "memorable_admin_change_cancel", current)

	err := changeAdminPassword(context.Background(), cfg, current, func() (string, error) { return "", errors.New("canceled") })

	require.Error(t, err)
	assert.True(t, adminCanConnect(t, cfg, current))
}
