package store

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	pgxvec "github.com/pgvector/pgvector-go/pgx"

	"github.com/constantine-kostikas/memorable/internal/config"
	"github.com/constantine-kostikas/memorable/internal/memory"
)

// Store is the app-role repository bound to one context schema.
type Store struct {
	pool   *pgxpool.Pool
	owner  string
	schema string
}

var _ memory.Repository = (*Store)(nil)

// DSN builds a Postgres connection string.
func DSN(db config.Database, user, password string) string {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, password),
		Host:     net.JoinHostPort(db.Host, strconv.Itoa(db.Port)),
		Path:     "/" + db.Name,
		RawQuery: "sslmode=disable&connect_timeout=5",
	}
	return u.String()
}

// Open creates a lazily connecting pool whose sessions are bound to schema.
func Open(ctx context.Context, db config.Database, password, schema, owner string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(DSN(db, db.AppUser, password))
	if err != nil {
		return nil, fmt.Errorf("parsing database config: %w", err)
	}
	cfg.MaxConns = 4
	cfg.MinConns = 0
	cfg.MaxConnIdleTime = time.Minute
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		setup := "SET search_path = " + pgx.Identifier{schema}.Sanitize() + ", public; " +
			"SET hnsw.iterative_scan = relaxed_order; SET hnsw.ef_search = 100; " +
			"SET statement_timeout = '30s'; SET lock_timeout = '5s'; SET idle_in_transaction_session_timeout = '60s'"
		if _, err := conn.Exec(ctx, setup); err != nil {
			return fmt.Errorf("configuring session: %w", err)
		}
		if err := pgxvec.RegisterTypes(ctx, conn); err != nil {
			return fmt.Errorf("registering vector types: %w", err)
		}
		return nil
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("creating pool: %w", err)
	}
	return &Store{pool: pool, owner: owner, schema: schema}, nil
}

// Ping checks that the database is reachable.
func (s *Store) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("%w: database: %w", memory.ErrUnavailable, err)
	}
	return nil
}

// Close releases the pool.
func (s *Store) Close() { s.pool.Close() }

func (s *Store) withTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return mapErr("begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return mapErr("commit", err)
	}
	return nil
}

const (
	sqlstateUniqueViolation  = "23505"
	sqlstateQueryCanceled    = "57014"
	sqlstateLockNotAvailable = "55P03"
)

// mapErr translates driver errors to domain errors.
func mapErr(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", op, memory.ErrNotFound)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case sqlstateUniqueViolation:
			return fmt.Errorf("%s: %w: %s", op, memory.ErrConflict, pgErr.ConstraintName)
		case sqlstateQueryCanceled, sqlstateLockNotAvailable:
			return fmt.Errorf("%s: %w: %w", op, memory.ErrUnavailable, err)
		}
		return fmt.Errorf("%s: %w", op, err)
	}
	var connErr *pgconn.ConnectError
	if errors.As(err, &connErr) || pgconn.Timeout(err) {
		return fmt.Errorf("%s: %w: %w", op, memory.ErrUnavailable, err)
	}
	return fmt.Errorf("%s: %w", op, err)
}
