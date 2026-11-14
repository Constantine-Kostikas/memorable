// Package embed computes embeddings with a local Ollama server.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/constantine-kostikas/memorable/internal/config"
	"github.com/constantine-kostikas/memorable/internal/memory"
)

const (
	batchSize      = 32
	requestTimeout = 30 * time.Second
	queryPrefix    = "task: search result | query: "
)

// Ollama calls POST /api/embed.
type Ollama struct {
	url    string
	model  string
	dims   int
	client *http.Client
}

var _ memory.Embedder = (*Ollama)(nil)

// NewOllama validates the config and returns an embedder; a nil client uses a default with a timeout.
func NewOllama(cfg config.Embedding, client *http.Client) (*Ollama, error) {
	if err := config.CheckEmbeddingURL(cfg); err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}
	return &Ollama{url: strings.TrimRight(cfg.URL, "/") + "/api/embed", model: cfg.Model, dims: cfg.Dimensions, client: client}, nil
}

// Model returns the configured model name.
func (o *Ollama) Model() string { return o.model }

// Dimensions returns the configured vector length.
func (o *Ollama) Dimensions() int { return o.dims }

// Embed returns one vector per text, batching requests.
func (o *Ollama) Embed(ctx context.Context, kind memory.EmbedKind, texts []string) ([][]float32, error) {
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += batchSize {
		end := min(start+batchSize, len(texts))
		batch := make([]string, 0, end-start)
		for _, t := range texts[start:end] {
			if kind == memory.EmbedQuery {
				t = queryPrefix + t
			}
			batch = append(batch, t)
		}
		vecs, err := o.call(ctx, batch)
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

func (o *Ollama) call(ctx context.Context, input []string) ([][]float32, error) {
	body, err := json.Marshal(map[string]any{"model": o.model, "input": input})
	if err != nil {
		return nil, fmt.Errorf("encoding embed request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building embed request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: ollama: %w", memory.ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: reading ollama response: %w", memory.ErrUnavailable, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: ollama returned %d: %s", memory.ErrUnavailable, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decoding ollama response: %w", err)
	}
	if len(parsed.Embeddings) != len(input) {
		return nil, fmt.Errorf("ollama returned %d embeddings for %d inputs", len(parsed.Embeddings), len(input))
	}
	for _, v := range parsed.Embeddings {
		if len(v) != o.dims {
			return nil, fmt.Errorf("ollama model %s returned %d dimensions, config expects %d", o.model, len(v), o.dims)
		}
	}
	return parsed.Embeddings, nil
}
