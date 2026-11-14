// Package backup dumps the database through the Postgres container and rotates old dumps.
package backup

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Runner runs name with args, streaming stdout to w.
type Runner func(ctx context.Context, w io.Writer, name string, args ...string) error

// Options configures a backup.
type Options struct {
	Container string
	User      string
	Database  string
	Dir       string
	Keep      int
}

const prefix = "memorable-"

// DefaultDir returns $XDG_DATA_HOME/memorable/backups, or ~/.local/share/memorable/backups.
func DefaultDir(getenv func(string) string) (string, error) {
	if d := getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "memorable", "backups"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating home: %w", err)
	}
	return filepath.Join(home, ".local", "share", "memorable", "backups"), nil
}

// Backup runs pg_dump inside the container (no host pg_dump or password needed), writes the dump
// to a new owner-only file, and keeps the newest o.Keep dumps.
func Backup(ctx context.Context, run Runner, o Options, now time.Time) (string, error) {
	if err := os.MkdirAll(o.Dir, 0o700); err != nil {
		return "", fmt.Errorf("creating %s: %w", o.Dir, err)
	}
	if err := os.Chmod(o.Dir, 0o700); err != nil { //nolint:gosec // a directory needs the execute bit; still owner-only
		return "", fmt.Errorf("securing %s: %w", o.Dir, err)
	}
	path := filepath.Join(o.Dir, prefix+now.UTC().Format("20060102-150405")+".dump")
	tmp := path + ".partial"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // path built from our own dir
	if err != nil {
		return "", fmt.Errorf("creating dump file: %w", err)
	}
	runErr := run(ctx, f, "docker", "exec", o.Container, "pg_dump", "-U", o.User, "-Fc", o.Database)
	closeErr := f.Close()
	if runErr != nil || closeErr != nil {
		_ = os.Remove(tmp)
		if runErr != nil {
			return "", fmt.Errorf("pg_dump: %w", runErr)
		}
		return "", fmt.Errorf("writing dump: %w", closeErr)
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", fmt.Errorf("finishing dump: %w", err)
	}
	return path, rotate(o.Dir, o.Keep)
}

func rotate(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("listing backups: %w", err)
	}
	var dumps []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) && strings.HasSuffix(e.Name(), ".dump") {
			dumps = append(dumps, e.Name())
		}
	}
	sort.Strings(dumps)
	for len(dumps) > keep {
		if err := os.Remove(filepath.Join(dir, dumps[0])); err != nil {
			return fmt.Errorf("removing old backup: %w", err)
		}
		dumps = dumps[1:]
	}
	return nil
}
