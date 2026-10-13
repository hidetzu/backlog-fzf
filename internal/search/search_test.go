package search

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hidetzu/backlog-fzf/internal/backlog"
	"github.com/hidetzu/backlog-fzf/internal/config"
	"github.com/hidetzu/backlog-fzf/internal/embed"
	"github.com/hidetzu/backlog-fzf/internal/index"
	"github.com/hidetzu/backlog-fzf/internal/syncer"
)

// fakeEmbedder maps exact texts to vectors; unknown texts get a vector
// orthogonal to everything else.
type fakeEmbedder struct {
	vectors map[string][]float32
	calls   int
}

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.calls++
	out := make([][]float32, len(texts))
	for i, t := range texts {
		if v, ok := f.vectors[t]; ok {
			out[i] = append([]float32(nil), v...)
		} else {
			out[i] = []float32{0, 0, 1}
		}
	}
	return out, nil
}

var now = time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

func fixture(t *testing.T) (*index.DB, *config.Embedding, *fakeEmbedder) {
	t.Helper()
	db, err := index.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	issues := []backlog.Issue{
		{ID: 1, Key: "P-1", ProjectKey: "P", Summary: "login fails on Safari", CreatedAt: now, UpdatedAt: now},
		{ID: 2, Key: "P-2", ProjectKey: "P", Summary: "slow export", CreatedAt: now, UpdatedAt: now.Add(time.Hour)},
		{ID: 3, Key: "P-3", ProjectKey: "P", Summary: "Safari rendering glitch", CreatedAt: now, UpdatedAt: now.Add(2 * time.Hour)},
	}
	if err := db.UpsertIssues(ctx, issues); err != nil {
		t.Fatal(err)
	}
	docs := []backlog.Document{
		{ID: "d1", ProjectKey: "P", Title: "setup guide", CreatedAt: now, UpdatedAt: now.Add(3 * time.Hour)},
	}
	if err := db.UpsertDocuments(ctx, docs); err != nil {
		t.Fatal(err)
	}
	ec := config.Embedding{Model: "m", MaxIssueChars: 100, ChunkChars: 1000, MaxChunks: 8}.WithDefaults()
	fe := &fakeEmbedder{vectors: map[string][]float32{
		"title: login fails on Safari | text: ":   {1, 0, 0},
		"title: slow export | text: ":             {0, 1, 0},
		"title: Safari rendering glitch | text: ": {0.3, 0.3, 0},
		"title: setup guide | text: ":             {0.9, 0.1, 0},
		*ec.QueryPrefix + "cannot sign in":        {1, 0.05, 0},
		*ec.QueryPrefix + "Safari":                {1, 0.05, 0},
	}}
	err = syncer.Embed(ctx, fe, db, syncer.EmbedOptions{Model: ec.Model, Text: embed.TextOptions{MaxIssueChars: 100, ChunkChars: 1000, MaxChunks: 8}})
	if err != nil {
		t.Fatal(err)
	}
	return db, &ec, fe
}

func refs(hits []Hit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.Ref()
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestKeyword_MergesByUpdatedAt(t *testing.T) {
	db, _, _ := fixture(t)
	hits, err := Keyword(context.Background(), db, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := refs(hits); !equal(got, []string{"doc:d1", "issue:P-3", "issue:P-2", "issue:P-1"}) {
		t.Errorf("recent order = %v", got)
	}
	hits, _ = Keyword(context.Background(), db, "Safari", 10)
	if got := refs(hits); !equal(got, []string{"issue:P-3", "issue:P-1"}) {
		t.Errorf("keyword Safari = %v", got)
	}
	if hits[0].Score != 0 {
		t.Error("keyword hits carry no score")
	}
}

func TestSemantic_RanksByCosineAcrossKinds(t *testing.T) {
	db, ec, fe := fixture(t)
	hits, err := Semantic(context.Background(), db, fe, ec, "cannot sign in", 10)
	if err != nil {
		t.Fatal(err)
	}
	got := refs(hits)
	if len(got) != 4 || got[0] != "issue:P-1" || got[1] != "doc:d1" {
		t.Errorf("semantic order = %v", got)
	}
	if hits[0].Score <= hits[1].Score {
		t.Errorf("scores not descending: %v", hits)
	}
}

func TestSemantic_EmptyQueryIsRecentWithoutHTTP(t *testing.T) {
	db, ec, fe := fixture(t)
	before := fe.calls
	hits, err := Semantic(context.Background(), db, fe, ec, " ", 10)
	if err != nil {
		t.Fatal(err)
	}
	if fe.calls != before {
		t.Error("empty query must not call the embedder")
	}
	if got := refs(hits); got[0] != "doc:d1" {
		t.Errorf("expected recent order, got %v", got)
	}
}

func TestSemantic_IndexChecks(t *testing.T) {
	db, err := index.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ec := config.Embedding{Model: "m"}.WithDefaults()
	fe := &fakeEmbedder{}
	if _, err := Semantic(context.Background(), db, fe, &ec, "x", 5); !errors.Is(err, ErrNotBuilt) {
		t.Errorf("want ErrNotBuilt, got %v", err)
	}
	if _, err := Hybrid(context.Background(), db, fe, &ec, "x", 5); !errors.Is(err, ErrNotBuilt) {
		t.Errorf("hybrid: want ErrNotBuilt, got %v", err)
	}
	if fe.calls != 0 {
		t.Error("index checks must happen before any embedding call")
	}
	db2, ec2, fe2 := fixture(t)
	other := *ec2
	other.Model = "different"
	if _, err := Semantic(context.Background(), db2, fe2, &other, "x", 5); !errors.Is(err, ErrRebuild) {
		t.Errorf("want ErrRebuild, got %v", err)
	}
	bad := &fakeEmbedder{vectors: map[string][]float32{*ec2.QueryPrefix + "x": {1, 0}}}
	if _, err := Semantic(context.Background(), db2, bad, ec2, "x", 5); !errors.Is(err, ErrRebuild) {
		t.Errorf("query dimension mismatch: want ErrRebuild, got %v", err)
	}
}

func TestRRF_RecordsInBothListsWin(t *testing.T) {
	a := Hit{Kind: index.KindIssue, Issue: backlog.Issue{Key: "A", UpdatedAt: now}}
	b := Hit{Kind: index.KindIssue, Issue: backlog.Issue{Key: "B", UpdatedAt: now.Add(time.Hour)}}
	c := Hit{Kind: index.KindDocument, Document: backlog.Document{ID: "C", UpdatedAt: now}}
	fused := RRF([][]Hit{{a, b}, {c, b}}, 60)
	// B: 1/62 + 1/62; A and C: 1/61 each → tie broken by updated_at (equal) then Ref ("doc:C" < "issue:A").
	if got := refs(fused); !equal(got, []string{"issue:B", "doc:C", "issue:A"}) {
		t.Errorf("fused = %v", got)
	}
	if fused[0].Score <= fused[1].Score || fused[1].Score != fused[2].Score {
		t.Errorf("scores = %v %v %v", fused[0].Score, fused[1].Score, fused[2].Score)
	}
	if len(RRF(nil, 60)) != 0 {
		t.Error("no lists → no hits")
	}
}

func TestHybrid_BoostsRecordsFoundByBothSearches(t *testing.T) {
	db, ec, fe := fixture(t)
	// Keyword "Safari" → P-3, P-1 (by updated_at). Semantic → P-1, d1, P-3, P-2.
	// P-1 (semantic 1st, keyword 2nd) and P-3 (keyword 1st, semantic 3rd) are
	// in both lists and outrank d1, which semantic alone puts second.
	hits, err := Hybrid(context.Background(), db, fe, ec, "Safari", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := refs(hits); !equal(got, []string{"issue:P-1", "issue:P-3", "doc:d1", "issue:P-2"}) {
		t.Errorf("hybrid order = %v", got)
	}
	sem, _ := Semantic(context.Background(), db, fe, ec, "Safari", 10)
	if refs(sem)[1] != "doc:d1" {
		t.Errorf("semantic baseline changed: %v", refs(sem))
	}
	one, _ := Hybrid(context.Background(), db, fe, ec, "Safari", 1)
	if len(one) != 1 {
		t.Errorf("limit not applied: %v", refs(one))
	}
}
