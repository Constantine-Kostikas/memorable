//go:build eval

package memory_test

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/config"
	"github.com/constantine-kostikas/memorable/internal/embed"
	"github.com/constantine-kostikas/memorable/internal/memory"
	"github.com/constantine-kostikas/memorable/internal/scan"
	"github.com/constantine-kostikas/memorable/internal/scope"
	"github.com/constantine-kostikas/memorable/internal/store"
	"github.com/constantine-kostikas/memorable/internal/store/storetest"
)

var (
	evalModels        = flag.String("eval.models", "embeddinggemma", "comma-separated Ollama embedding models to evaluate")
	evalMinSimilarity = flag.Float64("eval.min_similarity", 0, "override min_similarity (0 keeps the config default)")
	evalVerbose       = flag.Bool("eval.verbose", false, "log every miss")
)

const defaultModel = "embeddinggemma"

var projects = map[string]string{"memorable": "github.com/constantine-kostikas/memorable", "shop": "github.com/acme/shop"}

type evalMemory struct {
	Slug    string `json:"slug"`
	Project string `json:"project"`
	Type    string `json:"type"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

type evalQuery struct {
	Query    string   `json:"query"`
	Project  string   `json:"project"`
	Relevant []string `json:"relevant"`
}

type evalResult struct {
	Model             string   `json:"model"`
	RecallAt5         float64  `json:"recall_at_5"`
	MRR               float64  `json:"mrr"`
	NoAnswerFalsePos  float64  `json:"no_answer_false_positive_rate"`
	Misses            []string `json:"misses"`
	NoAnswerResponses []string `json:"no_answer_responses"`
}

func loadJSON(t *testing.T, name string, v any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "eval", name))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, v))
}

// probeDims asks Ollama for one embedding; ok is false when the model is not available.
func probeDims(model string) (int, bool) {
	body, _ := json.Marshal(map[string]any{"model": model, "input": []string{"probe"}})
	resp, err := http.Post("http://127.0.0.1:11434/api/embed", "application/json", bytes.NewReader(body))
	if err != nil {
		return 0, false
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&out) != nil || len(out.Embeddings) == 0 {
		return 0, false
	}
	return len(out.Embeddings[0]), true
}

func projectPtr(name string) *string {
	if p, ok := projects[name]; ok {
		return &p
	}
	return nil
}

func TestEval(t *testing.T) { //nolint:paralleltest // one heavy, sequential evaluation against the local GPU
	var mems []evalMemory
	var queries []evalQuery
	loadJSON(t, "memories.json", &mems)
	loadJSON(t, "queries.json", &queries)
	require.Len(t, mems, 50)
	require.GreaterOrEqual(t, len(queries), 100)

	var results []evalResult
	for _, model := range strings.Split(*evalModels, ",") {
		model = strings.TrimSpace(model)
		dims, ok := probeDims(model)
		if !ok {
			t.Logf("skipping %s: not available in local Ollama (ollama pull %s)", model, model)
			continue
		}
		r := evaluate(t, model, dims, mems, queries)
		results = append(results, r)
		t.Logf("%-24s recall@5=%.3f  MRR=%.3f  no-answer false positives=%.2f", r.Model, r.RecallAt5, r.MRR, r.NoAnswerFalsePos)
		if *evalVerbose {
			for _, m := range r.Misses {
				t.Logf("  miss: %s", m)
			}
			for _, m := range r.NoAnswerResponses {
				t.Logf("  no-answer hit: %s", m)
			}
		}
		if model == defaultModel {
			assert.GreaterOrEqual(t, r.RecallAt5, 0.80, "recall@5")
			assert.GreaterOrEqual(t, r.MRR, 0.60, "MRR")
			assert.LessOrEqual(t, r.NoAnswerFalsePos, 0.30, "no-answer false positive rate")
		}
	}
	out := filepath.Join(t.TempDir(), "eval-results.json")
	b, err := json.MarshalIndent(results, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(out, b, 0o600))
	t.Logf("results written to %s", out)
}

func evaluate(t *testing.T, model string, dims int, mems []evalMemory, queries []evalQuery) evalResult {
	t.Helper()
	ctx := context.Background()
	cfg := config.Default()
	cfg.Contexts = map[string]config.Context{"personal": {Paths: []string{"/p"}}}
	cfg.Embedding.Model, cfg.Embedding.Dimensions = model, dims
	if *evalMinSimilarity > 0 {
		cfg.Search.MinSimilarity = *evalMinSimilarity
	}
	db := storetest.NewDatabase(t)
	require.NoError(t, storetest.Bootstrap(ctx, t, db, &cfg))
	st, err := store.Open(ctx, storetest.DatabaseConfig(db), storetest.AppPassword, "mem_personal", "dev")
	require.NoError(t, err)
	t.Cleanup(st.Close)
	emb, err := embed.NewOllama(cfg.Embedding, nil)
	require.NoError(t, err)
	sec, err := scan.NewSecretScanner()
	require.NoError(t, err)
	service := func(project string) *memory.Service {
		return memory.NewService(st, st, emb, sec, scan.Injection, memory.Settings{
			Owner: "dev", Scope: scope.Resolution{Context: "personal", Project: projectPtr(project)},
			Search: cfg.Search, Preload: cfg.Preload, WritesPerMinute: 1000,
		}, memory.WithoutRateLimit())
	}

	for _, m := range mems {
		typ, err := memory.ParseType(m.Type)
		require.NoError(t, err)
		slug := m.Slug
		res, err := service(m.Project).Write(ctx, []memory.NewMemory{{Title: m.Title, Content: m.Content, Type: typ,
			Source: memory.SourceUserStated, Slug: &slug, Global: m.Project == "", OnSimilar: memory.OnSimilarKeepBoth}})
		require.NoError(t, err)
		require.Equal(t, memory.OutcomeCreated, res[0].Outcome, "%s: %s", m.Slug, res[0].Reason)
	}

	r := evalResult{Model: model}
	var recallSum, rrSum float64
	answerable, noAnswer, noAnswerHits := 0, 0, 0
	for _, q := range queries {
		res, err := service(q.Project).Search(ctx, memory.SearchRequest{Query: q.Query, K: 10})
		require.NoError(t, err, q.Query)
		var got []string
		for _, s := range res.Results {
			if s.Slug != nil {
				got = append(got, *s.Slug)
			}
		}
		if len(q.Relevant) == 0 {
			noAnswer++
			if len(got) > 0 {
				noAnswerHits++
				r.NoAnswerResponses = append(r.NoAnswerResponses, fmt.Sprintf("%q -> %v", q.Query, got[:min(3, len(got))]))
			}
			continue
		}
		answerable++
		top5 := got[:min(5, len(got))]
		found := 0
		for _, rel := range q.Relevant {
			if slices.Contains(top5, rel) {
				found++
			}
		}
		recallSum += float64(found) / float64(len(q.Relevant))
		for i, g := range got {
			if slices.Contains(q.Relevant, g) {
				rrSum += 1 / float64(i+1)
				break
			}
		}
		if found < len(q.Relevant) {
			r.Misses = append(r.Misses, fmt.Sprintf("%q want %v got %v", q.Query, q.Relevant, top5))
		}
	}
	r.RecallAt5 = recallSum / float64(answerable)
	r.MRR = rrSum / float64(answerable)
	r.NoAnswerFalsePos = float64(noAnswerHits) / float64(noAnswer)
	return r
}
