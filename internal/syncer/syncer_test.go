package syncer

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/hidetzu/backlog-fzf/internal/backlog"
	"github.com/hidetzu/backlog-fzf/internal/index"
)

// fakeLister is a test double for issueLister.
//   - When pageSize == 0, response is returned in a single onPage(response) call.
//   - When pageSize > 0, response is split into pageSize chunks across multiple onPage calls.
type fakeLister struct {
	calls    []listCall
	response []backlog.Issue
	err      error
	pageSize int // 0 = return as a single page
	count    int // value returned by CountIssues (0 = len(response))
	countErr error
}

func (f *fakeLister) CountIssues(_ context.Context, _ time.Time, _ []string) (int, error) {
	if f.countErr != nil {
		return 0, f.countErr
	}
	if f.count > 0 {
		return f.count, nil
	}
	return len(f.response), nil
}

type listCall struct {
	since       time.Time
	projectKeys []string
}

func (f *fakeLister) ListIssuesEach(_ context.Context, since time.Time, projectKeys []string, onPage func([]backlog.Issue) error) error {
	f.calls = append(f.calls, listCall{since: since, projectKeys: projectKeys})
	if f.err != nil {
		return f.err
	}
	if f.pageSize <= 0 {
		// Single page (onPage is invoked once even for an empty response — matches real client behavior).
		return onPage(f.response)
	}
	if len(f.response) == 0 {
		return onPage(nil)
	}
	for i := 0; i < len(f.response); i += f.pageSize {
		end := i + f.pageSize
		if end > len(f.response) {
			end = len(f.response)
		}
		if err := onPage(f.response[i:end]); err != nil {
			return err
		}
	}
	return nil
}

// fakeDocLister is a test double for documentLister.
//   - listResp is the page response from ListDocumentsEach (splittable via pageSize).
//   - getResp maps backlog_id → detail response; if empty, falls back to the matching ID in listResp.
type fakeDocLister struct {
	listCalls []listCall
	listResp  []backlog.Document
	getResp   map[string]backlog.Document
	listErr   error
	getErr    error
	pageSize  int
	count     int // value returned by CountAllDocuments (0 = len(listResp))
	countErr  error
}

func (f *fakeDocLister) ListDocumentsEach(_ context.Context, projectKeys []string, onPage func([]backlog.Document) error) error {
	f.listCalls = append(f.listCalls, listCall{projectKeys: projectKeys})
	if f.listErr != nil {
		return f.listErr
	}
	if f.pageSize <= 0 {
		return onPage(f.listResp)
	}
	if len(f.listResp) == 0 {
		return onPage(nil)
	}
	for i := 0; i < len(f.listResp); i += f.pageSize {
		end := i + f.pageSize
		if end > len(f.listResp) {
			end = len(f.listResp)
		}
		if err := onPage(f.listResp[i:end]); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeDocLister) CountAllDocuments(_ context.Context, _ []string) (int, error) {
	if f.countErr != nil {
		return 0, f.countErr
	}
	if f.count > 0 {
		return f.count, nil
	}
	return len(f.listResp), nil
}

func (f *fakeDocLister) GetDocument(_ context.Context, id string) (*backlog.Document, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if d, ok := f.getResp[id]; ok {
		return &d, nil
	}
	// fallback: look up in listResp
	for _, d := range f.listResp {
		if d.ID == id {
			d.Body = "body of " + id
			return &d, nil
		}
	}
	return nil, fmt.Errorf("not found: %s", id)
}

func docAt(id, projectKey string, updatedAt time.Time) backlog.Document {
	return backlog.Document{
		ID:         id,
		ProjectKey: projectKey,
		Title:      "title " + id,
		CreatedAt:  updatedAt,
		UpdatedAt:  updatedAt,
	}
}

func newMemDB(t *testing.T) *index.DB {
	t.Helper()
	db, err := index.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func issue(id int, key string, updatedAt time.Time) backlog.Issue {
	return backlog.Issue{
		ID:         id,
		Key:        key,
		ProjectKey: "P",
		Summary:    "summary " + key,
		CreatedAt:  updatedAt,
		UpdatedAt:  updatedAt,
	}
}

func TestSyncer_FirstRunFromZeroWatermark(t *testing.T) {
	db := newMemDB(t)
	ctx := context.Background()

	t1 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC)
	lister := &fakeLister{response: []backlog.Issue{
		issue(1, "P-1", t1),
		issue(2, "P-2", t2),
	}}

	if err := Issues(ctx, lister, db, Options{}); err != nil {
		t.Fatal(err)
	}

	// API is called with a zero since.
	if len(lister.calls) != 1 || !lister.calls[0].since.IsZero() {
		t.Errorf("first call should pass zero since, got %v", lister.calls)
	}

	// Both issues should be upserted.
	got, err := db.SearchIssues(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 issues in DB, got %d", len(got))
	}

	// watermark = max(UpdatedAt) - 1s
	wm, err := db.Watermark(ctx, issuesScope)
	if err != nil {
		t.Fatal(err)
	}
	want := t2.Add(-time.Second)
	if !wm.Equal(want) {
		t.Errorf("watermark = %v, want %v", wm, want)
	}
}

func TestSyncer_IncrementalFiltersAlreadySeen(t *testing.T) {
	db := newMemDB(t)
	ctx := context.Background()

	// Existing watermark = 11:59:59.
	existingWM := time.Date(2026, 5, 1, 11, 59, 59, 0, time.UTC)
	if err := db.SetWatermark(ctx, issuesScope, existingWM); err != nil {
		t.Fatal(err)
	}

	// API response: 1 record <= watermark (filtered out), 2 records > watermark (kept).
	t0 := time.Date(2026, 5, 1, 11, 30, 0, 0, time.UTC)
	t1 := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC)
	lister := &fakeLister{response: []backlog.Issue{
		issue(1, "P-1", t0),
		issue(2, "P-2", t1),
		issue(3, "P-3", t2),
	}}

	if err := Issues(ctx, lister, db, Options{}); err != nil {
		t.Fatal(err)
	}

	// API receives existingWM as since.
	if !lister.calls[0].since.Equal(existingWM) {
		t.Errorf("since = %v, want %v", lister.calls[0].since, existingWM)
	}

	// Only 2 records make it into the DB (t0 is filtered out).
	got, err := db.SearchIssues(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 issues, got %d", len(got))
	}

	// watermark = max(t1, t2) - 1s = t2 - 1s
	wm, err := db.Watermark(ctx, issuesScope)
	if err != nil {
		t.Fatal(err)
	}
	want := t2.Add(-time.Second)
	if !wm.Equal(want) {
		t.Errorf("watermark = %v, want %v", wm, want)
	}
}

func TestSyncer_RefetchIgnoresWatermark(t *testing.T) {
	db := newMemDB(t)
	ctx := context.Background()

	// Set up an existing watermark.
	existingWM := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	if err := db.SetWatermark(ctx, issuesScope, existingWM); err != nil {
		t.Fatal(err)
	}

	t1 := time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC)
	lister := &fakeLister{response: []backlog.Issue{
		issue(1, "P-1", t1),
	}}

	if err := Issues(ctx, lister, db, Options{Refetch: true}); err != nil {
		t.Fatal(err)
	}

	// --refetch → since must be zero.
	if !lister.calls[0].since.IsZero() {
		t.Errorf("--refetch should pass zero since, got %v", lister.calls[0].since)
	}

	// Watermark is updated (t1 - 1s).
	wm, _ := db.Watermark(ctx, issuesScope)
	want := t1.Add(-time.Second)
	if !wm.Equal(want) {
		t.Errorf("watermark = %v, want %v", wm, want)
	}
}

func TestSyncer_EmptyResponseDoesNotTouchWatermark(t *testing.T) {
	db := newMemDB(t)
	ctx := context.Background()

	existingWM := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	if err := db.SetWatermark(ctx, issuesScope, existingWM); err != nil {
		t.Fatal(err)
	}

	lister := &fakeLister{response: nil}
	if err := Issues(ctx, lister, db, Options{}); err != nil {
		t.Fatal(err)
	}

	wm, _ := db.Watermark(ctx, issuesScope)
	if !wm.Equal(existingWM) {
		t.Errorf("empty response should not move watermark: got %v, want %v", wm, existingWM)
	}
}

func TestSyncer_AllFilteredOutDoesNotTouchWatermark(t *testing.T) {
	db := newMemDB(t)
	ctx := context.Background()

	existingWM := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	if err := db.SetWatermark(ctx, issuesScope, existingWM); err != nil {
		t.Fatal(err)
	}

	// API responds, but every record has updated_at <= watermark (the over-fetch).
	t0 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	lister := &fakeLister{response: []backlog.Issue{issue(1, "P-1", t0)}}

	if err := Issues(ctx, lister, db, Options{}); err != nil {
		t.Fatal(err)
	}

	wm, _ := db.Watermark(ctx, issuesScope)
	if !wm.Equal(existingWM) {
		t.Errorf("all-filtered should not move watermark: got %v, want %v", wm, existingWM)
	}
}

func TestSyncer_APIErrorFailsFastWatermarkUnchanged(t *testing.T) {
	db := newMemDB(t)
	ctx := context.Background()

	existingWM := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	if err := db.SetWatermark(ctx, issuesScope, existingWM); err != nil {
		t.Fatal(err)
	}

	lister := &fakeLister{err: errors.New("api boom")}
	if err := Issues(ctx, lister, db, Options{}); err == nil {
		t.Fatal("expected error from API, got nil")
	}

	wm, _ := db.Watermark(ctx, issuesScope)
	if !wm.Equal(existingWM) {
		t.Errorf("watermark moved on API failure: got %v, want %v", wm, existingWM)
	}
}

func TestSyncer_OnProgressEmitsExpectedStages(t *testing.T) {
	db := newMemDB(t)
	ctx := context.Background()

	t1 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC)
	lister := &fakeLister{response: []backlog.Issue{
		issue(1, "P-1", t1),
		issue(2, "P-2", t2),
	}}

	type event struct {
		stage ProgressStage
		n     int
	}
	var events []event
	if err := Issues(ctx, lister, db, Options{
		OnProgress: func(stage ProgressStage, n int) {
			events = append(events, event{stage, n})
		},
	}); err != nil {
		t.Fatal(err)
	}

	want := []event{
		{StageTotal, 2}, // via CountIssues (fakeLister returns len(response))
		{StageFetching, 0},
		{StageFetched, 2},
		{StageFiltered, 2},
		{StageUpserting, 2},
		{StageDone, 2},
	}
	if !reflect.DeepEqual(events, want) {
		t.Errorf("events =\n  %v\nwant =\n  %v", events, want)
	}
}

func TestSyncer_OnProgress_FetchedCalledPerPage(t *testing.T) {
	db := newMemDB(t)
	ctx := context.Background()

	var resp []backlog.Issue
	for i := 1; i <= 5; i++ {
		resp = append(resp, issue(i, fmt.Sprintf("P-%d", i),
			time.Date(2026, 5, i, 0, 0, 0, 0, time.UTC)))
	}
	// pageSize=2 with 5 records → 3 pages: [1,2] [3,4] [5].
	lister := &fakeLister{response: resp, pageSize: 2}

	var fetchedCounts []int
	if err := Issues(ctx, lister, db, Options{
		OnProgress: func(stage ProgressStage, n int) {
			if stage == StageFetched {
				fetchedCounts = append(fetchedCounts, n)
			}
		},
	}); err != nil {
		t.Fatal(err)
	}
	want := []int{2, 4, 5}
	if !reflect.DeepEqual(fetchedCounts, want) {
		t.Errorf("fetched counts = %v, want %v", fetchedCounts, want)
	}
}

// On a zero-result sync, "[fetched: 0]" carries no information, so
// StageFetched does not fire — the CLI output stays clean without the
// caller having to add an `if n > 0` guard.
func TestSyncer_OnProgress_FetchedNotEmittedOnEmptyResponse(t *testing.T) {
	db := newMemDB(t)
	ctx := context.Background()

	lister := &fakeLister{response: nil}

	var fetchedEvents int
	if err := Issues(ctx, lister, db, Options{
		OnProgress: func(stage ProgressStage, n int) {
			if stage == StageFetched {
				fetchedEvents++
			}
		},
	}); err != nil {
		t.Fatal(err)
	}
	if fetchedEvents != 0 {
		t.Errorf("StageFetched fired %d times for empty response, want 0", fetchedEvents)
	}
}

// --- Documents ---

func TestDocuments_FirstRunFetchesAllAndSetsWatermark(t *testing.T) {
	db := newMemDB(t)
	ctx := context.Background()

	t1 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC)
	lister := &fakeDocLister{listResp: []backlog.Document{
		docAt("d1", "PROJ", t2), // newer first (desc)
		docAt("d2", "PROJ", t1),
	}}

	if err := Documents(ctx, lister, db, Options{}); err != nil {
		t.Fatal(err)
	}

	got, err := db.SearchDocuments(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 docs, got %d", len(got))
	}

	wm, _ := db.Watermark(ctx, documentsScope)
	want := t2.Add(-time.Second)
	if !wm.Equal(want) {
		t.Errorf("watermark = %v, want %v", wm, want)
	}
}

// Incremental: walk the desc-ordered list and early-break at the watermark.
// Verify via GetDocument call counts (the older side must not be called).
func TestDocuments_IncrementalEarlyStopsAtWatermark(t *testing.T) {
	db := newMemDB(t)
	ctx := context.Background()

	existingWM := time.Date(2026, 5, 1, 11, 59, 59, 0, time.UTC)
	if err := db.SetWatermark(ctx, documentsScope, existingWM); err != nil {
		t.Fatal(err)
	}

	tNew := time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC)
	tBoundary := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC) // newer than watermark
	tOld := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)       // older than watermark → early stop

	listResp := []backlog.Document{
		docAt("dnew", "PROJ", tNew),
		docAt("dboundary", "PROJ", tBoundary),
		docAt("dold", "PROJ", tOld),
	}

	getCalls := []string{}
	getResp := map[string]backlog.Document{}
	for _, d := range listResp {
		getResp[d.ID] = d
	}
	lister := &fakeDocListerSpy{
		fakeDocLister: fakeDocLister{listResp: listResp, getResp: getResp},
		onGetID:       func(id string) { getCalls = append(getCalls, id) },
	}

	if err := Documents(ctx, lister, db, Options{}); err != nil {
		t.Fatal(err)
	}

	// Early stop: GetDocument must not be called for dold.
	if len(getCalls) != 2 || getCalls[0] != "dnew" || getCalls[1] != "dboundary" {
		t.Errorf("GetDocument called for %v, want [dnew dboundary] (no dold)", getCalls)
	}

	// DB has 2 entries.
	got, _ := db.SearchDocuments(ctx, "", 10)
	if len(got) != 2 {
		t.Errorf("expected 2 docs in DB, got %d", len(got))
	}
}

func TestDocuments_RefetchIgnoresWatermarkAndScansAll(t *testing.T) {
	db := newMemDB(t)
	ctx := context.Background()

	existingWM := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	if err := db.SetWatermark(ctx, documentsScope, existingWM); err != nil {
		t.Fatal(err)
	}

	// All docs are older than the watermark. With Refetch=false, the first record triggers an early stop → collected=0.
	tOld := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	lister := &fakeDocLister{listResp: []backlog.Document{
		docAt("d1", "PROJ", tOld),
	}}

	if err := Documents(ctx, lister, db, Options{Refetch: true}); err != nil {
		t.Fatal(err)
	}

	got, _ := db.SearchDocuments(ctx, "", 10)
	if len(got) != 1 {
		t.Errorf("--refetch should ignore watermark and ingest all docs, got %d", len(got))
	}
}

// On initial sync (zero watermark), StageTotal fires up-front.
func TestDocuments_EmitsTotalOnInitialSync(t *testing.T) {
	db := newMemDB(t)
	ctx := context.Background()

	t1 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	lister := &fakeDocLister{
		listResp: []backlog.Document{docAt("d1", "PROJ", t1)},
		count:    100, // value returned by CountAllDocuments (independent of actual listResp)
	}

	var totalSeen int
	if err := Documents(ctx, lister, db, Options{
		OnProgress: func(stage ProgressStage, n int) {
			if stage == StageTotal {
				totalSeen = n
			}
		},
	}); err != nil {
		t.Fatal(err)
	}

	if totalSeen != 100 {
		t.Errorf("StageTotal n = %d, want 100", totalSeen)
	}
}

// Incremental sync (watermark already set) must NOT fire StageTotal.
func TestDocuments_NoTotalOnIncrementalSync(t *testing.T) {
	db := newMemDB(t)
	ctx := context.Background()

	existingWM := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	if err := db.SetWatermark(ctx, documentsScope, existingWM); err != nil {
		t.Fatal(err)
	}

	lister := &fakeDocLister{
		listResp: []backlog.Document{docAt("d1", "PROJ", existingWM.Add(time.Hour))},
		count:    100,
	}

	var totalEmitted bool
	if err := Documents(ctx, lister, db, Options{
		OnProgress: func(stage ProgressStage, _ int) {
			if stage == StageTotal {
				totalEmitted = true
			}
		},
	}); err != nil {
		t.Fatal(err)
	}
	if totalEmitted {
		t.Error("StageTotal should not fire on incremental sync (watermark set)")
	}
}

func TestDocuments_NothingNewDoesNotTouchWatermark(t *testing.T) {
	db := newMemDB(t)
	ctx := context.Background()

	existingWM := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	if err := db.SetWatermark(ctx, documentsScope, existingWM); err != nil {
		t.Fatal(err)
	}

	tOld := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	lister := &fakeDocLister{listResp: []backlog.Document{
		docAt("d1", "PROJ", tOld),
	}}

	if err := Documents(ctx, lister, db, Options{}); err != nil {
		t.Fatal(err)
	}
	wm, _ := db.Watermark(ctx, documentsScope)
	if !wm.Equal(existingWM) {
		t.Errorf("watermark moved: got %v, want %v", wm, existingWM)
	}
}

func TestDocuments_ListErrorFailsFast(t *testing.T) {
	db := newMemDB(t)
	ctx := context.Background()

	lister := &fakeDocLister{listErr: errors.New("api boom")}
	if err := Documents(ctx, lister, db, Options{}); err == nil {
		t.Fatal("expected error")
	}
}

// Derived fake that observes GetDocument calls via onGetID.
type fakeDocListerSpy struct {
	fakeDocLister
	onGetID func(string)
}

func (s *fakeDocListerSpy) GetDocument(ctx context.Context, id string) (*backlog.Document, error) {
	if s.onGetID != nil {
		s.onGetID(id)
	}
	return s.fakeDocLister.GetDocument(ctx, id)
}

func TestSyncer_ProjectKeysPassedThrough(t *testing.T) {
	db := newMemDB(t)
	ctx := context.Background()

	lister := &fakeLister{}
	if err := Issues(ctx, lister, db, Options{ProjectKeys: []string{"PROJ", "OTHER"}}); err != nil {
		t.Fatal(err)
	}

	got := lister.calls[0].projectKeys
	if len(got) != 2 || got[0] != "PROJ" || got[1] != "OTHER" {
		t.Errorf("projectKeys not passed correctly: got %v", got)
	}
}
