// Package search ranks issues and documents for a query. Three modes
// share one result type so the CLI, the TUI and the evaluation script
// format and score them the same way:
//
//   - Keyword: the FTS5 bigram search, merged across kinds by updated_at
//     (what `bkfz <query>` has always shown).
//   - Semantic: cosine similarity over the embedding index.
//   - Hybrid: Keyword and Semantic fused with Reciprocal Rank Fusion.
package search

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hidetzu/backlog-fzf/internal/backlog"
	"github.com/hidetzu/backlog-fzf/internal/config"
	"github.com/hidetzu/backlog-fzf/internal/embed"
	"github.com/hidetzu/backlog-fzf/internal/index"
)

// Hit is one ranked record. Exactly one of Issue / Document is set,
// according to Kind.
type Hit struct {
	Kind     string // index.KindIssue or index.KindDocument
	Issue    backlog.Issue
	Document backlog.Document
	// Score is mode-specific: cosine similarity for Semantic, the fused
	// score for Hybrid, 0 for Keyword.
	Score float64
}

// Ref returns a stable identifier: "issue:KEY" or "doc:ID".
func (h Hit) Ref() string {
	if h.Kind == index.KindIssue {
		return "issue:" + h.Issue.Key
	}
	return "doc:" + h.Document.ID
}

// UpdatedAt returns the record's last update, used for tie-breaking.
func (h Hit) UpdatedAt() time.Time {
	if h.Kind == index.KindIssue {
		return h.Issue.UpdatedAt
	}
	return h.Document.UpdatedAt
}

// ErrNotBuilt is returned by Semantic / Hybrid before any HTTP call when
// `bkfz embed` has never completed.
var ErrNotBuilt = errors.New("semantic index not built; run: bkfz embed")

// ErrRebuild is returned when the index was built with a different
// model / dimension count than the current config.
var ErrRebuild = errors.New("semantic index was built with a different model or dimensions; run: bkfz embed --rebuild")

// RRFK is the rank constant of Reciprocal Rank Fusion (Cormack et al.,
// 2009). 60 is the conventional value; it flattens the difference
// between rank 1 and rank 2 so that a record found by both searches
// wins over one found by only one.
const RRFK = 60

// Keyword runs the FTS search over issues and documents and merges the
// two lists by updated_at DESC, keeping at most limit hits. An empty
// query lists the most recently updated records.
func Keyword(ctx context.Context, db *index.DB, query string, limit int) ([]Hit, error) {
	issues, err := db.SearchIssues(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	docs, err := db.SearchDocuments(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	hits := make([]Hit, 0, len(issues)+len(docs))
	for _, i := range issues {
		hits = append(hits, Hit{Kind: index.KindIssue, Issue: i})
	}
	for _, d := range docs {
		hits = append(hits, Hit{Kind: index.KindDocument, Document: d})
	}
	sort.SliceStable(hits, func(a, b int) bool {
		return hits[a].UpdatedAt().After(hits[b].UpdatedAt())
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// Semantic embeds the query and returns the closest records, best first.
// It checks the index against the config before contacting the embedding
// server (ErrNotBuilt / ErrRebuild). An empty query falls back to
// Keyword("") so an idle prompt never costs an HTTP call.
func Semantic(ctx context.Context, db *index.DB, embedder embed.Embedder, ec *config.Embedding, query string, limit int) ([]Hit, error) {
	if err := checkIndex(ctx, db, ec); err != nil {
		return nil, err
	}
	if strings.TrimSpace(query) == "" {
		return Keyword(ctx, db, "", limit)
	}
	vecs, err := embedder.Embed(ctx, []string{prefix(ec.QueryPrefix) + query})
	if err != nil {
		return nil, err
	}
	if len(vecs) != 1 {
		return nil, fmt.Errorf("embedding server returned %d vectors for the query", len(vecs))
	}
	q := vecs[0]
	index.NormalizeVector(q)
	vhits, err := db.SearchVectors(ctx, q, limit)
	if err != nil {
		if errors.Is(err, index.ErrDimensionMismatch) {
			return nil, ErrRebuild
		}
		return nil, err
	}
	return resolve(ctx, db, vhits)
}

// Hybrid fuses Keyword and Semantic results with Reciprocal Rank Fusion.
// Both searches fetch up to limit candidates; records that appear in both
// lists rank highest. An empty query behaves like Keyword("").
func Hybrid(ctx context.Context, db *index.DB, embedder embed.Embedder, ec *config.Embedding, query string, limit int) ([]Hit, error) {
	if err := checkIndex(ctx, db, ec); err != nil {
		return nil, err
	}
	if strings.TrimSpace(query) == "" {
		return Keyword(ctx, db, "", limit)
	}
	kw, err := Keyword(ctx, db, query, limit)
	if err != nil {
		return nil, err
	}
	sem, err := Semantic(ctx, db, embedder, ec, query, limit)
	if err != nil {
		return nil, err
	}
	fused := RRF([][]Hit{kw, sem}, RRFK)
	if len(fused) > limit {
		fused = fused[:limit]
	}
	return fused, nil
}

// RRF merges ranked lists: each record scores Σ 1/(k + rank) over the
// lists it appears in (rank is 1-based). Ties are broken by updated_at
// DESC, then by Ref so the order is deterministic.
func RRF(lists [][]Hit, k int) []Hit {
	type acc struct {
		hit   Hit
		score float64
	}
	byRef := map[string]*acc{}
	var order []string
	for _, list := range lists {
		for rank, h := range list {
			ref := h.Ref()
			a, ok := byRef[ref]
			if !ok {
				a = &acc{hit: h}
				byRef[ref] = a
				order = append(order, ref)
			}
			a.score += 1 / float64(k+rank+1)
		}
	}
	out := make([]Hit, 0, len(order))
	for _, ref := range order {
		a := byRef[ref]
		a.hit.Score = a.score
		out = append(out, a.hit)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		ti, tj := out[i].UpdatedAt(), out[j].UpdatedAt()
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return out[i].Ref() < out[j].Ref()
	})
	return out
}

// checkIndex verifies the semantic index exists and matches the config.
func checkIndex(ctx context.Context, db *index.DB, ec *config.Embedding) error {
	meta, err := db.EmbeddingMeta(ctx)
	if err != nil {
		return err
	}
	if meta == nil {
		return ErrNotBuilt
	}
	if !meta.Compatible(ec.Model, ec.Dimensions) {
		return ErrRebuild
	}
	return nil
}

// resolve turns vector hits into Hits with the full records, keeping the
// vector ranking.
func resolve(ctx context.Context, db *index.DB, vhits []index.VectorHit) ([]Hit, error) {
	var issueIDs, docIDs []int64
	for _, h := range vhits {
		switch h.Kind {
		case index.KindIssue:
			issueIDs = append(issueIDs, h.RefID)
		case index.KindDocument:
			docIDs = append(docIDs, h.RefID)
		}
	}
	issues, err := db.IssuesByIDs(ctx, issueIDs)
	if err != nil {
		return nil, err
	}
	docs, err := db.DocumentsByIDs(ctx, docIDs)
	if err != nil {
		return nil, err
	}
	hits := make([]Hit, 0, len(vhits))
	for _, h := range vhits {
		switch h.Kind {
		case index.KindIssue:
			if i, ok := issues[h.RefID]; ok {
				hits = append(hits, Hit{Kind: index.KindIssue, Issue: i, Score: float64(h.Score)})
			}
		case index.KindDocument:
			if d, ok := docs[h.RefID]; ok {
				hits = append(hits, Hit{Kind: index.KindDocument, Document: d, Score: float64(h.Score)})
			}
		}
	}
	return hits, nil
}

func prefix(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
