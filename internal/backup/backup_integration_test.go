//go:build integration

package backup_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/backup"
	"github.com/constantine-kostikas/memorable/internal/config"
	"github.com/constantine-kostikas/memorable/internal/memory"
	"github.com/constantine-kostikas/memorable/internal/store"
	"github.com/constantine-kostikas/memorable/internal/store/storetest"
)

func docker(ctx context.Context, stdin io.Reader, stdout io.Writer, args ...string) error {
	c := exec.CommandContext(ctx, "docker", args...)
	var stderr bytes.Buffer
	c.Stdin, c.Stdout, c.Stderr = stdin, stdout, &stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("docker %v: %w: %s", args, err, stderr.String())
	}
	return nil
}

func countMemories(ctx context.Context, t *testing.T, dsn string) int {
	t.Helper()
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer func() { _ = conn.Close(ctx) }()
	var n int
	require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM mem_personal.memories").Scan(&n))
	return n
}

func TestBackup_RestoresIntoFreshDatabase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg := config.Default()
	cfg.Contexts = map[string]config.Context{"personal": {Paths: []string{"/p"}}}
	src := storetest.NewDatabase(t)
	require.NoError(t, storetest.Bootstrap(ctx, t, src, &cfg))
	s, err := store.Open(ctx, storetest.DatabaseConfig(src), storetest.AppPassword, "mem_personal", "dev")
	require.NoError(t, err)
	defer s.Close()
	for _, title := range []string{"one", "two"} {
		m := &memory.Memory{ID: uuid.Must(uuid.NewV7()), Owner: "dev", Type: memory.TypeProject, Title: title, Content: title,
			Source: memory.SourceUserStated, Status: memory.StatusActive, Version: 1, ContentHash: memory.ContentHash(title, title)}
		require.NoError(t, s.Insert(ctx, m, memory.AuditEntry{Action: "create", Cwd: "/p", Process: "1@t"}))
	}

	container := storetest.ContainerID(t)
	run := func(ctx context.Context, w io.Writer, name string, args ...string) error {
		require.Equal(t, "docker", name)
		return docker(ctx, nil, w, args...)
	}
	path, err := backup.Backup(ctx, run, backup.Options{Container: container, User: storetest.AdminUser,
		Database: src.Name, Dir: t.TempDir(), Keep: 3}, time.Now())
	require.NoError(t, err)

	dst := storetest.NewDatabase(t)
	dump, err := os.Open(path) //nolint:gosec // test temp file
	require.NoError(t, err)
	defer func() { _ = dump.Close() }()
	require.NoError(t, docker(ctx, dump, io.Discard, "exec", "-i", container,
		"pg_restore", "-U", storetest.AdminUser, "-d", dst.Name, "--exit-on-error"))
	assert.Equal(t, 2, countMemories(ctx, t, dst.AdminDSN))
}
