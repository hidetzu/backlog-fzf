package syncer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hidetzu/backlog-fzf/internal/backlog"
	"github.com/hidetzu/backlog-fzf/internal/embed"
	"github.com/hidetzu/backlog-fzf/internal/index"
)

// fakeEmbedder returns a deterministic vector per text and records the
// texts it was asked for. failAfter > 0 makes the (failAfter+1)-th call fail.
type fakeEmbedder struct {
	calls     [][]string
	dims      int
	failAfter int
	err       error
}

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.calls = append(f.calls, append([]string(nil), texts...))
	if f.failAfter > 0 && len(f.calls) > f.failAfter {
		return nil, f.err
	}
	dims := f.dims
	if dims == 0 {
		dims = 3
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, dims)
		v[0] = float32(len(t))
		v[1] = 1
		out[i] = v
	}
	return out, nil
}

func (f *fakeEmbedder) texts() int {
	n := 0
	for _, c := range f.calls {
		n += len(c)
	}
	return n
}

func embedDB(t *testing.T) *index.DB {
	t.Helper()
	db, err := index.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	issues := []backlog.Issue{
		{ID: 1, Key: "P-1", ProjectKey: "P", Summary: "login fails", Description: "after migration", CreatedAt: now, UpdatedAt: now},
		{ID: 2, Key: "P-2", ProjectKey: "P", Summary: "slow export", CreatedAt: now, UpdatedAt: now},
	}
	if err := db.UpsertIssues(context.Background(), issues); err != nil {
		t.Fatal(err)
	}
	docs := []backlog.Document{
		{ID: "d1", ProjectKey: "P", Title: "guide", Body: "para one\n\npara two", CreatedAt: now, UpdatedAt: now},
	}
	if err := db.UpsertDocuments(context.Background(), docs); err != nil {
		t.Fatal(err)
	}
	return db
}

func baseOpts() EmbedOptions {
	return EmbedOptions{
		Model: "m",
		Text:  embed.TextOptions{MaxIssueChars: 100, ChunkChars: 8, MaxChunks: 8},
	}
}

func TestEmbed_InitialBuildThenNoop(t *testing.T) {
	db := embedDB(t)
	ctx := context.Background()
	fe := &fakeEmbedder{}
	var stages []ProgressStage
	opts := baseOpts()
	opts.OnProgress = func(s ProgressStage, _ int) { stages = append(stages, s) }
	if err := Embed(ctx, fe, db, opts); err != nil {
		t.Fatal(err)
	}
	// 2 issues + doc split into 2 chunks ("para one" / "para two" at 8 runes) = 4 texts.
	if fe.texts() != 4 {
		t.Errorf("embedded %d texts, want 4 (calls=%v)", fe.texts(), fe.calls)
	}
	if n, _ := db.CountEmbeddings(ctx); n != 4 {
		t.Errorf("stored %d vectors, want 4", n)
	}
	meta, _ := db.EmbeddingMeta(ctx)
	if meta == nil || meta.Model != "m" || meta.Dimensions != 3 {
		t.Errorf("meta = %+v, want m/3 (recorded from the response)", meta)
	}
	if stages[0] != StageTotal || stages[len(stages)-1] != StageDone {
		t.Errorf("stages = %v", stages)
	}

	// Second run: nothing changed → embedder not called.
	fe2 := &fakeEmbedder{}
	if err := Embed(ctx, fe2, db, opts); err != nil {
		t.Fatal(err)
	}
	if len(fe2.calls) != 0 {
		t.Errorf("unchanged index should not call the embedder, got %v", fe2.calls)
	}
}

func TestEmbed_OnlyChangedRecordsAreReembedded(t *testing.T) {
	db := embedDB(t)
	ctx := context.Background()
	opts := baseOpts()
	if err := Embed(ctx, &fakeEmbedder{}, db, opts); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	// Change issue 2's text; upsert issue 1 with identical text (hash unchanged).
	if err := db.UpsertIssues(ctx, []backlog.Issue{
		{ID: 1, Key: "P-1", ProjectKey: "P", Summary: "login fails", Description: "after migration", CreatedAt: now, UpdatedAt: now},
		{ID: 2, Key: "P-2", ProjectKey: "P", Summary: "slow export", Description: "now with details", CreatedAt: now, UpdatedAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	fe := &fakeEmbedder{}
	if err := Embed(ctx, fe, db, opts); err != nil {
		t.Fatal(err)
	}
	if fe.texts() != 1 || len(fe.calls[0]) != 1 || fe.calls[0][0] != "title: slow export | text: now with details" {
		t.Errorf("expected exactly issue 2 to be re-embedded, got %v", fe.calls)
	}
}

func TestEmbed_ResumesAfterMidRunFailure(t *testing.T) {
	db := embedDB(t)
	ctx := context.Background()
	opts := baseOpts()
	opts.BatchSize = 1
	boom := errors.New("boom")
	fe := &fakeEmbedder{failAfter: 2, err: boom}
	err := Embed(ctx, fe, db, opts)
	if !errors.Is(err, boom) {
		t.Fatalf("expected the embedder error, got %v", err)
	}
	// Two batches of one were committed before the failure.
	if n, _ := db.CountEmbeddings(ctx); n != 2 {
		t.Errorf("committed vectors = %d, want 2", n)
	}
	// Resume: only the remaining 2 texts are sent.
	fe2 := &fakeEmbedder{}
	if err := Embed(ctx, fe2, db, opts); err != nil {
		t.Fatal(err)
	}
	if fe2.texts() != 2 {
		t.Errorf("resume embedded %d texts, want 2", fe2.texts())
	}
	if n, _ := db.CountEmbeddings(ctx); n != 4 {
		t.Errorf("after resume = %d, want 4", n)
	}
}

func TestEmbed_RebuildAndMismatch(t *testing.T) {
	db := embedDB(t)
	ctx := context.Background()
	opts := baseOpts()
	if err := Embed(ctx, &fakeEmbedder{}, db, opts); err != nil {
		t.Fatal(err)
	}
	// Different model without rebuild → mismatch, nothing embedded.
	other := opts
	other.Model = "m2"
	fe := &fakeEmbedder{}
	if err := Embed(ctx, fe, db, other); !errors.Is(err, ErrEmbeddingMismatch) {
		t.Fatalf("want ErrEmbeddingMismatch, got %v", err)
	}
	if len(fe.calls) != 0 {
		t.Error("mismatch must be detected before calling the embedder")
	}
	// Configured dimensions differ from the index → mismatch.
	dims := opts
	dims.Dimensions = 5
	if err := Embed(ctx, &fakeEmbedder{}, db, dims); !errors.Is(err, ErrEmbeddingMismatch) {
		t.Errorf("want ErrEmbeddingMismatch for dimensions, got %v", err)
	}
	// Server returning a different size than configured → mismatch.
	cfg := opts
	cfg.Dimensions = 3
	bad := &fakeEmbedder{dims: 4}
	_ = db.ClearEmbeddings(ctx)
	if err := Embed(ctx, bad, db, cfg); !errors.Is(err, ErrEmbeddingMismatch) || !errors.Is(err, ErrDimensionsIgnored) {
		t.Errorf("want ErrEmbeddingMismatch and ErrDimensionsIgnored for server size, got %v", err)
	}
	// Rebuild with the new model succeeds and replaces everything.
	other.Rebuild = true
	fe3 := &fakeEmbedder{}
	if err := Embed(ctx, fe3, db, other); err != nil {
		t.Fatal(err)
	}
	if fe3.texts() != 4 {
		t.Errorf("rebuild embedded %d texts, want 4", fe3.texts())
	}
	meta, _ := db.EmbeddingMeta(ctx)
	if meta == nil || meta.Model != "m2" {
		t.Errorf("meta after rebuild = %+v", meta)
	}
}

func TestEmbed_DeletesOrphansAndKeepsWatermark(t *testing.T) {
	db := embedDB(t)
	ctx := context.Background()
	wm := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	if err := db.SetWatermark(ctx, issuesScope, wm); err != nil {
		t.Fatal(err)
	}
	_ = db.EnsureEmbeddingSchema(ctx)
	_ = db.UpsertEmbeddings(ctx, []index.EmbeddingRecord{{Kind: index.KindIssue, RefID: 999, Chunks: []index.Chunk{{Index: 0, Hash: "x", Vec: []float32{1, 0, 0}}}}})
	_ = db.SetEmbeddingMeta(ctx, index.EmbeddingMeta{Model: "m", Dimensions: 3})
	if err := Embed(ctx, &fakeEmbedder{}, db, baseOpts()); err != nil {
		t.Fatal(err)
	}
	hashes, _ := db.EmbeddingHashes(ctx, index.KindIssue)
	if _, still := hashes[999]; still {
		t.Error("orphan vector should have been deleted")
	}
	got, _ := db.Watermark(ctx, issuesScope)
	if !got.Equal(wm) {
		t.Errorf("watermark changed: %v → %v", wm, got)
	}
}
