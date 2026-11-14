// Package worker computes pending embeddings in the background.
package worker

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

const (
	batch       = 16
	pollEvery   = 5 * time.Second
	baseBackoff = 5 * time.Second
	maxBackoff  = time.Hour
	maxAttempts = 10
)

// EmbeddingStore saves computed embeddings.
type EmbeddingStore interface {
	SetEmbedding(ctx context.Context, id uuid.UUID, version int, vec []float32, model string) error
}

// Worker drains the embedding job queue.
type Worker struct {
	jobs   memory.JobRepository
	store  EmbeddingStore
	emb    memory.Embedder
	logger *slog.Logger
}

// New returns a Worker.
func New(jobs memory.JobRepository, store EmbeddingStore, emb memory.Embedder, logger *slog.Logger) *Worker {
	return &Worker{jobs: jobs, store: store, emb: emb, logger: logger}
}

func backoff(attempts int) time.Duration {
	d := baseBackoff
	for range attempts {
		d *= 2
		if d >= maxBackoff {
			return maxBackoff
		}
	}
	return d
}

// Run processes jobs until ctx is canceled; it returns nil on cancellation.
func (w *Worker) Run(ctx context.Context) error {
	for {
		n, err := w.RunOnce(ctx)
		if ctx.Err() != nil {
			return nil //nolint:nilerr // cancellation is the normal way to stop the worker
		}
		if err != nil {
			w.logger.WarnContext(ctx, "embedding worker cycle failed", "error", err)
		}
		if n > 0 && err == nil {
			continue
		}
		timer := time.NewTimer(pollEvery)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

// RunOnce claims and processes one batch, returning how many jobs it handled.
func (w *Worker) RunOnce(ctx context.Context) (int, error) {
	jobs, err := w.jobs.ClaimJobs(ctx, batch, time.Now())
	if err != nil || len(jobs) == 0 {
		return 0, err //nolint:wrapcheck // repository errors are already contextual
	}
	texts := make([]string, 0, len(jobs))
	for _, j := range jobs {
		texts = append(texts, memory.DocumentText(j.Title, j.Content))
	}
	vecs, embedErr := w.emb.Embed(ctx, memory.EmbedDocument, texts)
	var errs []error
	for i, j := range jobs {
		if embedErr == nil {
			err := w.store.SetEmbedding(ctx, j.MemoryID, j.Version, vecs[i], w.emb.Model())
			// A stale version is done too: completing it by version leaves any newer job queued.
			if err == nil || errors.Is(err, memory.ErrConflict) {
				errs = append(errs, w.jobs.CompleteJob(ctx, j.MemoryID, j.Version))
				continue
			}
		}
		reason := "embedding failed"
		if embedErr != nil {
			reason = embedErr.Error()
		}
		giveUp := j.Attempts+1 >= maxAttempts
		errs = append(errs, w.jobs.FailJob(ctx, j.MemoryID, j.Version, reason, time.Now().Add(backoff(j.Attempts)), giveUp))
	}
	return len(jobs), errors.Join(errs...)
}
