package doctor

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/config"
	"github.com/constantine-kostikas/memorable/internal/memory"
)

type fakeProbe struct {
	ext  map[string]string
	ver  int64
	dims int
}

func (f *fakeProbe) Ping(context.Context) error { return nil }
func (f *fakeProbe) Extensions(context.Context) (map[string]string, error) {
	return f.ext, nil
}
func (f *fakeProbe) SchemaVersion(context.Context, string) (int64, error) { return f.ver, nil }
func (f *fakeProbe) EmbeddingDimensions(context.Context) (int, error)     { return f.dims, nil }
func (f *fakeProbe) Close()                                               {}

func ollama(t *testing.T, models ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			_, _ = w.Write([]byte(`{"version":"0.40.16"}`))
		case "/api/tags":
			body := `{"models":[`
			for i, m := range models {
				if i > 0 {
					body += ","
				}
				body += `{"name":"` + m + `"}`
			}
			_, _ = w.Write([]byte(body + `]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func healthy(t *testing.T) Deps {
	t.Helper()
	cfg := config.Default()
	cfg.Contexts = map[string]config.Context{"personal": {Paths: []string{"/p"}}}
	cfg.Embedding.URL = ollama(t, "embeddinggemma:latest").URL
	return Deps{
		Config:      &cfg,
		ContextName: "personal",
		Password:    func() (string, error) { return "pw", nil },
		Docker:      func(context.Context, ...string) ([]byte, error) { return []byte("true\n"), nil },
		Dial: func(context.Context, string, string) (net.Conn, error) {
			a, b := net.Pipe()
			_ = b.Close()
			return a, nil
		},
		Open: func(context.Context, string) (Probe, error) {
			return &fakeProbe{ext: map[string]string{"vector": "0.8.7", "pg_trgm": "1.6"}, ver: 2, dims: 768}, nil
		},
		HTTP: http.DefaultClient,
	}
}

func byName(checks []Check) map[string]Check {
	out := map[string]Check{}
	for _, c := range checks {
		out[c.Name] = c
	}
	return out
}

func TestRun_AllGreen(t *testing.T) {
	t.Parallel()
	checks := Run(context.Background(), healthy(t))
	require.NotEmpty(t, checks)
	for _, c := range checks {
		assert.Equal(t, StatusOK, c.Status, "%s: %s", c.Name, c.Detail)
	}
	assert.False(t, Failed(checks))
}

func TestRun_ContainerStopped(t *testing.T) {
	t.Parallel()
	d := healthy(t)
	d.Docker = func(context.Context, ...string) ([]byte, error) { return []byte("false\n"), nil }
	checks := byName(Run(context.Background(), d))
	assert.Equal(t, StatusFail, checks["container"].Status)
	assert.Contains(t, checks["container"].Hint, "docker start memorable-postgres")
	assert.Equal(t, StatusSkipped, checks["app login"].Status)
	assert.Equal(t, StatusSkipped, checks["extensions"].Status)
}

func TestRun_ModelMissing(t *testing.T) {
	t.Parallel()
	d := healthy(t)
	d.Config.Embedding.URL = ollama(t, "llama3:latest").URL
	checks := byName(Run(context.Background(), d))
	assert.Equal(t, StatusFail, checks["model"].Status)
	assert.Contains(t, checks["model"].Hint, "ollama pull embeddinggemma")
}

func TestRun_DimensionMismatch(t *testing.T) {
	t.Parallel()
	d := healthy(t)
	d.Config.Embedding.Dimensions = 1024
	checks := byName(Run(context.Background(), d))
	assert.Equal(t, StatusFail, checks["dimensions"].Status)
	assert.Contains(t, checks["dimensions"].Hint, "memorable reembed --all")
}

func TestRun_NoPassword(t *testing.T) {
	t.Parallel()
	d := healthy(t)
	d.Password = func() (string, error) { return "", memory.ErrNotFound }
	checks := byName(Run(context.Background(), d))
	assert.Equal(t, StatusFail, checks["app password"].Status)
	assert.Contains(t, checks["app password"].Hint, "memorable setup")
	assert.Equal(t, StatusSkipped, checks["app login"].Status)
}

func TestRun_OldPgvector(t *testing.T) {
	t.Parallel()
	d := healthy(t)
	d.Open = func(context.Context, string) (Probe, error) {
		return &fakeProbe{ext: map[string]string{"vector": "0.7.4", "pg_trgm": "1.6"}, ver: 2, dims: 768}, nil
	}
	checks := byName(Run(context.Background(), d))
	assert.Equal(t, StatusFail, checks["extensions"].Status)
}

func TestRun_ConfigError(t *testing.T) {
	t.Parallel()
	checks := Run(context.Background(), Deps{ConfigErr: errors.New("bad toml")})
	require.Len(t, checks, 1)
	assert.Equal(t, StatusFail, checks[0].Status)
}
