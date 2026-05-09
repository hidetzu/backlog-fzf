// Package syncer orchestrates incremental sync from the Backlog API to
// the local index. It handles two object kinds — issues and documents —
// which expose different API capabilities.
//
// Watermark strategy:
//
// For each scope we persist sync_state.last_synced_at = max(UpdatedAt) - 1s.
// The 1-second buffer guards against Backlog's updatedSince parameter, which
// is date-only (yyyy-MM-dd) while UpdatedAt timestamps come back with full
// second precision (UTC). On the next sync we round the watermark down to a
// date for the API call, then filter the response client-side with
// `UpdatedAt > watermark`. This may over-fetch up to one day's worth of
// records, but UPSERT keeps it idempotent.
//
// Documents has no updatedSince at all, so we walk the listing in
// updated-desc order and stop early once we hit a stub older than the
// watermark; surviving entries are body-fetched via GetDocument (N+1).
//
// Refetch (Options.Refetch = true) ignores the watermark and re-fetches
// everything, but does not delete stale local rows — that is "refetch",
// not "rebuild".
package syncer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hidetzu/backlog-fzf/internal/backlog"
	"github.com/hidetzu/backlog-fzf/internal/index"
)

// sync_state.scope names per object kind.
const (
	issuesScope    = "issues"
	documentsScope = "documents"
)

// ProgressStage represents a stage of the OnProgress callback.
type ProgressStage string

const (
	StageTotal     ProgressStage = "total"     // n = total fetch count (only emitted on initial sync / refetch)
	StageFetching  ProgressStage = "fetching"  // n = 0; emitted just before the API fetch starts
	StageFetched   ProgressStage = "fetched"   // n = cumulative fetched count (emitted per page)
	StageFiltered  ProgressStage = "filtered"  // n = count after client-side filtering
	StageUpserting ProgressStage = "upserting" // n = count to be upserted
	StageDone      ProgressStage = "done"      // n = final upsert count
)

// Options configures a sync run.
type Options struct {
	// Refetch=true ignores the watermark and re-fetches everything for upsert.
	// Stale rows in the local DB (e.g. issues deleted on the Backlog side) are
	// not removed — this is not a true rebuild.
	Refetch     bool
	ProjectKeys []string // empty = all projects
	// OnProgress, if non-nil, is invoked at each stage. Used by the CLI for progress display.
	OnProgress func(stage ProgressStage, n int)
}

// issueLister abstracts the Backlog API dependency for testing.
// *backlog.Client implements it.
type issueLister interface {
	ListIssuesEach(ctx context.Context, since time.Time, projectKeys []string, onPage func([]backlog.Issue) error) error
	// CountIssues returns the count filtered by the same updatedSince.
	// Used as the progress denominator; valid for incremental sync as well (same params as list).
	CountIssues(ctx context.Context, since time.Time, projectKeys []string) (int, error)
}

// documentLister is the subset of the Backlog API needed for Documents sync.
// *backlog.Client implements it.
type documentLister interface {
	ListDocumentsEach(ctx context.Context, projectKeys []string, onPage func([]backlog.Document) error) error
	GetDocument(ctx context.Context, id string) (*backlog.Document, error)
	// CountAllDocuments returns the total document count for projectKeys.
	// Used as the progress denominator; only called on initial sync / refetch.
	CountAllDocuments(ctx context.Context, projectKeys []string) (int, error)
}

// errEarlyStop is a sentinel returned from the onPage callback of
// ListDocumentsEach to signal "stop scanning, watermark reached".
// The caller checks with errors.Is and treats it as a normal exit.
var errEarlyStop = errors.New("syncer: early stop after watermark match")

// Issues fetches issues from Backlog and upserts them into the index.
//
// Algorithm:
//  1. If opts.Refetch, the watermark is reset to zero (full fetch).
//     Otherwise the watermark is loaded from the DB.
//  2. lister.ListIssuesEach(ctx, watermark, projectKeys, onPage) drives
//     the API fetch. onPage appends to fetched and emits
//     OnProgress(StageFetched, n) with the cumulative count.
//  3. When the watermark is non-zero, filter the API response client-side
//     with `issue.UpdatedAt > watermark` to drop the over-fetch caused by
//     updatedSince's date-only granularity.
//  4. If nothing was fetched, exit (do not touch the watermark).
//  5. UpsertIssues.
//  6. SetWatermark with max(UpdatedAt) - 1s.
//
// Errors are fail-fast: a mid-run failure leaves the watermark untouched.
//
// Note: opts.Refetch performs "fetch everything and upsert", but does
// not delete stale local rows. A true rebuild is reserved for a future
// --rebuild-style flag.
func Issues(ctx context.Context, lister issueLister, db *index.DB, opts Options) error {
	progress := opts.OnProgress
	if progress == nil {
		progress = func(_ ProgressStage, _ int) {}
	}

	var watermark time.Time
	if !opts.Refetch {
		wm, err := db.Watermark(ctx, issuesScope)
		if err != nil {
			return fmt.Errorf("syncer: load watermark: %w", err)
		}
		watermark = wm
	}

	// Same updatedSince → count is the fetch denominator.
	if total, err := lister.CountIssues(ctx, watermark, opts.ProjectKeys); err != nil {
		return fmt.Errorf("syncer: count issues: %w", err)
	} else if total > 0 {
		progress(StageTotal, total)
	}

	progress(StageFetching, 0)
	var fetched []backlog.Issue
	err := lister.ListIssuesEach(ctx, watermark, opts.ProjectKeys, func(page []backlog.Issue) error {
		fetched = append(fetched, page...)
		// Don't fire StageFetched on empty pages (the "zero-result final fetch")
		// to avoid a meaningless "[fetched: 0]" line.
		if len(page) > 0 {
			progress(StageFetched, len(fetched))
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("syncer: list issues: %w", err)
	}

	filtered := filterAfter(fetched, watermark)
	progress(StageFiltered, len(filtered))
	if len(filtered) == 0 {
		progress(StageDone, 0)
		return nil
	}

	progress(StageUpserting, len(filtered))
	if err := db.UpsertIssues(ctx, filtered); err != nil {
		return fmt.Errorf("syncer: upsert: %w", err)
	}

	maxUpdated := maxUpdatedAt(filtered)
	if err := db.SetWatermark(ctx, issuesScope, maxUpdated.Add(-time.Second)); err != nil {
		return fmt.Errorf("syncer: set watermark: %w", err)
	}
	progress(StageDone, len(filtered))
	return nil
}

// Documents fetches documents from Backlog and upserts them into the index.
//
// Algorithm:
//  1. opts.Refetch resets the watermark to zero; otherwise it is loaded
//     from the DB.
//  2. lister.ListDocumentsEach (sort=updated, order=desc) walks pages and
//     compares each stub's updated_at against the watermark:
//     - newer than the watermark → fetch the body via lister.GetDocument
//     and collect it.
//     - reached the watermark → return errEarlyStop to stop scanning.
//  3. If nothing was collected, exit (do not touch the watermark).
//  4. UpsertDocuments.
//  5. SetWatermark with max(UpdatedAt) - 1s.
//
// Errors are fail-fast. errEarlyStop is treated as a normal exit
// signaling end-of-scan.
//
// The Backlog API has no updatedSince for documents, so incremental
// behavior is implemented via client-side filtering plus early stop.
// The first run (zero watermark) is therefore a full scan plus
// GetDocument N+1.
func Documents(ctx context.Context, lister documentLister, db *index.DB, opts Options) error {
	progress := opts.OnProgress
	if progress == nil {
		progress = func(_ ProgressStage, _ int) {}
	}

	var watermark time.Time
	if !opts.Refetch {
		wm, err := db.Watermark(ctx, documentsScope)
		if err != nil {
			return fmt.Errorf("syncer: load watermark: %w", err)
		}
		watermark = wm
	}

	// On initial sync / refetch only, fetch the total count up-front to use
	// as the progress denominator. Skip it for incremental syncs since the
	// watermark causes an early break, so a "% of total" denominator would
	// be misleading.
	if watermark.IsZero() {
		total, err := lister.CountAllDocuments(ctx, opts.ProjectKeys)
		if err != nil {
			return fmt.Errorf("syncer: count documents: %w", err)
		}
		if total > 0 {
			progress(StageTotal, total)
		}
	}

	progress(StageFetching, 0)
	var collected []backlog.Document
	listErr := lister.ListDocumentsEach(ctx, opts.ProjectKeys, func(page []backlog.Document) error {
		for _, stub := range page {
			if !watermark.IsZero() && !stub.UpdatedAt.After(watermark) {
				return errEarlyStop // desc order: everything past this is older
			}
			full, err := lister.GetDocument(ctx, stub.ID)
			if err != nil {
				return fmt.Errorf("get document %s: %w", stub.ID, err)
			}
			collected = append(collected, *full)
			progress(StageFetched, len(collected))
		}
		return nil
	})
	if listErr != nil && !errors.Is(listErr, errEarlyStop) {
		return fmt.Errorf("syncer: list documents: %w", listErr)
	}

	progress(StageFiltered, len(collected))
	if len(collected) == 0 {
		progress(StageDone, 0)
		return nil
	}

	progress(StageUpserting, len(collected))
	if err := db.UpsertDocuments(ctx, collected); err != nil {
		return fmt.Errorf("syncer: upsert documents: %w", err)
	}

	maxUpdated := maxUpdatedAtDocs(collected)
	if err := db.SetWatermark(ctx, documentsScope, maxUpdated.Add(-time.Second)); err != nil {
		return fmt.Errorf("syncer: set watermark: %w", err)
	}
	progress(StageDone, len(collected))
	return nil
}

// maxUpdatedAtDocs returns the largest UpdatedAt among docs. Assumes len > 0.
func maxUpdatedAtDocs(docs []backlog.Document) time.Time {
	max := docs[0].UpdatedAt
	for _, d := range docs[1:] {
		if d.UpdatedAt.After(max) {
			max = d.UpdatedAt
		}
	}
	return max
}

// filterAfter keeps only issues whose updated_at is strictly greater than the watermark.
// When the watermark is zero (initial run / --refetch), everything is kept.
func filterAfter(issues []backlog.Issue, watermark time.Time) []backlog.Issue {
	if watermark.IsZero() {
		return issues
	}
	out := make([]backlog.Issue, 0, len(issues))
	for _, i := range issues {
		if i.UpdatedAt.After(watermark) {
			out = append(out, i)
		}
	}
	return out
}

// maxUpdatedAt returns the largest UpdatedAt among issues. The caller must ensure len > 0.
func maxUpdatedAt(issues []backlog.Issue) time.Time {
	max := issues[0].UpdatedAt
	for _, i := range issues[1:] {
		if i.UpdatedAt.After(max) {
			max = i.UpdatedAt
		}
	}
	return max
}
