package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hidetzu/backlog-fzf/internal/backlog"
	"github.com/hidetzu/backlog-fzf/internal/config"
	"github.com/hidetzu/backlog-fzf/internal/embed"
	"github.com/hidetzu/backlog-fzf/internal/index"
	"github.com/hidetzu/backlog-fzf/internal/syncer"
)

// fakeEmbedder maps texts to fixed vectors; unknown texts get a zero vector.
type fakeEmbedder struct {
	vectors map[string][]float32
	calls   []string
	err     error
}

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.calls = append(f.calls, texts...)
	if f.err != nil {
		return nil, f.err
	}
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

func semanticFixture(t *testing.T) (*index.DB, *config.Embedding) {
	t.Helper()
	db, err := index.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertIssues(ctx, []backlog.Issue{
		{ID: 1, Key: "P-1", ProjectKey: "P", Summary: "login fails", Status: "Open", CreatedAt: now, UpdatedAt: now},
		{ID: 2, Key: "P-2", ProjectKey: "P", Summary: "slow export", Status: "Open", CreatedAt: now, UpdatedAt: now.Add(time.Hour)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertDocuments(ctx, []backlog.Document{
		{ID: "d1", ProjectKey: "P", Title: "setup guide", CreatedAt: now, UpdatedAt: now.Add(2 * time.Hour)},
	}); err != nil {
		t.Fatal(err)
	}
	ec := config.Embedding{Model: "m", MaxIssueChars: 100, ChunkChars: 1000, MaxChunks: 8}.WithDefaults()
	return db, &ec
}

func buildIndex(t *testing.T, db *index.DB, ec *config.Embedding, fe *fakeEmbedder) {
	t.Helper()
	err := syncer.Embed(context.Background(), fe, db, syncer.EmbedOptions{
		Model: ec.Model, Dimensions: ec.Dimensions, Text: textOptions(ec),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSemanticLines_RanksAcrossKindsByScore(t *testing.T) {
	db, ec := semanticFixture(t)
	fe := &fakeEmbedder{vectors: map[string][]float32{
		"title: login fails | text: ":      {1, 0, 0},
		"title: slow export | text: ":      {0, 1, 0},
		"title: setup guide | text: ":      {0.9, 0.1, 0},
		*ec.QueryPrefix + "cannot sign in": {1, 0.05, 0},
	}}
	buildIndex(t, db, ec, fe)

	lines, err := semanticLines(context.Background(), db, fe, ec, "cannot sign in", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 3 {
		t.Fatalf("got %d lines: %q", len(lines), lines)
	}
	if !strings.HasPrefix(lines[0], "issue\tP-1\t") {
		t.Errorf("top line should be issue P-1 (best cosine), got %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "doc\td1\t") {
		t.Errorf("second line should be doc d1, got %q", lines[1])
	}
	if !strings.HasPrefix(lines[2], "issue\tP-2\t") {
		t.Errorf("third line should be issue P-2, got %q", lines[2])
	}
	// The query was sent with the configured prefix.
	last := fe.calls[len(fe.calls)-1]
	if last != *ec.QueryPrefix+"cannot sign in" {
		t.Errorf("query sent = %q", last)
	}
}

func TestSemanticLines_EmptyQueryReturnsRecentWithoutEmbedding(t *testing.T) {
	db, ec := semanticFixture(t)
	fe := &fakeEmbedder{}
	buildIndex(t, db, ec, fe)
	before := len(fe.calls)
	lines, err := semanticLines(context.Background(), db, fe, ec, "   ", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(fe.calls) != before {
		t.Error("empty query must not call the embedder")
	}
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "doc\td1\t") {
		t.Errorf("expected recent order (doc d1 first), got %q", lines)
	}
}

func TestSemanticLines_NotBuiltAndRebuildErrors(t *testing.T) {
	db, ec := semanticFixture(t)
	fe := &fakeEmbedder{}
	ctx := context.Background()
	if _, err := semanticLines(ctx, db, fe, ec, "x", 10); !errors.Is(err, errSemanticNotBuilt) {
		t.Errorf("before embed: want errSemanticNotBuilt, got %v", err)
	}
	if len(fe.calls) != 0 {
		t.Error("must not contact the server when the index is not built")
	}
	buildIndex(t, db, ec, fe)
	other := *ec
	other.Model = "different"
	if _, err := semanticLines(ctx, db, fe, &other, "x", 10); !errors.Is(err, errSemanticRebuild) {
		t.Errorf("model mismatch: want errSemanticRebuild, got %v", err)
	}
	dims := *ec
	dims.Dimensions = 7
	if _, err := semanticLines(ctx, db, fe, &dims, "x", 10); !errors.Is(err, errSemanticRebuild) {
		t.Errorf("dimension mismatch: want errSemanticRebuild, got %v", err)
	}
	// Server now returns a vector of another size → rebuild hint too.
	bad := &fakeEmbedder{vectors: map[string][]float32{*ec.QueryPrefix + "x": {1, 0}}}
	if _, err := semanticLines(ctx, db, bad, ec, "x", 10); !errors.Is(err, errSemanticRebuild) {
		t.Errorf("query size mismatch: want errSemanticRebuild, got %v", err)
	}
}

func TestSemanticLines_EmbedderErrorPropagates(t *testing.T) {
	db, ec := semanticFixture(t)
	buildIndex(t, db, ec, &fakeEmbedder{})
	boom := errors.New("boom")
	_, err := semanticLines(context.Background(), db, &fakeEmbedder{err: boom}, ec, "x", 10)
	if !errors.Is(err, boom) {
		t.Errorf("want boom, got %v", err)
	}
}

func TestSplitModeFlag(t *testing.T) {
	if m, q := splitModeFlag([]string{"--semantic", "a", "b"}); m != "semantic" || q != "a b" {
		t.Errorf("got %q %q", m, q)
	}
	if m, q := splitModeFlag([]string{"--hybrid", "a"}); m != "hybrid" || q != "a" {
		t.Errorf("got %q %q", m, q)
	}
	if m, q := splitModeFlag([]string{"a", "--semantic"}); m != "" || q != "a --semantic" {
		t.Errorf("non-leading flag must be part of the query: got %q %q", m, q)
	}
	if m, q := splitModeFlag(nil); m != "" || q != "" {
		t.Errorf("got %q %q", m, q)
	}
}

func TestModeLines_HybridFusesKeywordAndSemantic(t *testing.T) {
	db, ec := semanticFixture(t)
	fe := &fakeEmbedder{vectors: map[string][]float32{
		"title: login fails | text: ": {1, 0, 0},
		"title: slow export | text: ": {0, 1, 0},
		"title: setup guide | text: ": {0.9, 0.1, 0},
		*ec.QueryPrefix + "export":    {0.9, 0.1, 0}, // semantically closest to the guide ...
	}}
	buildIndex(t, db, ec, fe)
	// ... but "export" is a keyword hit for P-2 only, so hybrid lifts P-2
	// above the guide while semantic alone keeps the guide first.
	hyb, err := modeLines(context.Background(), db, fe, ec, "hybrid", "export", 10)
	if err != nil {
		t.Fatal(err)
	}
	sem, _ := modeLines(context.Background(), db, fe, ec, "semantic", "export", 10)
	if !strings.HasPrefix(sem[0], "doc\td1\t") {
		t.Errorf("semantic baseline: %q", sem)
	}
	if !strings.HasPrefix(hyb[0], "issue\tP-2\t") {
		t.Errorf("hybrid should rank the keyword+semantic hit first, got %q", hyb)
	}
}

func TestErrorLine_IsParseableAndIgnoredOnEnter(t *testing.T) {
	line := errorLine(errors.New("server\nunreachable"))
	if !strings.HasPrefix(line, "error\t-\t-\t-\t✗ server unreachable\t-") {
		t.Errorf("errorLine = %q", line)
	}
	kind, key, ok := parseSelectedRef(line)
	if !ok || kind != kindError || key != "-" {
		t.Errorf("parseSelectedRef = %q %q %v", kind, key, ok)
	}
	if strings.Count(line, "\t") != strings.Count(formatIssueLine(backlog.Issue{}), "\t") {
		t.Error("error line must have the same column count as issue lines")
	}
}

func TestErrorLine_DropsHint(t *testing.T) {
	ec := &config.Embedding{BaseURL: "http://localhost:1/v1", Model: "m"}
	err := embedServerError(ec, fmt.Errorf("%w: http://localhost:1/v1 (refused)", embed.ErrUnreachable))
	if !strings.Contains(err.Error(), "ollama pull m") {
		t.Errorf("terminal error should carry the hint, got %q", err)
	}
	line := errorLine(err)
	if strings.Contains(line, "ollama") || !strings.Contains(line, "(see README)") {
		t.Errorf("pseudo line should drop the hint, got %q", line)
	}
	if !errors.Is(err, embed.ErrUnreachable) {
		t.Error("hintedError must unwrap to the original error")
	}
}

func TestRunSemantic_RequiresQuery(t *testing.T) {
	err := runSemantic(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "--semantic: QUERY required") {
		t.Errorf("got %v", err)
	}
	if err := runHybrid(context.Background(), []string{"  "}); err == nil || !strings.Contains(err.Error(), "--hybrid: QUERY required") {
		t.Errorf("got %v", err)
	}
}

func TestTUIOptions_SemanticBinding(t *testing.T) {
	q := shellQuote("/opt/bkfz")
	on := tuiOptions(q, "semantic")
	if on.SemanticCmd != q+" --list --semantic {q}" || on.SemanticPrompt != "semantic> " || on.SemanticHint != "" {
		t.Errorf("enabled: %+v", on)
	}
	hy := tuiOptions(q, "hybrid")
	if hy.SemanticCmd != q+" --list --hybrid {q}" || hy.SemanticPrompt != "hybrid> " {
		t.Errorf("hybrid: %+v", hy)
	}
	off := tuiOptions(q, "")
	if off.SemanticCmd != "" || off.SemanticHint == "" {
		t.Errorf("disabled: %+v", off)
	}
	if tuiMode(&config.Config{}) != "" {
		t.Error("tuiMode without embedding should be empty")
	}
	if got := tuiMode(&config.Config{Embedding: &config.Embedding{TUIMode: "hybrid"}}); got != "hybrid" {
		t.Errorf("tuiMode = %q", got)
	}
}
