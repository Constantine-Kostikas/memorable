//go:build integration

package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/config"
	"github.com/constantine-kostikas/memorable/internal/store/storetest"
)

func TestServe_ExitsWhenClientDisconnects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := storetest.NewDatabase(t)
	home := t.TempDir()
	project := filepath.Join(home, "projects", "demo")
	require.NoError(t, os.MkdirAll(project, 0o755))
	cfg := config.Default()
	cfg.Database = storetest.DatabaseConfig(db)
	cfg.Contexts = map[string]config.Context{"personal": {Paths: []string{filepath.Join(home, "projects")}}}
	cfg.Embedding.URL = "http://127.0.0.1:1"
	require.NoError(t, storetest.Bootstrap(ctx, t, db, &cfg))
	cfgDir := filepath.Join(home, "config", "memorable")
	require.NoError(t, config.Save(filepath.Join(cfgDir, "config.toml"), cfg))
	creds := fmt.Sprintf(`{"memorable_app@%s:%d/%s": %q}`, cfg.Database.Host, cfg.Database.Port, cfg.Database.Name, storetest.AppPassword)
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "credentials.json"), []byte(creds), 0o600))

	cmd := exec.Command(binPath, "serve")
	cmd.Env = append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, "config"), "MEMORABLE_KEYRING=off", "MEMORABLE_CONFIG=")
	cmd.Dir = project
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	for _, line := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"memory_top","arguments":{}}}`,
	} {
		_, err := io.WriteString(stdin, line+"\n")
		require.NoError(t, err)
	}
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		if strings.Contains(sc.Text(), `"id":2`) {
			require.NotContains(t, sc.Text(), `"isError":true`, "the session started, so the worker runs")
			break
		}
	}
	require.NoError(t, stdin.Close())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		require.NoError(t, err, "serve exits cleanly on EOF")
	case <-time.After(10 * time.Second):
		t.Fatal("serve kept running after the client closed stdin")
	}
}
