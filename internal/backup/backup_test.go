package backup

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBackup_Rotation(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "backups")
	var gotArgs []string
	run := func(_ context.Context, stdout io.Writer, name string, args ...string) error {
		gotArgs = append([]string{name}, args...)
		_, err := io.WriteString(stdout, "PGDMP")
		return err
	}
	start := time.Date(2026, 11, 14, 3, 0, 0, 0, time.UTC)
	var last string
	for i := range 16 {
		p, err := Backup(context.Background(), run, Options{Container: "memorable-postgres", User: "memorable_admin",
			Database: "memorable", Dir: dir, Keep: 14}, start.Add(time.Duration(i)*24*time.Hour))
		require.NoError(t, err)
		last = p
	}
	assert.Equal(t, []string{"docker", "exec", "memorable-postgres", "pg_dump", "-U", "memorable_admin", "-Fc", "memorable"}, gotArgs)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	require.Len(t, names, 14)
	assert.Equal(t, "memorable-20261116-030000.dump", names[0], "two oldest removed")
	assert.Equal(t, filepath.Base(last), names[13])

	info, err := os.Stat(last)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	dirInfo, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())
}

func TestBackup_FailureLeavesNoFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	run := func(context.Context, io.Writer, string, ...string) error { return io.ErrUnexpectedEOF }
	_, err := Backup(context.Background(), run, Options{Container: "c", User: "u", Database: "d", Dir: dir, Keep: 3}, time.Now())
	require.Error(t, err)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestDefaultDir(t *testing.T) {
	t.Parallel()
	got, err := DefaultDir(func(k string) string {
		if k == "XDG_DATA_HOME" {
			return "/data"
		}
		return ""
	})
	require.NoError(t, err)
	assert.Equal(t, "/data/memorable/backups", got)
}
