// Command eval measures semantic search quality on the fictional demo
// data (scripts/demodata) with a set of paraphrase queries (cases.go).
// It needs a running embedding server (Ollama by default) but no Backlog
// access, API key, or config file: the index is built in memory.
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

	"github.com/hidetzu/backlog-fzf/internal/backlog"
	"github.com/hidetzu/backlog-fzf/internal/config"
	"github.com/hidetzu/backlog-fzf/internal/embed"
	"github.com/hidetzu/backlog-fzf/internal/index"
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
// the report. ok=false means the Recall@5 gate failed.
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

	issueKeys, docIDs := refTables(issues, docs)
	var results []result
	var ftsHits int
	var queryTime time.Duration
	for _, c := range cases {
		t0 := time.Now()
		vecs, err := client.Embed(ctx, []string{*ec.QueryPrefix + c.query})
		if err != nil {
			return false, fmt.Errorf("embed query %q: %w", c.query, err)
		}
		q := vecs[0]
		index.NormalizeVector(q)
		hits, err := db.SearchVectors(ctx, q, k)
		if err != nil {
			return false, err
		}
		queryTime += time.Since(t0)
		var ranks []string
		for _, h := range hits {
			switch h.Kind {
			case index.KindIssue:
				ranks = append(ranks, "issue:"+issueKeys[h.RefID])
			case index.KindDocument:
				ranks = append(ranks, "doc:"+docIDs[h.RefID])
			}
		}
		results = append(results, result{c, ranks})

		// Keyword baseline: does the FTS path find any wanted record?
		if ftsFinds(ctx, db, c) {
			ftsHits++
		}
		if verbose {
			fmt.Fprintf(out, "\n%q  want %v\n", c.query, c.want)
			for i, r := range ranks[:min(5, len(ranks))] {
				mark := " "
				if rankOf([]string{r}, c.want) == 1 {
					mark = "*"
				}
				fmt.Fprintf(out, "  %s%d. %s  (%.3f)\n", mark, i+1, r, hits[i].Score)
			}
		}
	}

	s := summarize(results)
	fmt.Fprintf(out, "\nsemantic: %s  (avg query %s)\n", s, (queryTime / time.Duration(max(len(cases), 1))).Round(time.Millisecond))
	fmt.Fprintf(out, "keyword : hits=%d/%d (same queries through the FTS path, any wanted record in top %d)\n", ftsHits, len(cases), k)
	if len(s.misses) > 0 {
		fmt.Fprintln(out, "misses:")
		for _, m := range s.misses {
			fmt.Fprintf(out, "  %q  want %v\n", m.query, m.want)
		}
	}
	if minRecall5 > 0 && s.recallAt5() < minRecall5 {
		fmt.Fprintf(out, "\nFAIL: Recall@5 %.2f < %.2f\n", s.recallAt5(), minRecall5)
		return false, nil
	}
	return true, nil
}

// refTables maps rowids back to "issue:KEY" / "doc:ID" refs.
func refTables(issues []backlog.Issue, docs []backlog.Document) (map[int64]string, map[int64]string) {
	issueKeys := map[int64]string{}
	for _, i := range issues {
		issueKeys[int64(i.ID)] = i.Key
	}
	// Documents get AUTOINCREMENT rowids in insertion order, starting at 1.
	docIDs := map[int64]string{}
	for n, d := range docs {
		docIDs[int64(n+1)] = d.ID
	}
	return issueKeys, docIDs
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
