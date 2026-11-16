package adminops_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/adminops"
	"github.com/constantine-kostikas/memorable/internal/memory"
	"github.com/constantine-kostikas/memorable/internal/scan"
)

type fakeAdmin struct {
	inserted      []*memory.Memory
	insertedVecs  [][]float32
	updatedPatch  memory.Patch
	updatedVec    []float32
	updateCalls   int
	profile       []memory.Memory
	pruneBefore   time.Time
	pruneCountArg time.Time
	approved      []uuid.UUID
	purged        []uuid.UUID
	deleted       []uuid.UUID
}

func (f *fakeAdmin) QuarantinedList(context.Context) ([]memory.Memory, error) { return nil, nil }

func (f *fakeAdmin) Approve(_ context.Context, id uuid.UUID) error {
	f.approved = append(f.approved, id)
	return nil
}

func (f *fakeAdmin) Purge(_ context.Context, id uuid.UUID) error {
	f.purged = append(f.purged, id)
	return nil
}

func (f *fakeAdmin) PruneCount(_ context.Context, before time.Time) (int64, error) {
	f.pruneCountArg = before
	return 7, nil
}

func (f *fakeAdmin) Prune(_ context.Context, before time.Time) (int64, error) {
	f.pruneBefore = before
	return 3, nil
}

func (f *fakeAdmin) ProfileInsert(_ context.Context, m *memory.Memory, vec []float32, _ string) error {
	f.inserted = append(f.inserted, m)
	f.insertedVecs = append(f.insertedVecs, vec)
	return nil
}

func (f *fakeAdmin) ProfileUpdate(_ context.Context, id uuid.UUID, p memory.Patch, vec []float32, _ string) (memory.Memory, error) {
	f.updateCalls++
	f.updatedPatch, f.updatedVec = p, vec
	return memory.Memory{ID: id, Version: 2}, nil
}

func (f *fakeAdmin) ProfileDelete(_ context.Context, id uuid.UUID) error {
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeAdmin) ProfileList(context.Context) ([]memory.Memory, error) { return f.profile, nil }

type fakeEmbedder struct {
	err   error
	calls int
}

func (e *fakeEmbedder) Embed(_ context.Context, _ memory.EmbedKind, _ []string) ([][]float32, error) {
	e.calls++
	if e.err != nil {
		return nil, e.err
	}
	return [][]float32{{1, 2, 3}}, nil
}
func (e *fakeEmbedder) Model() string   { return "fake" }
func (e *fakeEmbedder) Dimensions() int { return 3 }

func newScanner(t *testing.T) memory.SecretScanner {
	t.Helper()
	sec, err := scan.NewSecretScanner()
	require.NoError(t, err)
	return sec
}

func validInput() adminops.ProfileInput {
	return adminops.ProfileInput{Title: "Spelling", Content: "Use US spelling.", Tags: []string{"style"}, Type: memory.TypeUser, Pinned: true}
}

func TestProfileAddRejectsSecret(t *testing.T) {
	t.Parallel()
	fa := &fakeAdmin{}
	emb := &fakeEmbedder{}
	ops := adminops.New(fa, emb, newScanner(t), 90, time.Now)

	in := validInput()
	in.Content = "token ghp_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789"
	_, err := ops.ProfileAdd(context.Background(), in)

	require.ErrorIs(t, err, memory.ErrSecretDetected)
	assert.Empty(t, fa.inserted)
	assert.Zero(t, emb.calls, "secrets are never sent to the embedder")
}

func TestProfileAddWithoutEmbedder(t *testing.T) {
	t.Parallel()
	fa := &fakeAdmin{}
	ops := adminops.New(fa, nil, newScanner(t), 90, time.Now)

	got, err := ops.ProfileAdd(context.Background(), validInput())

	require.NoError(t, err)
	require.Len(t, fa.inserted, 1)
	assert.Nil(t, fa.insertedVecs[0])
	assert.Nil(t, got.EmbeddingModel)
	assert.Equal(t, got.ID, fa.inserted[0].ID)
	assert.Equal(t, memory.SourceUserStated, fa.inserted[0].Source)
	assert.Equal(t, memory.ContentHash("Spelling", "Use US spelling."), fa.inserted[0].ContentHash)
}

func TestProfileAddIgnoresEmbedFailure(t *testing.T) {
	t.Parallel()
	fa := &fakeAdmin{}
	ops := adminops.New(fa, &fakeEmbedder{err: errors.New("ollama down")}, newScanner(t), 90, time.Now)

	_, err := ops.ProfileAdd(context.Background(), validInput())

	require.NoError(t, err)
	require.Len(t, fa.inserted, 1)
	assert.Nil(t, fa.insertedVecs[0])
}

func TestProfileAddEmbeds(t *testing.T) {
	t.Parallel()
	fa := &fakeAdmin{}
	ops := adminops.New(fa, &fakeEmbedder{}, newScanner(t), 90, time.Now)

	got, err := ops.ProfileAdd(context.Background(), validInput())

	require.NoError(t, err)
	assert.Equal(t, []float32{1, 2, 3}, fa.insertedVecs[0])
	require.NotNil(t, got.EmbeddingModel)
	assert.Equal(t, "fake", *got.EmbeddingModel)
}

func TestProfileAddValidates(t *testing.T) {
	t.Parallel()
	fa := &fakeAdmin{}
	ops := adminops.New(fa, nil, newScanner(t), 90, time.Now)

	in := validInput()
	in.Title = ""
	_, err := ops.ProfileAdd(context.Background(), in)

	require.ErrorIs(t, err, memory.ErrValidation)
	assert.Empty(t, fa.inserted)
}

func TestProfileEdit(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	cur := memory.Memory{ID: id, Title: "Spelling", Content: "Use US spelling.", Tags: []string{"style"}, Type: memory.TypeUser, Pinned: true}

	tests := []struct {
		name       string
		mutate     func(*adminops.ProfileInput)
		wantUpdate bool
		wantEmbed  bool
	}{
		{name: "unchanged input skips the update", mutate: func(*adminops.ProfileInput) {}},
		{name: "pin change keeps the embedding", mutate: func(in *adminops.ProfileInput) { in.Pinned = false }, wantUpdate: true},
		{name: "content change re-embeds", mutate: func(in *adminops.ProfileInput) { in.Content = "Use US spelling always." }, wantUpdate: true, wantEmbed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fa := &fakeAdmin{profile: []memory.Memory{cur}}
			ops := adminops.New(fa, &fakeEmbedder{}, newScanner(t), 90, time.Now)
			in := validInput()
			tt.mutate(&in)

			got, err := ops.ProfileEdit(context.Background(), id, in)

			require.NoError(t, err)
			assert.Equal(t, id, got.ID)
			assert.Equal(t, tt.wantUpdate, fa.updateCalls == 1)
			assert.Equal(t, tt.wantEmbed, fa.updatedVec != nil)
		})
	}
}

func TestProfileEditUnknownID(t *testing.T) {
	t.Parallel()
	ops := adminops.New(&fakeAdmin{}, nil, newScanner(t), 90, time.Now)

	_, err := ops.ProfileEdit(context.Background(), uuid.New(), validInput())

	require.ErrorIs(t, err, memory.ErrNotFound)
}

func TestProfileEditRejectsSecret(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	fa := &fakeAdmin{profile: []memory.Memory{{ID: id, Title: "Spelling", Content: "x", Type: memory.TypeUser}}}
	ops := adminops.New(fa, nil, newScanner(t), 90, time.Now)

	in := validInput()
	in.Content = "token ghp_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789"
	_, err := ops.ProfileEdit(context.Background(), id, in)

	require.ErrorIs(t, err, memory.ErrSecretDetected)
	assert.Zero(t, fa.updateCalls)
}

func TestPruneUsesRetention(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	fa := &fakeAdmin{}
	ops := adminops.New(fa, nil, newScanner(t), 90, func() time.Time { return now })
	want := now.Add(-90 * 24 * time.Hour)

	n, err := ops.Prune(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(3), n)
	assert.Equal(t, want, fa.pruneBefore)

	n, err = ops.PruneCount(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(7), n)
	assert.Equal(t, want, fa.pruneCountArg)
}

func TestDelegates(t *testing.T) {
	t.Parallel()
	fa := &fakeAdmin{}
	ops := adminops.New(fa, nil, newScanner(t), 90, time.Now)
	id := uuid.New()
	ctx := context.Background()

	require.NoError(t, ops.Approve(ctx, id))
	require.NoError(t, ops.Purge(ctx, id))
	require.NoError(t, ops.ProfileDelete(ctx, id))

	assert.Equal(t, []uuid.UUID{id}, fa.approved)
	assert.Equal(t, []uuid.UUID{id}, fa.purged)
	assert.Equal(t, []uuid.UUID{id}, fa.deleted)
}
