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

// ErrLegacySchema is returned by Open when an existing DB was created by
// v0.1.0 (trigram FTS). Callers should surface a "delete the index DB
// and re-sync" instruction; the file path is in err.Error().
var ErrLegacySchema = errors.New("index: legacy v0.1.0 schema (trigram FTS) detected; delete the index DB and run `bkfz sync` to rebuild")

// Open opens the SQLite file at path, applying the schema if needed.
// Passing ":memory:" creates an in-memory DB (for tests).
//
// Returns ErrLegacySchema (wrapped) if an existing DB still uses the
// v0.1.0 trigram FTS schema; we don't auto-migrate at this stage and
// instead ask the user to delete the file and re-run `bkfz sync`.
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
	if err := detectLegacySchema(context.Background(), conn, path); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if _, err := conn.ExecContext(context.Background(), schemaSQL); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("index: apply schema: %w", err)
	}
	return &DB{sql: conn}, nil
}

// detectLegacySchema returns a wrapped ErrLegacySchema if issues_fts
// exists with the v0.1.0 trigram tokenizer. Fresh DBs (no issues_fts
// table) and DBs already on the v0.1.1 schema return nil.
func detectLegacySchema(ctx context.Context, conn *sql.DB, path string) error {
	var sqlDef sql.NullString
	err := conn.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='issues_fts'`,
	).Scan(&sqlDef)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("index: detect schema at %s: %w", path, err)
	}
	if !sqlDef.Valid {
		return nil
	}
	if strings.Contains(sqlDef.String, "tokenize='trigram'") || strings.Contains(sqlDef.String, `tokenize="trigram"`) {
		return fmt.Errorf("%w (path: %s)", ErrLegacySchema, path)
	}
	return nil
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

// SearchDocuments returns documents matching the bigram FTS5 query plus a
// LIKE post-filter. For an empty query, returns the most recent `limit`
// rows ordered by updated_at DESC. Returns nil for queries whose every
// word is shorter than 2 runes (bigram cannot match 1-rune queries).
func (db *DB) SearchDocuments(ctx context.Context, query string, limit int) ([]backlog.Document, error) {
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	if strings.TrimSpace(query) == "" {
		return db.searchRecentDocuments(ctx, limit)
	}
	ftsExpr, needles, ok := bigramQuery(query)
	if !ok {
		return nil, nil
	}

	likeWhere, likeArgs := buildLikeWhere(
		[]string{"d.title", "d.body", "d.project_key"}, needles,
	)
	args := append([]any{ftsExpr}, likeArgs...)
	args = append(args, limit)

	q := `
SELECT d.backlog_id, d.project_key, d.title, d.body, d.status_id, d.created_at, d.updated_at
FROM documents_fts
JOIN documents d ON d.id = documents_fts.rowid
WHERE documents_fts MATCH ?` + likeWhere + `
ORDER BY rank
LIMIT ?
`
	rows, err := db.sql.QueryContext(ctx, q, args...)
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

// SearchIssues returns issues matching the bigram FTS5 query plus a LIKE
// post-filter. For an empty query, returns the most recent `limit` rows
// ordered by updated_at DESC (used right after TUI start-up). Returns
// nil for queries whose every word is shorter than 2 runes (bigram
// cannot match 1-rune queries).
func (db *DB) SearchIssues(ctx context.Context, query string, limit int) ([]backlog.Issue, error) {
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	if strings.TrimSpace(query) == "" {
		return db.searchRecent(ctx, limit)
	}
	ftsExpr, needles, ok := bigramQuery(query)
	if !ok {
		return nil, nil
	}
	return db.searchFTS(ctx, ftsExpr, needles, limit)
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

func (db *DB) searchFTS(ctx context.Context, ftsExpr string, needles []string, limit int) ([]backlog.Issue, error) {
	likeWhere, likeArgs := buildLikeWhere(
		[]string{"i.summary", "i.description", "i.status", "i.assignee", "i.project_key", "i.issue_key"},
		needles,
	)
	args := append([]any{ftsExpr}, likeArgs...)
	args = append(args, limit)

	q := `
SELECT i.id, i.issue_key, i.project_key, i.summary, i.description, i.status, i.assignee, i.due_date, i.created_at, i.updated_at
FROM issues_fts
JOIN issues i ON i.id = issues_fts.rowid
WHERE issues_fts MATCH ?` + likeWhere + `
ORDER BY rank
LIMIT ?
`
	rows, err := db.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIssues(rows)
}

// buildLikeWhere builds the AND-joined LIKE post-filter that verifies
// substring contiguity after the bigram MATCH. Each needle must appear
// as a substring in at least one of the supplied fields. Returns the
// SQL fragment (with a leading " AND " when non-empty) and the values
// to bind in order.
//
// LIKE wildcards in the user's input are escaped via ESCAPE '#'.
func buildLikeWhere(fields []string, needles []string) (string, []any) {
	if len(needles) == 0 {
		return "", nil
	}
	var clauses []string
	var args []any
	for _, n := range needles {
		var perField []string
		wild := "%" + likeEscape(n) + "%"
		for _, f := range fields {
			perField = append(perField, f+" LIKE ? ESCAPE '#'")
			args = append(args, wild)
		}
		clauses = append(clauses, "("+strings.Join(perField, " OR ")+")")
	}
	return " AND " + strings.Join(clauses, " AND "), args
}

// likeEscape escapes LIKE wildcards (% _ #) using # as the escape char.
func likeEscape(s string) string {
	return strings.NewReplacer(`#`, `##`, `%`, `#%`, `_`, `#_`).Replace(s)
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
