package worker

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/constantine-kostikas/memorable/internal/memory"
	"github.com/constantine-kostikas/memorable/internal/memory/memorytest"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		// The testcontainers reaper connection lives for the whole test process in integration runs.
		goleak.IgnoreTopFunction("github.com/testcontainers/testcontainers-go.(*Reaper).connect.func1"),
	)
}

type fakeJobs struct {
	mu        sync.Mutex
	jobs      map[uuid.UUID]*memory.Job
	next      map[uuid.UUID]time.Time
	done      map[uuid.UUID]bool
	gaveUp    map[uuid.UUID]bool
	failTimes []time.Duration
	embedded  map[uuid.UUID]int
}

func newFakeJobs(n int) *fakeJobs {
	f := &fakeJobs{jobs: map[uuid.UUID]*memory.Job{}, next: map[uuid.UUID]time.Time{}, done: map[uuid.UUID]bool{},
		gaveUp: map[uuid.UUID]bool{}, embedded: map[uuid.UUID]int{}}
	for range n {
		id := uuid.Must(uuid.NewV7())
		f.jobs[id] = &memory.Job{MemoryID: id, Version: 1, Title: "t", Content: id.String()}
	}
	return f
}

func (f *fakeJobs) EnqueueEmbedding(context.Context, uuid.UUID, int) error { return nil }

func (f *fakeJobs) ClaimJobs(_ context.Context, n int, now time.Time) ([]memory.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []memory.Job
	for id, j := range f.jobs {
		if f.done[id] || f.next[id].After(now) || len(out) == n {
			continue
		}
		f.next[id] = now.Add(5 * time.Minute)
		out = append(out, *j)
	}
	return out, nil
}

func (f *fakeJobs) CompleteJob(_ context.Context, id uuid.UUID, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.done[id] = true
	return nil
}

func (f *fakeJobs) FailJob(_ context.Context, id uuid.UUID, _ int, _ string, next time.Time, giveUp bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failTimes = append(f.failTimes, time.Until(next))
	f.jobs[id].Attempts++
	f.next[id] = next
	if giveUp {
		f.done[id], f.gaveUp[id] = true, true
	}
	return nil
}

func (f *fakeJobs) SetEmbedding(_ context.Context, id uuid.UUID, _ int, _ []float32, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.embedded[id]++
	return nil
}

func (f *fakeJobs) allDone() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.done) == len(f.jobs)
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestWorker_ProcessesJobs(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		jobs := newFakeJobs(40)
		w := New(jobs, jobs, memorytest.NewEmbedder(), quiet())
		ctx, cancel := context.WithCancel(t.Context())
		errc := make(chan error, 1)
		go func() { errc <- w.Run(ctx) }()
		time.Sleep(time.Second)
		synctest.Wait()
		assert.True(t, jobs.allDone())
		for id := range jobs.jobs {
			assert.Equal(t, 1, jobs.embedded[id])
		}
		cancel()
		require.NoError(t, <-errc)
	})
}

func TestWorker_BackoffSchedule(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		jobs := newFakeJobs(1)
		emb := memorytest.NewEmbedder()
		emb.Down = true
		w := New(jobs, jobs, emb, quiet())
		ctx, cancel := context.WithCancel(t.Context())
		errc := make(chan error, 1)
		go func() { errc <- w.Run(ctx) }()
		time.Sleep(48 * time.Hour)
		synctest.Wait()
		cancel()
		require.NoError(t, <-errc)

		want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second,
			160 * time.Second, 320 * time.Second, 640 * time.Second, 1280 * time.Second, 2560 * time.Second}
		require.Len(t, jobs.failTimes, 10, "gives up after 10 attempts")
		assert.Equal(t, want[:9], jobs.failTimes[:9])
		assert.Len(t, jobs.gaveUp, 1)
	})
}

func TestBackoff_Capped(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 5*time.Second, backoff(0))
	assert.Equal(t, time.Hour, backoff(20))
}

func TestWorker_StopsOnCancel(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		w := New(newFakeJobs(0), newFakeJobs(0), memorytest.NewEmbedder(), quiet())
		ctx, cancel := context.WithCancel(t.Context())
		errc := make(chan error, 1)
		go func() { errc <- w.Run(ctx) }()
		time.Sleep(time.Minute)
		cancel()
		require.NoError(t, <-errc)
	})
}

type recordingEmbedder struct {
	*memorytest.Embedder
	texts []string
}

func (r *recordingEmbedder) Embed(ctx context.Context, kind memory.EmbedKind, texts []string) ([][]float32, error) {
	r.texts = append(r.texts, texts...)
	return r.Embedder.Embed(ctx, kind, texts) //nolint:wrapcheck // test double
}

func TestWorker_EmbedsJobTags(t *testing.T) {
	t.Parallel()
	jobs := newFakeJobs(1)
	for _, j := range jobs.jobs {
		j.Tags = []string{"ci", "go"}
	}
	emb := &recordingEmbedder{Embedder: memorytest.NewEmbedder()}
	w := New(jobs, jobs, emb, quiet())

	n, err := w.RunOnce(context.Background())

	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Len(t, emb.texts, 1)
	assert.Contains(t, emb.texts[0], "| tags: ci, go |")
}
