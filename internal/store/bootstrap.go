// Package store is the Postgres adapter for memorable.
package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/constantine-kostikas/memorable/internal/config"
)

// ProfileSchema holds memories readable from every context and writable only by the admin role.
const ProfileSchema = "mem_profile"

// Bootstrap prepares extensions, the app role, and schemas. It is idempotent and needs an admin connection.
func Bootstrap(ctx context.Context, admin *pgx.Conn, cfg *config.Config, appPassword string) error {
	for _, ext := range []string{"vector", "pg_trgm"} {
		if _, err := admin.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS "+pgx.Identifier{ext}.Sanitize()+" WITH SCHEMA public"); err != nil {
			return fmt.Errorf("creating extension %s: %w", ext, err)
		}
	}
	if err := ensureAppRole(ctx, admin, cfg.Database.AppUser, appPassword); err != nil {
		return err
	}
	app := pgx.Identifier{cfg.Database.AppUser}.Sanitize()
	stmts := []string{
		"REVOKE CREATE ON SCHEMA public FROM PUBLIC",
		"GRANT CONNECT ON DATABASE " + pgx.Identifier{cfg.Database.Name}.Sanitize() + " TO " + app,
		"GRANT USAGE ON SCHEMA public TO " + app,
	}
	for _, s := range stmts {
		if _, err := admin.Exec(ctx, s); err != nil {
			return fmt.Errorf("bootstrap %q: %w", s, err)
		}
	}
	schemas := []string{ProfileSchema}
	for _, name := range cfg.ContextNames() {
		schemas = append(schemas, config.SchemaName(name))
	}
	for _, schema := range schemas {
		id := pgx.Identifier{schema}.Sanitize()
		if _, err := admin.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+id); err != nil {
			return fmt.Errorf("creating schema %s: %w", schema, err)
		}
		if _, err := admin.Exec(ctx, "GRANT USAGE ON SCHEMA "+id+" TO "+app); err != nil {
			return fmt.Errorf("granting schema %s: %w", schema, err)
		}
	}
	return nil
}

func ensureAppRole(ctx context.Context, admin *pgx.Conn, role, password string) error {
	var exists bool
	if err := admin.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", role).Scan(&exists); err != nil {
		return fmt.Errorf("checking role: %w", err)
	}
	verb := "CREATE"
	if exists {
		verb = "ALTER"
	}
	// format() quotes the identifier and the password literal server-side; DDL cannot take bind parameters.
	var stmt string
	if err := admin.QueryRow(ctx,
		"SELECT format('"+verb+" ROLE %I WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT PASSWORD %L', $1::text, $2::text)",
		role, password).Scan(&stmt); err != nil {
		return fmt.Errorf("building role statement: %w", err)
	}
	if _, err := admin.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("%s role %s: %w", verb, role, err)
	}
	return nil
}
