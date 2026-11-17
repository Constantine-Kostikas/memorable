// Package storetest starts a disposable pgvector Postgres for integration tests.
package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/constantine-kostikas/memorable/internal/config"
	"github.com/constantine-kostikas/memorable/internal/dockerpg"
	"github.com/constantine-kostikas/memorable/internal/store"
)

// Image is the Postgres image used in production and tests.
const Image = dockerpg.Image

const (
	adminUser = "memorable_admin"
	// AdminPassword is the admin role password used by tests.
	AdminPassword = "admin-test-password"
	// AppPassword is the app role password used by tests.
	AppPassword = "app-test-password"
)

var (
	once        sync.Once
	baseDSN     string
	containerID string
	startErr    error
	// bootstrapMu serializes role creation, which is cluster-wide and not concurrency-safe.
	bootstrapMu sync.Mutex
)

// Database is a fresh database inside the shared test container.
type Database struct {
	Name     string
	AdminDSN string
	AppDSN   string
	Host     string
	Port     int
}

func start() {
	ctx := context.Background()
	c, err := postgres.Run(ctx, Image,
		postgres.WithDatabase("memorable"),
		postgres.WithUsername(adminUser),
		postgres.WithPassword(AdminPassword),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		startErr = fmt.Errorf("starting postgres container: %w", err)
		return
	}
	containerID = c.GetContainerID()
	baseDSN, startErr = c.ConnectionString(ctx, "sslmode=disable")
}

// ContainerID returns the shared test container's id, for tests that run tools inside it with docker exec.
func ContainerID(tb testing.TB) string {
	tb.Helper()
	once.Do(start)
	if startErr != nil {
		tb.Fatal(startErr)
	}
	return containerID
}

// AdminUser is the superuser role inside the test container.
const AdminUser = adminUser

// NewDatabase creates a uniquely named database and returns its DSNs.
func NewDatabase(tb testing.TB) Database {
	tb.Helper()
	once.Do(start)
	if startErr != nil {
		tb.Fatal(startErr)
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, baseDSN)
	if err != nil {
		tb.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()

	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "t_" + hex.EncodeToString(b)
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		tb.Fatal(err)
	}
	u, err := url.Parse(baseDSN)
	if err != nil {
		tb.Fatal(err)
	}
	u.Path = "/" + name
	admin := u.String()
	u.User = url.UserPassword("memorable_app", AppPassword)
	cfg, err := pgx.ParseConfig(admin)
	if err != nil {
		tb.Fatal(err)
	}
	return Database{Name: name, AdminDSN: admin, AppDSN: u.String(), Host: cfg.Host, Port: int(cfg.Port)}
}

// Bootstrap runs store.Bootstrap and migrates every context schema and the profile schema.
func Bootstrap(ctx context.Context, tb testing.TB, db Database, cfg *config.Config) error {
	tb.Helper()
	local := *cfg
	local.Database.Name = db.Name

	bootstrapMu.Lock()
	conn, err := pgx.Connect(ctx, db.AdminDSN)
	if err != nil {
		bootstrapMu.Unlock()
		return fmt.Errorf("connecting admin: %w", err)
	}
	err = store.Bootstrap(ctx, conn, &local, AppPassword)
	_ = conn.Close(ctx)
	bootstrapMu.Unlock()
	if err != nil {
		return err
	}

	if _, err := store.Migrate(ctx, db.AdminDSN, store.ProfileSchema, store.SchemaProfile, local.Embedding.Dimensions, local.Database.AppUser); err != nil {
		return err
	}
	for _, name := range local.ContextNames() {
		if _, err := store.Migrate(ctx, db.AdminDSN, config.SchemaName(name), store.SchemaContext, local.Embedding.Dimensions, local.Database.AppUser); err != nil {
			return err
		}
	}
	return nil
}

// DatabaseConfig returns a config.Database pointing at db.
func DatabaseConfig(db Database) config.Database {
	d := config.Default().Database
	d.Host, d.Port, d.Name = db.Host, db.Port, db.Name
	return d
}
