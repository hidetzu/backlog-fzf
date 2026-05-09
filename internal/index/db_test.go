package index

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/hidetzu/backlog-fzf/internal/backlog"
)

func newMemDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func issueWith(id int, key, projectKey, summary, status, assignee string) backlog.Issue {
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	return backlog.Issue{
		ID:         id,
		Key:        key,
		ProjectKey: projectKey,
		Summary:    summary,
		Status:     status,
		Assignee:   assignee,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

// `:memory:` produces a separate DB per database/sql connection, so
// opening multiple connections leads to inconsistent table/row visibility.
// Open() pins MaxOpenConnections to 1 to avoid that. We assert the value
// here so future edits don't accidentally lift the constraint.
func TestOpen_MemoryDBPinsToOneConnection(t *testing.T) {
	db := newMemDB(t)
	if got := db.sql.Stats().MaxOpenConnections; got != 1 {
		t.Errorf(":memory: DB MaxOpenConnections = %d, want 1", got)
	}
}

func TestOpen_AppliesSchemaIdempotently(t *testing.T) {
	db := newMemDB(t)

	// Re-applying schema must not error (CREATE IF NOT EXISTS)
	if _, err := db.sql.Exec(schemaSQL); err != nil {
		t.Fatalf("re-apply schemaSQL: %v", err)
	}

	// Verify that issues / sync_state / issues_fts all exist.
	for _, name := range []string{"issues", "sync_state", "issues_fts"} {
		var got string
		err := db.sql.QueryRow(
			`SELECT name FROM sqlite_master WHERE name = ?`, name,
		).Scan(&got)
		if err != nil {
			t.Errorf("table %q not found: %v", name, err)
		}
	}
}

func TestUpsertIssues_InsertThenUpdateSameID(t *testing.T) {
	db := newMemDB(t)
	ctx := context.Background()

	first := issueWith(1, "P-1", "P", "first summary", "Open", "alice")
	if err := db.UpsertIssues(ctx, []backlog.Issue{first}); err != nil {
		t.Fatal(err)
	}

	got, err := db.SearchIssues(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Summary != "first summary" {
		t.Fatalf("after insert: %v", got)
	}

	updated := issueWith(1, "P-1", "P", "updated summary", "Closed", "bob")
	if err := db.UpsertIssues(ctx, []backlog.Issue{updated}); err != nil {
		t.Fatal(err)
	}

	got, err = db.SearchIssues(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("after update should still be 1 row, got %d", len(got))
	}
	if got[0].Summary != "updated summary" || got[0].Status != "Closed" || got[0].Assignee != "bob" {
		t.Errorf("after update: %+v", got[0])
	}
}

// The trigram tokenizer only matches queries of 3+ characters, so the
// test queries below are all 3+ chars (this constraint is mentioned in
// the README's known-limitations section).
func TestUpsertIssues_FTSStaysInSyncOnUpdate(t *testing.T) {
	db := newMemDB(t)
	ctx := context.Background()

	first := issueWith(1, "P-1", "P", "認証バグ修正", "Open", "alice")
	if err := db.UpsertIssues(ctx, []backlog.Issue{first}); err != nil {
		t.Fatal(err)
	}
	if hits, _ := db.SearchIssues(ctx, "認証バ", 10); len(hits) != 1 {
		t.Fatalf("expected hit on 認証バ, got %d", len(hits))
	}

	updated := issueWith(1, "P-1", "P", "ログ出力 機能外出し", "Open", "alice")
	if err := db.UpsertIssues(ctx, []backlog.Issue{updated}); err != nil {
		t.Fatal(err)
	}
	if hits, _ := db.SearchIssues(ctx, "認証バ", 10); len(hits) != 0 {
		t.Errorf("after update old summary should not match: got %d hits", len(hits))
	}
	if hits, _ := db.SearchIssues(ctx, "ログ出", 10); len(hits) != 1 {
		t.Errorf("after update new summary should match: got %d hits", len(hits))
	}
}

func TestWatermark_RoundTrip(t *testing.T) {
	db := newMemDB(t)
	ctx := context.Background()

	got, err := db.Watermark(ctx, "issues")
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsZero() {
		t.Errorf("missing scope should return zero, got %v", got)
	}

	t1 := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	if err := db.SetWatermark(ctx, "issues", t1); err != nil {
		t.Fatal(err)
	}
	got, err = db.Watermark(ctx, "issues")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(t1) {
		t.Errorf("after first set: got %v want %v", got, t1)
	}

	t2 := t1.Add(time.Hour)
	if err := db.SetWatermark(ctx, "issues", t2); err != nil {
		t.Fatal(err)
	}
	got, err = db.Watermark(ctx, "issues")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(t2) {
		t.Errorf("after second set: got %v want %v", got, t2)
	}
}

// Verify that FTS5 covers all of summary / description / status /
// assignee / project_key / issue_key.
func TestSearchIssues_FTSCoversAllIndexedFields(t *testing.T) {
	db := newMemDB(t)
	ctx := context.Background()

	issues := []backlog.Issue{
		issueWith(1, "ALPHA-1", "ALPHA", "summary one", "未対応", "alice"),
		issueWith(2, "BETA-2", "BETA", "summary two", "完了", "bob"),
		issueWith(3, "ALPHA-3", "ALPHA", "summary three", "処理中", "carol"),
	}
	if err := db.UpsertIssues(ctx, issues); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name      string
		query     string
		wantCount int
	}{
		{"summary text", "two", 1},
		{"assignee", "carol", 1},
		{"status (Japanese)", "未対応", 1},
		{"project_key", "BETA", 1},
		{"issue_key", "ALPHA-3", 1},
		{"project_key matches multiple", "ALPHA", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := db.SearchIssues(ctx, tc.query, 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tc.wantCount {
				keys := make([]string, len(got))
				for i, g := range got {
					keys[i] = g.Key
				}
				t.Errorf("query %q: got %d hits %v, want %d", tc.query, len(got), keys, tc.wantCount)
			}
		})
	}
}

func TestSearchIssues_EmptyQueryReturnsRecentDescending(t *testing.T) {
	db := newMemDB(t)
	ctx := context.Background()

	older := issueWith(1, "P-1", "P", "older", "Open", "")
	older.UpdatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := issueWith(2, "P-2", "P", "newer", "Open", "")
	newer.UpdatedAt = time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	if err := db.UpsertIssues(ctx, []backlog.Issue{older, newer}); err != nil {
		t.Fatal(err)
	}
	got, err := db.SearchIssues(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
	if got[0].Key != "P-2" || got[1].Key != "P-1" {
		t.Errorf("expected newer first, got %s, %s", got[0].Key, got[1].Key)
	}
}

func TestSearchIssues_LimitApplied(t *testing.T) {
	db := newMemDB(t)
	ctx := context.Background()

	var batch []backlog.Issue
	for i := 1; i <= 5; i++ {
		batch = append(batch, issueWith(i, fmt.Sprintf("P-%d", i), "P", "summary", "Open", ""))
	}
	if err := db.UpsertIssues(ctx, batch); err != nil {
		t.Fatal(err)
	}
	got, err := db.SearchIssues(ctx, "summary", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("limit=3 expected 3, got %d", len(got))
	}
}

func TestUpsertIssues_PreservesDueDateRoundTrip(t *testing.T) {
	db := newMemDB(t)
	ctx := context.Background()

	due := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	i := issueWith(1, "P-1", "P", "with due", "Open", "alice")
	i.DueDate = &due

	if err := db.UpsertIssues(ctx, []backlog.Issue{i}); err != nil {
		t.Fatal(err)
	}
	got, err := db.SearchIssues(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
	if got[0].DueDate == nil || !got[0].DueDate.Equal(due) {
		t.Errorf("DueDate roundtrip: got %v want %v", got[0].DueDate, due)
	}
}

func TestGetIssue_FoundAndNotFound(t *testing.T) {
	db := newMemDB(t)
	ctx := context.Background()

	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	in := backlog.Issue{
		ID: 1, Key: "P-1", ProjectKey: "P", Summary: "found me", Status: "Open",
		Assignee: "alice", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.UpsertIssues(ctx, []backlog.Issue{in}); err != nil {
		t.Fatal(err)
	}

	got, err := db.GetIssue(ctx, "P-1")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if got == nil || got.Key != "P-1" || got.Summary != "found me" || got.Assignee != "alice" {
		t.Errorf("GetIssue returned %+v", got)
	}

	if _, err := db.GetIssue(ctx, "NOPE-99"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing key: expected ErrNotFound, got %v", err)
	}
}

// --- documents ---

func docWith(id, projectKey, title, body string, updatedAt time.Time) backlog.Document {
	return backlog.Document{
		ID:         id,
		ProjectKey: projectKey,
		Title:      title,
		Body:       body,
		CreatedAt:  updatedAt,
		UpdatedAt:  updatedAt,
	}
}

func TestUpsertDocuments_InsertThenUpdateSameBacklogID(t *testing.T) {
	db := newMemDB(t)
	ctx := context.Background()

	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	first := docWith("doc1", "PROJ", "first title", "first body", now)
	if err := db.UpsertDocuments(ctx, []backlog.Document{first}); err != nil {
		t.Fatal(err)
	}

	got, err := db.SearchDocuments(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "first title" {
		t.Fatalf("after insert: %+v", got)
	}

	updated := docWith("doc1", "PROJ", "updated title", "updated body", now.Add(time.Hour))
	if err := db.UpsertDocuments(ctx, []backlog.Document{updated}); err != nil {
		t.Fatal(err)
	}
	got, err = db.SearchDocuments(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("after update should still be 1 row, got %d", len(got))
	}
	if got[0].Title != "updated title" || got[0].Body != "updated body" {
		t.Errorf("after update: %+v", got[0])
	}
}

func TestSearchDocuments_FTSCoversTitleBodyProjectKey(t *testing.T) {
	db := newMemDB(t)
	ctx := context.Background()

	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	if err := db.UpsertDocuments(ctx, []backlog.Document{
		docWith("doc1", "ALPHA", "認証ガイド", "OAuth の流れを説明する", now),
		docWith("doc2", "BETA", "デプロイ手順", "本番環境の更新手順", now),
	}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name      string
		query     string
		wantCount int
	}{
		{"title text", "認証ガイ", 1},
		{"body text", "OAuth", 1},
		{"project_key", "BETA", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := db.SearchDocuments(ctx, tc.query, 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tc.wantCount {
				ids := make([]string, len(got))
				for i, g := range got {
					ids[i] = g.ID
				}
				t.Errorf("query %q: got %d hits %v, want %d", tc.query, len(got), ids, tc.wantCount)
			}
		})
	}
}

func TestSearchDocuments_EmptyQueryReturnsRecentDescending(t *testing.T) {
	db := newMemDB(t)
	ctx := context.Background()

	older := docWith("d-older", "P", "older", "x", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	newer := docWith("d-newer", "P", "newer", "x", time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))
	if err := db.UpsertDocuments(ctx, []backlog.Document{older, newer}); err != nil {
		t.Fatal(err)
	}
	got, err := db.SearchDocuments(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "d-newer" || got[1].ID != "d-older" {
		t.Errorf("expected newer first, got %v", got)
	}
}

func TestGetDocument_FoundAndNotFound(t *testing.T) {
	db := newMemDB(t)
	ctx := context.Background()

	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	if err := db.UpsertDocuments(ctx, []backlog.Document{
		docWith("doc1", "PROJ", "Found", "body", now),
	}); err != nil {
		t.Fatal(err)
	}

	got, err := db.GetDocument(ctx, "doc1")
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if got == nil || got.Title != "Found" {
		t.Errorf("GetDocument returned %+v", got)
	}

	if _, err := db.GetDocument(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing key: expected ErrNotFound, got %v", err)
	}
}

func TestBuildFTSQuery(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"foo", `"foo"`},
		{"foo bar", `"foo" "bar"`},
		{"   foo   bar   ", `"foo" "bar"`},
		{`with "quote"`, `"with" """quote"""`},
		{"", ""},
		{"   ", ""},
	}
	for _, tc := range cases {
		got := buildFTSQuery(tc.in)
		if got != tc.want {
			t.Errorf("buildFTSQuery(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
