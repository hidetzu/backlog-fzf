package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver

	"github.com/hidetzu/backlog-fzf/internal/backlog"
)

// ErrNotFound is returned by GetIssue when no issue matches the given issue_key.
var ErrNotFound = errors.New("issue not found")

// defaultSearchLimit is the default count used when SearchIssues receives limit <= 0.
const defaultSearchLimit = 50

// DB is the local index for backlog-fzf (SQLite + FTS5).
type DB struct {
	sql *sql.DB
}

// Open opens the SQLite file at path, applying the schema if needed.
// Passing ":memory:" creates an in-memory DB (for tests).
//
// `:memory:` caveat: database/sql's connection pool creates a separate
// in-memory DB per connection, so with multiple connections, tables and
// rows are not visible across them. For test use we pin to a single
// connection via SetMaxOpenConns(1).
// (An alternative is a shared-cache DSN like "file::memory:?cache=shared",
// but SQLite's behavior there is finicky, so we go with max=1.)
func Open(path string) (*DB, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("index: open: %w", err)
	}
	if path == ":memory:" {
		conn.SetMaxOpenConns(1)
	}
	if _, err := conn.ExecContext(context.Background(), schemaSQL); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("index: apply schema: %w", err)
	}
	return &DB{sql: conn}, nil
}

// Close closes the DB handle.
func (db *DB) Close() error {
	return db.sql.Close()
}

// UpsertIssues upserts the given issues into the local DB.
// issues_fts is updated automatically via FTS5 triggers.
// All records are processed in a single transaction (partial failure rolls everything back).
func (db *DB) UpsertIssues(ctx context.Context, issues []backlog.Issue) error {
	if len(issues) == 0 {
		return nil
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO issues (id, issue_key, project_key, summary, description, status, assignee, due_date, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  issue_key = excluded.issue_key,
  project_key = excluded.project_key,
  summary = excluded.summary,
  description = excluded.description,
  status = excluded.status,
  assignee = excluded.assignee,
  due_date = excluded.due_date,
  created_at = excluded.created_at,
  updated_at = excluded.updated_at
`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, i := range issues {
		var dueDate any
		if i.DueDate != nil {
			dueDate = i.DueDate.UTC().Format(time.RFC3339)
		}
		if _, err := stmt.ExecContext(ctx,
			i.ID, i.Key, i.ProjectKey, i.Summary, i.Description, i.Status, i.Assignee,
			dueDate, i.CreatedAt.UTC().Format(time.RFC3339), i.UpdatedAt.UTC().Format(time.RFC3339),
		); err != nil {
			return fmt.Errorf("upsert issue %s: %w", i.Key, err)
		}
	}
	return tx.Commit()
}

// Watermark returns the last-synced timestamp for the given scope (e.g. "issues").
// If no record exists, returns the zero value (time.Time{}).
func (db *DB) Watermark(ctx context.Context, scope string) (time.Time, error) {
	var ts string
	err := db.sql.QueryRowContext(ctx,
		`SELECT last_synced_at FROM sync_state WHERE scope = ?`, scope,
	).Scan(&ts)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339, ts)
}

// SetWatermark updates the last-synced timestamp for the given scope.
func (db *DB) SetWatermark(ctx context.Context, scope string, t time.Time) error {
	_, err := db.sql.ExecContext(ctx, `
INSERT INTO sync_state (scope, last_synced_at, last_count) VALUES (?, ?, 0)
ON CONFLICT(scope) DO UPDATE SET last_synced_at = excluded.last_synced_at
`, scope, t.UTC().Format(time.RFC3339))
	return err
}

// GetIssue fetches a single issue by issue_key. Returns ErrNotFound if missing.
func (db *DB) GetIssue(ctx context.Context, key string) (*backlog.Issue, error) {
	rows, err := db.sql.QueryContext(ctx, `
SELECT id, issue_key, project_key, summary, description, status, assignee, due_date, created_at, updated_at
FROM issues
WHERE issue_key = ?
LIMIT 1
`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	issues, err := scanIssues(rows)
	if err != nil {
		return nil, err
	}
	if len(issues) == 0 {
		return nil, ErrNotFound
	}
	return &issues[0], nil
}

// UpsertDocuments upserts the given documents into the local DB.
// documents_fts is updated automatically via FTS5 triggers.
// All records are processed in a single transaction (partial failure rolls everything back).
func (db *DB) UpsertDocuments(ctx context.Context, docs []backlog.Document) error {
	if len(docs) == 0 {
		return nil
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO documents (backlog_id, project_key, title, body, status_id, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(backlog_id) DO UPDATE SET
  project_key = excluded.project_key,
  title = excluded.title,
  body = excluded.body,
  status_id = excluded.status_id,
  created_at = excluded.created_at,
  updated_at = excluded.updated_at
`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, d := range docs {
		if _, err := stmt.ExecContext(ctx,
			d.ID, d.ProjectKey, d.Title, d.Body, d.StatusID,
			d.CreatedAt.UTC().Format(time.RFC3339),
			d.UpdatedAt.UTC().Format(time.RFC3339),
		); err != nil {
			return fmt.Errorf("upsert document %s: %w", d.ID, err)
		}
	}
	return tx.Commit()
}

// GetDocument fetches a single document by backlog_id.
func (db *DB) GetDocument(ctx context.Context, backlogID string) (*backlog.Document, error) {
	rows, err := db.sql.QueryContext(ctx, `
SELECT backlog_id, project_key, title, body, status_id, created_at, updated_at
FROM documents
WHERE backlog_id = ?
LIMIT 1
`, backlogID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	docs, err := scanDocuments(rows)
	if err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return nil, ErrNotFound
	}
	return &docs[0], nil
}

// SearchDocuments returns documents matching the FTS5 query.
// For an empty query, returns the most recent `limit` rows ordered by updated_at DESC.
func (db *DB) SearchDocuments(ctx context.Context, query string, limit int) ([]backlog.Document, error) {
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	if strings.TrimSpace(query) == "" {
		return db.searchRecentDocuments(ctx, limit)
	}
	rows, err := db.sql.QueryContext(ctx, `
SELECT d.backlog_id, d.project_key, d.title, d.body, d.status_id, d.created_at, d.updated_at
FROM documents_fts
JOIN documents d ON d.id = documents_fts.rowid
WHERE documents_fts MATCH ?
ORDER BY rank
LIMIT ?
`, buildFTSQuery(query), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDocuments(rows)
}

func (db *DB) searchRecentDocuments(ctx context.Context, limit int) ([]backlog.Document, error) {
	rows, err := db.sql.QueryContext(ctx, `
SELECT backlog_id, project_key, title, body, status_id, created_at, updated_at
FROM documents
ORDER BY updated_at DESC
LIMIT ?
`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDocuments(rows)
}

func scanDocuments(rows *sql.Rows) ([]backlog.Document, error) {
	var out []backlog.Document
	for rows.Next() {
		var (
			d                      backlog.Document
			body                   sql.NullString
			statusID               sql.NullInt64
			createdStr, updatedStr string
		)
		if err := rows.Scan(
			&d.ID, &d.ProjectKey, &d.Title, &body, &statusID, &createdStr, &updatedStr,
		); err != nil {
			return nil, err
		}
		d.Body = body.String
		d.StatusID = int(statusID.Int64)
		if t, err := time.Parse(time.RFC3339, createdStr); err == nil {
			d.CreatedAt = t
		}
		if t, err := time.Parse(time.RFC3339, updatedStr); err == nil {
			d.UpdatedAt = t
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// SearchIssues returns issues matching the FTS5 query.
// For an empty query, returns the most recent `limit` rows ordered by
// updated_at DESC (used right after TUI start-up).
func (db *DB) SearchIssues(ctx context.Context, query string, limit int) ([]backlog.Issue, error) {
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	if strings.TrimSpace(query) == "" {
		return db.searchRecent(ctx, limit)
	}
	return db.searchFTS(ctx, query, limit)
}

func (db *DB) searchRecent(ctx context.Context, limit int) ([]backlog.Issue, error) {
	rows, err := db.sql.QueryContext(ctx, `
SELECT id, issue_key, project_key, summary, description, status, assignee, due_date, created_at, updated_at
FROM issues
ORDER BY updated_at DESC
LIMIT ?
`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIssues(rows)
}

func (db *DB) searchFTS(ctx context.Context, query string, limit int) ([]backlog.Issue, error) {
	rows, err := db.sql.QueryContext(ctx, `
SELECT i.id, i.issue_key, i.project_key, i.summary, i.description, i.status, i.assignee, i.due_date, i.created_at, i.updated_at
FROM issues_fts
JOIN issues i ON i.id = issues_fts.rowid
WHERE issues_fts MATCH ?
ORDER BY rank
LIMIT ?
`, buildFTSQuery(query), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIssues(rows)
}

// buildFTSQuery converts user input into FTS5 MATCH syntax.
// Whitespace-split tokens are each wrapped in double quotes and joined
// with AND (FTS5's default connector).
// Example: `auth fix` → `"auth" "fix"`. Embedded `"` is escaped as `""`.
func buildFTSQuery(s string) string {
	tokens := strings.Fields(s)
	for i, t := range tokens {
		t = strings.ReplaceAll(t, `"`, `""`)
		tokens[i] = `"` + t + `"`
	}
	return strings.Join(tokens, " ")
}

func scanIssues(rows *sql.Rows) ([]backlog.Issue, error) {
	var out []backlog.Issue
	for rows.Next() {
		var (
			i                                  backlog.Issue
			description, status, assignee, due sql.NullString
			createdStr, updatedStr             string
		)
		if err := rows.Scan(
			&i.ID, &i.Key, &i.ProjectKey, &i.Summary,
			&description, &status, &assignee, &due,
			&createdStr, &updatedStr,
		); err != nil {
			return nil, err
		}
		i.Description = description.String
		i.Status = status.String
		i.Assignee = assignee.String
		if due.Valid {
			if t, err := time.Parse(time.RFC3339, due.String); err == nil {
				i.DueDate = &t
			}
		}
		if t, err := time.Parse(time.RFC3339, createdStr); err == nil {
			i.CreatedAt = t
		}
		if t, err := time.Parse(time.RFC3339, updatedStr); err == nil {
			i.UpdatedAt = t
		}
		out = append(out, i)
	}
	return out, rows.Err()
}
