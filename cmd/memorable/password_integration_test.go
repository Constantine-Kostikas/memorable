//go:build integration

package main

import (
	"context"
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
