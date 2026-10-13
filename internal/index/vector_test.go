package index

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/hidetzu/backlog-fzf/internal/backlog"
)

func unit(xs ...float32) []float32 {
	v := append([]float32(nil), xs...)
	NormalizeVector(v)
	return v
}

func TestEmbeddingSchema_IsLazy(t *testing.T) {
	db := newMemDB(t)
	ctx := context.Background()
	ok, err := db.HasEmbeddingSchema(ctx)
	if err != nil || ok {
		t.Fatalf("fresh DB should have no embedding tables (ok=%v err=%v)", ok, err)
	}
	if _, err := db.SearchVectors(ctx, unit(1, 0), 5); !errors.Is(err, ErrNoEmbeddings) {
		t.Errorf("SearchVectors before schema: want ErrNoEmbeddings, got %v", err)
	}
	if m, err := db.EmbeddingMeta(ctx); err != nil || m != nil {
		t.Errorf("EmbeddingMeta before schema: want nil,nil got %v,%v", m, err)
	}
	if err := db.EnsureEmbeddingSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureEmbeddingSchema(ctx); err != nil {
		t.Fatalf("EnsureEmbeddingSchema should be idempotent: %v", err)
	}
	ok, _ = db.HasEmbeddingSchema(ctx)
	if !ok {
		t.Error("schema should exist after Ensure")
	}
}

func TestEmbeddingMeta_RoundTrip(t *testing.T) {
	db := newMemDB(t)
	ctx := context.Background()
	if err := db.EnsureEmbeddingSchema(ctx); err != nil {
		t.Fatal(err)
	}
	m, err := db.EmbeddingMeta(ctx)
	if err != nil || m != nil {
		t.Fatalf("empty meta: want nil, got %v (%v)", m, err)
	}
	if err := db.SetEmbeddingMeta(ctx, EmbeddingMeta{Model: "m", Dimensions: 3}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetEmbeddingMeta(ctx, EmbeddingMeta{Model: "m2", Dimensions: 4}); err != nil {
		t.Fatal(err)
	}
	m, err = db.EmbeddingMeta(ctx)
	if err != nil || m == nil || m.Model != "m2" || m.Dimensions != 4 {
		t.Errorf("meta = %+v, err=%v", m, err)
	}
}

func TestUpsertEmbeddings_ReplacesChunkSet(t *testing.T) {
	db := newMemDB(t)
	ctx := context.Background()
	if err := db.EnsureEmbeddingSchema(ctx); err != nil {
		t.Fatal(err)
	}
	rec := EmbeddingRecord{Kind: KindDocument, RefID: 7, Chunks: []Chunk{
		{0, "h0", unit(1, 0)}, {1, "h1", unit(0, 1)}, {2, "h2", unit(1, 1)},
	}}
	if err := db.UpsertEmbeddings(ctx, []EmbeddingRecord{rec}); err != nil {
		t.Fatal(err)
	}
	hashes, _ := db.EmbeddingHashes(ctx, KindDocument)
	if got := hashes[7]; len(got) != 3 || got[0] != "h0" || got[2] != "h2" {
		t.Fatalf("hashes = %v", got)
	}
	// Shrink to one chunk: stale chunk rows must disappear.
	rec.Chunks = rec.Chunks[:1]
	rec.Chunks[0].Hash = "h0b"
	if err := db.UpsertEmbeddings(ctx, []EmbeddingRecord{rec}); err != nil {
		t.Fatal(err)
	}
	hashes, _ = db.EmbeddingHashes(ctx, KindDocument)
	if got := hashes[7]; len(got) != 1 || got[0] != "h0b" {
		t.Errorf("after shrink hashes = %v, want [h0b]", got)
	}
	if n, _ := db.CountEmbeddings(ctx); n != 1 {
		t.Errorf("CountEmbeddings = %d, want 1", n)
	}
	if other, _ := db.EmbeddingHashes(ctx, KindIssue); len(other) != 0 {
		t.Errorf("issue hashes should be empty, got %v", other)
	}
}

func TestSearchVectors_AggregatesPerRecordAndRanks(t *testing.T) {
	db := newMemDB(t)
	ctx := context.Background()
	if err := db.EnsureEmbeddingSchema(ctx); err != nil {
		t.Fatal(err)
	}
	recs := []EmbeddingRecord{
		// doc 1: two chunks, chunk 1 is the near-perfect match.
		{KindDocument, 1, []Chunk{{0, "a", unit(0, 1)}, {1, "b", unit(1, 0.1)}}},
		// issue 2: a single chunk, slightly worse than doc 1's best.
		{KindIssue, 2, []Chunk{{0, "c", unit(1, 0.3)}}},
		// issue 3: orthogonal.
		{KindIssue, 3, []Chunk{{0, "d", unit(0, 1)}}},
	}
	if err := db.UpsertEmbeddings(ctx, recs); err != nil {
		t.Fatal(err)
	}
	hits, err := db.SearchVectors(ctx, unit(1, 0), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 3 {
		t.Fatalf("expected 3 aggregated hits (not 4 chunks), got %d: %+v", len(hits), hits)
	}
	if hits[0].Kind != KindDocument || hits[0].RefID != 1 || hits[0].Chunk != 1 {
		t.Errorf("top hit = %+v, want doc 1 via chunk 1", hits[0])
	}
	if hits[1].Kind != KindIssue || hits[1].RefID != 2 {
		t.Errorf("second hit = %+v, want issue 2", hits[1])
	}
	if math.Abs(float64(hits[2].Score)) > 1e-6 {
		t.Errorf("orthogonal hit score = %v, want ~0", hits[2].Score)
	}
	// k applies after aggregation.
	top1, _ := db.SearchVectors(ctx, unit(1, 0), 1)
	if len(top1) != 1 || top1[0].RefID != 1 {
		t.Errorf("k=1: %+v", top1)
	}
}

func TestSearchVectors_DimensionMismatch(t *testing.T) {
	db := newMemDB(t)
	ctx := context.Background()
	_ = db.EnsureEmbeddingSchema(ctx)
	_ = db.UpsertEmbeddings(ctx, []EmbeddingRecord{{KindIssue, 1, []Chunk{{0, "h", unit(1, 0, 0)}}}})
	_, err := db.SearchVectors(ctx, unit(1, 0), 5)
	if !errors.Is(err, ErrDimensionMismatch) {
		t.Errorf("want ErrDimensionMismatch, got %v", err)
	}
}

func TestDeleteOrphanEmbeddings_AndClear(t *testing.T) {
	db := newMemDB(t)
	ctx := context.Background()
	_ = db.EnsureEmbeddingSchema(ctx)
	if err := db.UpsertIssues(ctx, []backlog.Issue{issueWith(1, "P-1", "P", "s", "", "")}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertDocuments(ctx, []backlog.Document{{ID: "d1", ProjectKey: "P", Title: "t", CreatedAt: now, UpdatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	docs, _ := db.DocumentsByIDs(ctx, []int64{1})
	if len(docs) != 1 || docs[1].ID != "d1" {
		t.Fatalf("DocumentsByIDs = %v", docs)
	}
	recs := []EmbeddingRecord{
		{KindIssue, 1, []Chunk{{0, "a", unit(1)}}},
		{KindIssue, 99, []Chunk{{0, "b", unit(1)}}}, // orphan
		{KindDocument, 1, []Chunk{{0, "c", unit(1)}}},
		{KindDocument, 42, []Chunk{{0, "d", unit(1)}}}, // orphan
	}
	_ = db.UpsertEmbeddings(ctx, recs)
	n, err := db.DeleteOrphanEmbeddings(ctx)
	if err != nil || n != 2 {
		t.Fatalf("orphans deleted = %d (err %v), want 2", n, err)
	}
	if cnt, _ := db.CountEmbeddings(ctx); cnt != 2 {
		t.Errorf("remaining = %d, want 2", cnt)
	}
	_ = db.SetEmbeddingMeta(ctx, EmbeddingMeta{Model: "m", Dimensions: 1})
	if err := db.ClearEmbeddings(ctx); err != nil {
		t.Fatal(err)
	}
	if cnt, _ := db.CountEmbeddings(ctx); cnt != 0 {
		t.Errorf("after clear = %d", cnt)
	}
	if m, _ := db.EmbeddingMeta(ctx); m != nil {
		t.Errorf("meta should be cleared, got %+v", m)
	}
}

func TestIssuesByIDs_AndTexts(t *testing.T) {
	db := newMemDB(t)
	ctx := context.Background()
	i1 := issueWith(1, "P-1", "P", "first", "Open", "a")
	i1.Description = "desc"
	i2 := issueWith(2, "P-2", "P", "second", "Open", "b")
	if err := db.UpsertIssues(ctx, []backlog.Issue{i1, i2}); err != nil {
		t.Fatal(err)
	}
	got, err := db.IssuesByIDs(ctx, []int64{2, 1, 3})
	if err != nil || len(got) != 2 || got[1].Key != "P-1" || got[2].Key != "P-2" {
		t.Errorf("IssuesByIDs = %v, err=%v", got, err)
	}
	texts, err := db.AllIssueTexts(ctx)
	if err != nil || len(texts) != 2 || texts[0].ID != 1 || texts[0].Description != "desc" || texts[1].Summary != "second" {
		t.Errorf("AllIssueTexts = %+v, err=%v", texts, err)
	}
	if empty, _ := db.IssuesByIDs(ctx, nil); len(empty) != 0 {
		t.Error("nil ids should return an empty map")
	}
}

func TestVectorEncoding_RoundTripAndNormalize(t *testing.T) {
	v := []float32{3, 4}
	NormalizeVector(v)
	if math.Abs(float64(v[0])-0.6) > 1e-6 || math.Abs(float64(v[1])-0.8) > 1e-6 {
		t.Errorf("normalize = %v", v)
	}
	got := DecodeVector(EncodeVector(v))
	if len(got) != 2 || got[0] != v[0] || got[1] != v[1] {
		t.Errorf("round trip = %v, want %v", got, v)
	}
	zero := []float32{0, 0}
	NormalizeVector(zero)
	if zero[0] != 0 || zero[1] != 0 {
		t.Errorf("zero vector should stay zero, got %v", zero)
	}
}
