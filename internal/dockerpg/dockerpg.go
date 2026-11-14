// Package dockerpg manages the memorable Postgres container through the docker CLI.
package dockerpg

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/constantine-kostikas/memorable/internal/config"
)

// Image is the pinned pgvector image used in production and tests.
const Image = "pgvector/pgvector:0.8.7-pg18-trixie"

// Volume holds the database files.
const Volume = "memorable-pgdata"

// Runner runs `docker <args>` and returns stdout.
type Runner func(ctx context.Context, args ...string) ([]byte, error)

// State is the container's lifecycle state.
type State uint8

// Container states.
const (
	StateUnknown State = iota
	StateMissing
	StateStopped
	StateRunning
)

// RunArgs returns the docker arguments that create the container, publishing only on loopback.
func RunArgs(db config.Database, adminPassword string) ([]string, error) {
	ip := net.ParseIP(db.Host)
	if db.Host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("database host %q must be a loopback address for a local container", db.Host)
	}
	if db.Host == "localhost" {
		db.Host = "127.0.0.1"
	}
	return []string{"run", "-d", "--name", db.Container, "--restart", "unless-stopped",
		"-p", net.JoinHostPort(db.Host, strconv.Itoa(db.Port)) + ":5432",
		"-e", "POSTGRES_USER=" + db.AdminUser, "-e", "POSTGRES_PASSWORD=" + adminPassword,
		"-e", "POSTGRES_DB=" + db.Name, "-v", Volume + ":/var/lib/postgresql", Image}, nil
}

// Start creates and starts the container.
func Start(ctx context.Context, run Runner, db config.Database, adminPassword string) error {
	args, err := RunArgs(db, adminPassword)
	if err != nil {
		return err
	}
	if _, err := run(ctx, args...); err != nil {
		return fmt.Errorf("starting %s: %w", db.Container, err)
	}
	return nil
}

// ContainerState inspects the container.
func ContainerState(ctx context.Context, run Runner, container string) (State, error) {
	out, err := run(ctx, "inspect", "-f", "{{.State.Running}}", container)
	if err != nil {
		return StateMissing, nil //nolint:nilerr // inspect fails exactly when the container does not exist
	}
	if strings.TrimSpace(string(out)) == "true" {
		return StateRunning, nil
	}
	return StateStopped, nil
}

// WaitReady polls pg_isready inside the container until it accepts connections or timeout passes.
func WaitReady(ctx context.Context, run Runner, container, user string, every, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := run(ctx, "exec", container, "pg_isready", "-U", user); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not become ready within %s", container, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err() //nolint:wrapcheck // cancellation passes through
		case <-time.After(every):
		}
	}
}
