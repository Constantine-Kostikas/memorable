package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/config"
	"github.com/constantine-kostikas/memorable/internal/memory"
)

type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

func fakeOllama(t *testing.T, dims int, calls *atomic.Int32, seen chan<- embedRequest) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var req embedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if calls != nil {
			calls.Add(1)
		}
		if seen != nil {
			seen <- req
		}
		out := make([][]float32, len(req.Input))
		for i := range out {
			out[i] = make([]float32, dims)
			out[i][0] = 1
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": req.Model, "embeddings": out})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func cfgFor(url string) config.Embedding {
	e := config.Default().Embedding
	e.URL = url
	return e
}

func TestEmbed_RequestShape(t *testing.T) {
	t.Parallel()
	seen := make(chan embedRequest, 2)
	srv := fakeOllama(t, 768, nil, seen)
	o, err := NewOllama(cfgFor(srv.URL), srv.Client())
	require.NoError(t, err)

	vecs, err := o.Embed(context.Background(), memory.EmbedQuery, []string{"postgres port"})
	require.NoError(t, err)
	require.Len(t, vecs, 1)
	assert.Len(t, vecs[0], 768)
	req := <-seen
	assert.Equal(t, "embeddinggemma", req.Model)
	assert.Equal(t, []string{"task: search result | query: postgres port"}, req.Input)

	doc := memory.DocumentText("Ports", nil, "5434")
	_, err = o.Embed(context.Background(), memory.EmbedDocument, []string{doc})
	require.NoError(t, err)
	assert.Equal(t, []string{"title: Ports | text: 5434"}, (<-seen).Input)
	assert.Equal(t, "embeddinggemma", o.Model())
	assert.Equal(t, 768, o.Dimensions())
}

func TestEmbed_Batches(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := fakeOllama(t, 768, &calls, nil)
	o, err := NewOllama(cfgFor(srv.URL), srv.Client())
	require.NoError(t, err)
	texts := make([]string, 70)
	for i := range texts {
		texts[i] = strings.Repeat("x", i+1)
	}
	vecs, err := o.Embed(context.Background(), memory.EmbedDocument, texts)
	require.NoError(t, err)
	assert.Len(t, vecs, 70)
	assert.Equal(t, int32(3), calls.Load())
}

func TestEmbed_DimensionMismatch(t *testing.T) {
	t.Parallel()
	srv := fakeOllama(t, 512, nil, nil)
	o, err := NewOllama(cfgFor(srv.URL), srv.Client())
	require.NoError(t, err)
	_, err = o.Embed(context.Background(), memory.EmbedQuery, []string{"x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "512")
}

func TestEmbed_Unavailable(t *testing.T) {
	t.Parallel()
	srv := fakeOllama(t, 768, nil, nil)
	url := srv.URL
	srv.Close()
	o, err := NewOllama(cfgFor(url), http.DefaultClient)
	require.NoError(t, err)
	_, err = o.Embed(context.Background(), memory.EmbedQuery, []string{"x"})
	require.ErrorIs(t, err, memory.ErrUnavailable)
}

func TestEmbed_ModelMissing(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"model \"embeddinggemma\" not found, try pulling it first"}`, http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	o, err := NewOllama(cfgFor(srv.URL), srv.Client())
	require.NoError(t, err)
	_, err = o.Embed(context.Background(), memory.EmbedQuery, []string{"x"})
	require.ErrorIs(t, err, memory.ErrUnavailable)
	assert.Contains(t, err.Error(), "not found")
}

func TestNewOllama_RejectsRemote(t *testing.T) {
	t.Parallel()
	_, err := NewOllama(cfgFor("http://10.0.0.5:11434"), nil)
	require.Error(t, err)
	e := cfgFor("http://10.0.0.5:11434")
	e.AllowRemote = true
	_, err = NewOllama(e, nil)
	require.NoError(t, err)
}

func TestEmbed_Empty(t *testing.T) {
	t.Parallel()
	o, err := NewOllama(cfgFor("http://127.0.0.1:1"), nil)
	require.NoError(t, err)
	vecs, err := o.Embed(context.Background(), memory.EmbedQuery, nil)
	require.NoError(t, err)
	assert.Empty(t, vecs)
}
