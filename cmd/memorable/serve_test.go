package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var binPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "memorable-bin")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binPath = filepath.Join(dir, "memorable")
	out, err := exec.Command("go", "build", "-o", binPath, ".").CombinedOutput()
	if err != nil {
		fmt.Fprintln(os.Stderr, string(out))
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// isolatedEnv returns env vars and a project dir for a config whose database is unreachable.
func isolatedEnv(t *testing.T) ([]string, string) {
	t.Helper()
	home := t.TempDir()
	cfgDir := filepath.Join(home, "config", "memorable")
	project := filepath.Join(home, "projects", "demo")
	require.NoError(t, os.MkdirAll(cfgDir, 0o700))
	require.NoError(t, os.MkdirAll(project, 0o755))
	cfg := fmt.Sprintf("[database]\nport = 1\n\n[contexts.personal]\npaths = [%q]\n", filepath.Join(home, "projects"))
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(cfg), 0o600))
	creds := `{"memorable_app@127.0.0.1:1/memorable": "pw"}`
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "credentials.json"), []byte(creds), 0o600))
	env := append(os.Environ(),
		"HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, "config"), "MEMORABLE_KEYRING=off", "MEMORABLE_CONFIG=",
	)
	return env, project
}

func TestServe_StdoutOnlyProtocol(t *testing.T) {
	t.Parallel()
	env, project := isolatedEnv(t)
	cmd := exec.Command(binPath, "serve")
	cmd.Env = env
	cmd.Dir = project
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	stderr := &lockedBuffer{}
	cmd.Stderr = stderr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	send := func(s string) {
		_, err := io.WriteString(stdin, s+"\n")
		require.NoError(t, err)
	}
	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`)
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"memory_search","arguments":{"query":"x"}}}`)

	lines := make(chan string)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1<<20), 1<<22)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	var last string
	deadline := time.After(20 * time.Second)
	for !strings.Contains(last, `"id":3`) {
		select {
		case line, ok := <-lines:
			require.True(t, ok, "stdout closed early; stderr: %s", stderr.String())
			var msg map[string]any
			require.NoError(t, json.Unmarshal([]byte(line), &msg), "non-protocol stdout line: %q", line)
			assert.Equal(t, "2.0", msg["jsonrpc"])
			last = line
		case <-deadline:
			t.Fatalf("timed out; stderr: %s", stderr.String())
		}
	}
	assert.Contains(t, last, "memorable doctor")
	assert.Contains(t, last, `"isError":true`)
	require.NoError(t, stdin.Close())
	for range lines {
	}
	assert.Contains(t, stderr.String(), `"msg":"memorable startup failed"`)
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
