// Command eval measures semantic search quality on the fictional demo
// data (scripts/demodata) with a set of paraphrase queries (cases.go).
// It needs a running embedding server (Ollama by default) but no Backlog
// access, API key, or config file: the index is built in memory. Two sets
// are scored, each in semantic and hybrid mode: "paraphrase" queries that
// share no keyword with their target, and "partial" queries that contain
// one exact term plus a vague description.
//
// Usage:
//
//	go run ./scripts/eval [-base-url URL] [-model NAME] [-dimensions N]
//	                      [-chunk-chars N] [-max-issue-chars N] [-k N]
//	                      [-min-recall5 X] [-v]
//
// Use it to compare models, dimensions and chunk settings: the summary
// line reports Recall@1, Recall@5 and MRR, and -v prints the top hits per
// query. With -min-recall5 the exit status is 1 when Recall@5 falls below
// the threshold, which makes it usable as a regression gate.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/hidetzu/backlog-fzf/internal/config"
	"github.com/hidetzu/backlog-fzf/internal/embed"
	"github.com/hidetzu/backlog-fzf/internal/index"
	"github.com/hidetzu/backlog-fzf/internal/search"
	"github.com/hidetzu/backlog-fzf/internal/syncer"
	"github.com/hidetzu/backlog-fzf/scripts/demodata"
)

func main() {
	fs := flag.NewFlagSet("eval", flag.ExitOnError)
	baseURL := fs.String("base-url", config.DefaultEmbeddingBaseURL, "OpenAI-compatible embeddings endpoint")
	model := fs.String("model", config.DefaultEmbeddingModel, "embedding model")
	dims := fs.Int("dimensions", 0, "requested dimensions (0 = model native)")
	queryPrefix := fs.String("query-prefix", config.DefaultQueryPrefix, "prefix prepended to queries")
	docPrefix := fs.String("document-prefix", config.DefaultDocumentPrefix, "prefix prepended to documents")
	issueChars := fs.Int("max-issue-chars", config.DefaultEmbeddingIssueChars, "description runes embedded per issue")
	chunkChars := fs.Int("chunk-chars", config.DefaultEmbeddingChunkChars, "document chunk size in runes")
	maxChunks := fs.Int("max-chunks", config.DefaultEmbeddingMaxChunks, "chunks per document")
	k := fs.Int("k", 10, "results fetched per query")
	minRecall5 := fs.Float64("min-recall5", 0, "exit 1 when Recall@5 is below this (0 = report only)")
	verbose := fs.Bool("v", false, "print the top hits for every query")
	_ = fs.Parse(os.Args[1:])

	ec := config.Embedding{
		BaseURL: *baseURL, Model: *model, Dimensions: *dims,
		QueryPrefix: queryPrefix, DocumentPrefix: docPrefix,
		MaxIssueChars: *issueChars, ChunkChars: *chunkChars, MaxChunks: *maxChunks,
	}.WithDefaults()
	if err := ec.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "eval:", err)
		os.Exit(2)
	}
	ok, err := run(context.Background(), ec, *k, *minRecall5, *verbose, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "eval:", err)
		os.Exit(1)
	}
	if !ok {
		os.Exit(1)
	}
}

// run builds the in-memory index, embeds it, runs every case and prints
// the report. ok=false means the Recall@5 gate failed for some mode.
func run(ctx context.Context, ec config.Embedding, k int, minRecall5 float64, verbose bool, out *os.File) (bool, error) {
	db, err := index.Open(":memory:")
	if err != nil {
		return false, err
	}
	defer db.Close()
	issues, docs := demodata.Issues(), demodata.Documents()
	if err := db.UpsertIssues(ctx, issues); err != nil {
		return false, err
	}
	if err := db.UpsertDocuments(ctx, docs); err != nil {
		return false, err
	}

	client := &embed.Client{BaseURL: ec.BaseURL, Model: ec.Model, Dimensions: ec.Dimensions, APIKey: ec.APIKey()}
	start := time.Now()
	err = syncer.Embed(ctx, client, db, syncer.EmbedOptions{
		Model: ec.Model, Dimensions: ec.Dimensions,
		Text: embed.TextOptions{
			DocumentPrefix: *ec.DocumentPrefix, MaxIssueChars: ec.MaxIssueChars,
			ChunkChars: ec.ChunkChars, MaxChunks: ec.MaxChunks,
		},
	})
	if err != nil {
		return false, err
	}
	nVec, _ := db.CountEmbeddings(ctx)
	meta, _ := db.EmbeddingMeta(ctx)
	fmt.Fprintf(out, "model=%s dimensions=%d records=%d vectors=%d embed=%s\n",
		meta.Model, meta.Dimensions, len(issues)+len(docs), nVec, time.Since(start).Round(time.Millisecond))

	sets := []struct {
		name  string
		cases []evalCase
	}{
		{"paraphrase", cases},
		{"partial", partialCases},
	}
	modes := []struct {
		name string
		run  func(context.Context, string) ([]search.Hit, error)
	}{
		{"semantic", func(ctx context.Context, q string) ([]search.Hit, error) {
			return search.Semantic(ctx, db, client, &ec, q, k)
		}},
		{"hybrid", func(ctx context.Context, q string) ([]search.Hit, error) {
			return search.Hybrid(ctx, db, client, &ec, q, k)
		}},
	}

	type cell struct {
		s     summary
		query time.Duration
	}
	table := map[string]map[string]cell{}  // set → mode → metrics
	var allResults = map[string][]result{} // mode → results over every set
	ftsHits := map[string]int{}
	for _, set := range sets {
		table[set.name] = map[string]cell{}
		for _, mode := range modes {
			var results []result
			var queryTime time.Duration
			for _, c := range set.cases {
				t0 := time.Now()
				hits, err := mode.run(ctx, c.query)
				if err != nil {
					return false, fmt.Errorf("%s %q: %w", mode.name, c.query, err)
				}
				queryTime += time.Since(t0)
				ranks := make([]string, len(hits))
				for i, h := range hits {
					ranks[i] = h.Ref()
				}
				results = append(results, result{c, ranks})
				if verbose && mode.name == modes[0].name {
					printTop(out, c, hits)
				}
			}
			table[set.name][mode.name] = cell{summarize(results), queryTime / time.Duration(max(len(set.cases), 1))}
			allResults[mode.name] = append(allResults[mode.name], results...)
		}
		for _, c := range set.cases {
			if ftsFinds(ctx, db, c) {
				ftsHits[set.name]++
			}
		}
	}

	fmt.Fprintf(out, "\n%-12s %-9s %6s %8s %8s %6s   %s\n", "set", "mode", "cases", "Recall@1", "Recall@5", "MRR", "avg query")
	for _, set := range sets {
		for _, mode := range modes {
			c := table[set.name][mode.name]
			fmt.Fprintf(out, "%-12s %-9s %6d %8.2f %8.2f %6.2f   %s\n", set.name, mode.name, c.s.n, c.s.recallAt1(), c.s.recallAt5(), c.s.mrr(), c.query.Round(time.Millisecond))
		}
		fmt.Fprintf(out, "%-12s %-9s %6d   (any wanted record returned by the FTS path: %d/%d)\n", set.name, "keyword", len(set.cases), ftsHits[set.name], len(set.cases))
	}
	fmt.Fprintln(out)
	var gateFailed bool
	for _, mode := range modes {
		s := summarize(allResults[mode.name])
		fmt.Fprintf(out, "%-9s all: %s\n", mode.name, s)
		for _, m := range s.misses {
			fmt.Fprintf(out, "          miss %q  want %v\n", m.query, m.want)
		}
		if minRecall5 > 0 && s.recallAt5() < minRecall5 {
			fmt.Fprintf(out, "FAIL: %s Recall@5 %.2f < %.2f\n", mode.name, s.recallAt5(), minRecall5)
			gateFailed = true
		}
	}
	return !gateFailed, nil
}

// printTop prints the top-5 hits of one query, marking wanted records.
func printTop(out *os.File, c evalCase, hits []search.Hit) {
	fmt.Fprintf(out, "\n%q  want %v\n", c.query, c.want)
	for i, h := range hits[:min(5, len(hits))] {
		mark := " "
		if rankOf([]string{h.Ref()}, c.want) == 1 {
			mark = "*"
		}
		fmt.Fprintf(out, "  %s%d. %s  (%.3f)\n", mark, i+1, h.Ref(), h.Score)
	}
}

// ftsFinds runs the keyword search for the case and reports whether any
// wanted record is returned.
func ftsFinds(ctx context.Context, db *index.DB, c evalCase) bool {
	issues, err := db.SearchIssues(ctx, c.query, 50)
	if err != nil {
		return false
	}
	docs, err := db.SearchDocuments(ctx, c.query, 50)
	if err != nil {
		return false
	}
	var ranks []string
	for _, i := range issues {
		ranks = append(ranks, "issue:"+i.Key)
	}
	for _, d := range docs {
		ranks = append(ranks, "doc:"+d.ID)
	}
	return rankOf(ranks, c.want) > 0
}

// seededRefs returns every "issue:KEY" / "doc:ID" present in the demo
// data; used by tests to keep cases.go in sync with demodata.
func seededRefs() map[string]bool {
	refs := map[string]bool{}
	for _, i := range demodata.Issues() {
		refs["issue:"+i.Key] = true
	}
	for _, d := range demodata.Documents() {
		refs["doc:"+d.ID] = true
	}
	return refs
}
