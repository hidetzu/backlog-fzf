package syncer

import (
	"context"
	"errors"
	"fmt"

	"github.com/hidetzu/backlog-fzf/internal/embed"
	"github.com/hidetzu/backlog-fzf/internal/index"
)

// StageEmbedded is emitted by Embed with the cumulative number of chunks
// embedded so far (the StageTotal before it is the number of chunks to do).
const StageEmbedded ProgressStage = "embedded"

// ErrEmbeddingMismatch is returned when the stored vectors were produced
// by a different model or dimension count than the current configuration.
// The caller should ask the user to run `bkfz embed --rebuild`.
var ErrEmbeddingMismatch = errors.New("syncer: embedding model/dimensions differ from the index")

// ErrDimensionsIgnored is returned (it also matches ErrEmbeddingMismatch)
// when the server returned vectors of a size other than the configured
// Dimensions: the server does not honor the parameter, so a rebuild
// cannot help; the config has to change.
var ErrDimensionsIgnored = fmt.Errorf("%w: server ignored the requested dimensions", ErrEmbeddingMismatch)

// EmbedOptions configures the embedding stage.
type EmbedOptions struct {
	Model      string
	Dimensions int // 0 = model native; recorded from the first response
	Text       embed.TextOptions
	// Rebuild drops every stored vector (and the meta) first.
	Rebuild bool
	// BatchSize is the number of chunk texts sent per Embed call (0 = 32).
	BatchSize  int
	OnProgress func(stage ProgressStage, n int)
}

const defaultEmbedBatch = 32

type chunkJob struct {
	index int
	hash  string
	text  string
}

type recordJob struct {
	kind   string
	refID  int64
	chunks []chunkJob
}

// Embed brings the semantic index up to date with issues / documents.
//
// Algorithm:
//  1. Ensure the tables; with Rebuild, clear everything first.
//  2. Compare embedding_meta with opts (ErrEmbeddingMismatch on conflict).
//  3. Build the embedding text for every record and compare the content
//     hashes with what is stored; records whose chunk hashes differ (or
//     whose chunk count changed) become jobs.
//  4. Send chunk texts to the embedder in batches; after each batch the
//     completed records are upserted and committed, so an interrupted run
//     resumes where it stopped.
//  5. Delete vectors whose record no longer exists; record the meta.
//
// The watermark tables are never touched.
func Embed(ctx context.Context, embedder embed.Embedder, db *index.DB, opts EmbedOptions) error {
	progress := opts.OnProgress
	if progress == nil {
		progress = func(_ ProgressStage, _ int) {}
	}
	batch := opts.BatchSize
	if batch <= 0 {
		batch = defaultEmbedBatch
	}

	if err := db.EnsureEmbeddingSchema(ctx); err != nil {
		return err
	}
	if opts.Rebuild {
		if err := db.ClearEmbeddings(ctx); err != nil {
			return fmt.Errorf("syncer: clear embeddings: %w", err)
		}
	}
	meta, err := db.EmbeddingMeta(ctx)
	if err != nil {
		return fmt.Errorf("syncer: read embedding meta: %w", err)
	}
	if meta != nil && !meta.Compatible(opts.Model, opts.Dimensions) {
		return fmt.Errorf("%w (index: %s/%d, config: %s/%d)", ErrEmbeddingMismatch,
			meta.Model, meta.Dimensions, opts.Model, opts.Dimensions)
	}

	jobs, total, err := collectJobs(ctx, db, opts)
	if err != nil {
		return err
	}
	if total > 0 {
		progress(StageTotal, total)
	}

	done := 0
	// pending holds the records of the current batch; texts is the flat
	// list of their chunk texts in order.
	var pending []recordJob
	var texts []string
	flush := func() error {
		if len(texts) == 0 {
			return nil
		}
		vecs, err := embedder.Embed(ctx, texts)
		if err != nil {
			return fmt.Errorf("syncer: embed: %w", err)
		}
		if len(vecs) != len(texts) {
			return fmt.Errorf("syncer: embed returned %d vectors for %d texts", len(vecs), len(texts))
		}
		dims := len(vecs[0])
		if opts.Dimensions > 0 && dims != opts.Dimensions {
			return fmt.Errorf("%w: server returned %d dimensions, config asks for %d", ErrDimensionsIgnored, dims, opts.Dimensions)
		}
		if meta == nil {
			meta = &index.EmbeddingMeta{Model: opts.Model, Dimensions: dims}
			if err := db.SetEmbeddingMeta(ctx, *meta); err != nil {
				return fmt.Errorf("syncer: set embedding meta: %w", err)
			}
		} else if meta.Dimensions != dims {
			return fmt.Errorf("%w: server returned %d dimensions, index has %d", ErrEmbeddingMismatch, dims, meta.Dimensions)
		}
		recs := make([]index.EmbeddingRecord, 0, len(pending))
		vi := 0
		for _, rj := range pending {
			rec := index.EmbeddingRecord{Kind: rj.kind, RefID: rj.refID}
			for _, c := range rj.chunks {
				v := vecs[vi]
				vi++
				index.NormalizeVector(v)
				rec.Chunks = append(rec.Chunks, index.Chunk{Index: c.index, Hash: c.hash, Vec: v})
			}
			recs = append(recs, rec)
		}
		if err := db.UpsertEmbeddings(ctx, recs); err != nil {
			return fmt.Errorf("syncer: upsert embeddings: %w", err)
		}
		done += len(texts)
		progress(StageEmbedded, done)
		pending, texts = pending[:0], texts[:0]
		return nil
	}
	for _, j := range jobs {
		pending = append(pending, j)
		for _, c := range j.chunks {
			texts = append(texts, c.text)
		}
		if len(texts) >= batch {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := flush(); err != nil {
		return err
	}

	if _, err := db.DeleteOrphanEmbeddings(ctx); err != nil {
		return fmt.Errorf("syncer: delete orphan embeddings: %w", err)
	}
	progress(StageDone, done)
	return nil
}

// collectJobs returns the records whose stored hashes differ from the
// current text, and the total number of chunks they contain.
func collectJobs(ctx context.Context, db *index.DB, opts EmbedOptions) ([]recordJob, int, error) {
	var jobs []recordJob
	total := 0

	issues, err := db.AllIssueTexts(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("syncer: list issues: %w", err)
	}
	issueHashes, err := db.EmbeddingHashes(ctx, index.KindIssue)
	if err != nil {
		return nil, 0, fmt.Errorf("syncer: list issue hashes: %w", err)
	}
	for _, it := range issues {
		text := embed.IssueText(opts.Text, it.Summary, it.Description)
		job := recordJob{kind: index.KindIssue, refID: it.ID,
			chunks: []chunkJob{{0, embed.Hash(opts.Model, text), text}}}
		if !sameHashes(issueHashes[it.ID], job.chunks) {
			jobs = append(jobs, job)
			total += len(job.chunks)
		}
	}

	docs, err := db.AllDocumentTexts(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("syncer: list documents: %w", err)
	}
	docHashes, err := db.EmbeddingHashes(ctx, index.KindDocument)
	if err != nil {
		return nil, 0, fmt.Errorf("syncer: list document hashes: %w", err)
	}
	for _, d := range docs {
		job := recordJob{kind: index.KindDocument, refID: d.ID}
		for i, text := range embed.DocumentTexts(opts.Text, d.Title, d.Body) {
			job.chunks = append(job.chunks, chunkJob{i, embed.Hash(opts.Model, text), text})
		}
		if !sameHashes(docHashes[d.ID], job.chunks) {
			jobs = append(jobs, job)
			total += len(job.chunks)
		}
	}
	return jobs, total, nil
}

func sameHashes(stored []string, chunks []chunkJob) bool {
	if len(stored) != len(chunks) {
		return false
	}
	for i, c := range chunks {
		if stored[i] != c.hash {
			return false
		}
	}
	return true
}
