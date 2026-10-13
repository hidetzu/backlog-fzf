package index

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hidetzu/backlog-fzf/internal/backlog"
)

// Semantic search storage: one float32 vector per (kind, ref_id, chunk)
// in a plain table, searched by brute force in Go. The tables are created
// lazily by EnsureEmbeddingSchema, so a DB belonging to a user who never
// enabled semantic search is byte-for-byte unchanged.

// Kind values stored in embeddings.kind.
const (
	KindIssue    = "issue"
	KindDocument = "doc"
)

const embeddingSchemaSQL = `
CREATE TABLE IF NOT EXISTS embeddings (
  kind         TEXT    NOT NULL,
  ref_id       INTEGER NOT NULL,
  chunk        INTEGER NOT NULL,
  content_hash TEXT    NOT NULL,
  vec          BLOB    NOT NULL,
  PRIMARY KEY (kind, ref_id, chunk)
);
CREATE TABLE IF NOT EXISTS embedding_meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
`

// ErrNoEmbeddings is returned by SearchVectors when the semantic index has
// never been built (tables missing).
var ErrNoEmbeddings = errors.New("semantic index not built")

// ErrDimensionMismatch is returned when a query vector's size differs from
// the stored vectors (model or dimensions changed without a rebuild).
var ErrDimensionMismatch = errors.New("embedding dimensions do not match the index")

// Chunk is one embedded piece of a record.
type Chunk struct {
	Index int
	Hash  string
	Vec   []float32
}

// EmbeddingRecord is the full chunk set for one record.
type EmbeddingRecord struct {
	Kind   string
	RefID  int64
	Chunks []Chunk
}

// VectorHit is one search result, aggregated per (kind, ref_id).
type VectorHit struct {
	Kind  string
	RefID int64
	Chunk int     // chunk that produced the best score
	Score float32 // cosine similarity (vectors are stored normalized)
}

// EmbeddingMeta describes the model that produced the stored vectors.
type EmbeddingMeta struct {
	Model      string
	Dimensions int
}

// Compatible reports whether vectors produced by m can serve a config
// asking for model / dims (dims <= 0 = model native, accepts any size).
func (m EmbeddingMeta) Compatible(model string, dims int) bool {
	return m.Model == model && (dims <= 0 || m.Dimensions == dims)
}

// HasEmbeddingSchema reports whether the embeddings table exists.
func (db *DB) HasEmbeddingSchema(ctx context.Context) (bool, error) {
	var n int
	err := db.sql.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='embeddings'`).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("index: check embedding schema: %w", err)
	}
	return n > 0, nil
}

// EnsureEmbeddingSchema creates the embedding tables if missing.
func (db *DB) EnsureEmbeddingSchema(ctx context.Context) error {
	if _, err := db.sql.ExecContext(ctx, embeddingSchemaSQL); err != nil {
		return fmt.Errorf("index: apply embedding schema: %w", err)
	}
	return nil
}

// EmbeddingMeta returns the stored model info, or nil when none was
// recorded yet (fresh index).
func (db *DB) EmbeddingMeta(ctx context.Context) (*EmbeddingMeta, error) {
	ok, err := db.HasEmbeddingSchema(ctx)
	if err != nil || !ok {
		return nil, err
	}
	rows, err := db.sql.QueryContext(ctx, `SELECT key, value FROM embedding_meta`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var m EmbeddingMeta
	found := false
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		switch k {
		case "model":
			m.Model = v
			found = true
		case "dimensions":
			m.Dimensions, _ = strconv.Atoi(v)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return &m, nil
}

// SetEmbeddingMeta records the model info.
func (db *DB) SetEmbeddingMeta(ctx context.Context, m EmbeddingMeta) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for k, v := range map[string]string{"model": m.Model, "dimensions": strconv.Itoa(m.Dimensions)} {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO embedding_meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
			k, v); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// EmbeddingHashes returns, per ref_id of the given kind, the content
// hashes in chunk order. Used to detect records whose text changed.
func (db *DB) EmbeddingHashes(ctx context.Context, kind string) (map[int64][]string, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT ref_id, content_hash FROM embeddings WHERE kind = ? ORDER BY ref_id, chunk`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]string{}
	for rows.Next() {
		var id int64
		var h string
		if err := rows.Scan(&id, &h); err != nil {
			return nil, err
		}
		out[id] = append(out[id], h)
	}
	return out, rows.Err()
}

// UpsertEmbeddings replaces the chunk set of each record in one
// transaction (stale chunk numbers are removed).
func (db *DB) UpsertEmbeddings(ctx context.Context, recs []EmbeddingRecord) error {
	if len(recs) == 0 {
		return nil
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	del, err := tx.PrepareContext(ctx, `DELETE FROM embeddings WHERE kind = ? AND ref_id = ?`)
	if err != nil {
		return err
	}
	defer del.Close()
	ins, err := tx.PrepareContext(ctx,
		`INSERT INTO embeddings(kind, ref_id, chunk, content_hash, vec) VALUES(?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer ins.Close()
	for _, r := range recs {
		if _, err := del.ExecContext(ctx, r.Kind, r.RefID); err != nil {
			return err
		}
		for _, c := range r.Chunks {
			if _, err := ins.ExecContext(ctx, r.Kind, r.RefID, c.Index, c.Hash, EncodeVector(c.Vec)); err != nil {
				return fmt.Errorf("upsert embedding %s/%d/%d: %w", r.Kind, r.RefID, c.Index, err)
			}
		}
	}
	return tx.Commit()
}

// DeleteOrphanEmbeddings removes vectors whose record no longer exists.
func (db *DB) DeleteOrphanEmbeddings(ctx context.Context) (int64, error) {
	res, err := db.sql.ExecContext(ctx, `
DELETE FROM embeddings
WHERE (kind = ? AND ref_id NOT IN (SELECT id FROM issues))
   OR (kind = ? AND ref_id NOT IN (SELECT id FROM documents))
   OR kind NOT IN (?, ?)`, KindIssue, KindDocument, KindIssue, KindDocument)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ClearEmbeddings drops every vector and the meta rows (used by --rebuild).
func (db *DB) ClearEmbeddings(ctx context.Context) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM embeddings`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM embedding_meta`); err != nil {
		return err
	}
	return tx.Commit()
}

// CountEmbeddings returns the number of stored vectors (chunks).
func (db *DB) CountEmbeddings(ctx context.Context) (int, error) {
	var n int
	err := db.sql.QueryRowContext(ctx, `SELECT count(*) FROM embeddings`).Scan(&n)
	return n, err
}

// SearchVectors scans every stored vector, keeps the best chunk score per
// (kind, ref_id), and returns the top k records by cosine similarity. The
// query must be L2-normalized (NormalizeVector) for scores to be cosines.
func (db *DB) SearchVectors(ctx context.Context, query []float32, k int) ([]VectorHit, error) {
	ok, err := db.HasEmbeddingSchema(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNoEmbeddings
	}
	if k <= 0 {
		k = defaultSearchLimit
	}
	rows, err := db.sql.QueryContext(ctx, `SELECT kind, ref_id, chunk, vec FROM embeddings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type key struct {
		kind string
		id   int64
	}
	best := map[key]VectorHit{}
	buf := make([]float32, len(query))
	for rows.Next() {
		var (
			kind  string
			id    int64
			chunk int
			blob  []byte
		)
		if err := rows.Scan(&kind, &id, &chunk, &blob); err != nil {
			return nil, err
		}
		if len(blob) != 4*len(query) {
			return nil, fmt.Errorf("%w: query has %d dimensions, stored vector has %d", ErrDimensionMismatch, len(query), len(blob)/4)
		}
		decodeInto(buf, blob)
		var s float32
		for i, q := range query {
			s += q * buf[i]
		}
		kk := key{kind, id}
		if cur, seen := best[kk]; !seen || s > cur.Score {
			best[kk] = VectorHit{Kind: kind, RefID: id, Chunk: chunk, Score: s}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	hits := make([]VectorHit, 0, len(best))
	for _, h := range best {
		hits = append(hits, h)
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		if hits[i].Kind != hits[j].Kind {
			return hits[i].Kind < hits[j].Kind
		}
		return hits[i].RefID < hits[j].RefID
	})
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits, nil
}

// IssuesByIDs returns the issues with the given rowids, keyed by id.
func (db *DB) IssuesByIDs(ctx context.Context, ids []int64) (map[int64]backlog.Issue, error) {
	out := map[int64]backlog.Issue{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := db.sql.QueryContext(ctx, `
SELECT id, issue_key, project_key, summary, description, status, assignee, due_date, created_at, updated_at
FROM issues WHERE id IN (`+placeholders(len(ids))+`)`, int64Args(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	issues, err := scanIssues(rows)
	if err != nil {
		return nil, err
	}
	for _, i := range issues {
		out[int64(i.ID)] = i
	}
	return out, nil
}

// DocumentsByIDs returns the documents with the given rowids, keyed by id.
func (db *DB) DocumentsByIDs(ctx context.Context, ids []int64) (map[int64]backlog.Document, error) {
	out := map[int64]backlog.Document{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := db.sql.QueryContext(ctx, `
SELECT id, backlog_id, project_key, title, body, status_id, created_at, updated_at
FROM documents WHERE id IN (`+placeholders(len(ids))+`)`, int64Args(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id         int64
			d          backlog.Document
			body       sql.NullString
			statusID   sql.NullInt64
			created    string
			updated    string
			projectKey string
		)
		if err := rows.Scan(&id, &d.ID, &projectKey, &d.Title, &body, &statusID, &created, &updated); err != nil {
			return nil, err
		}
		d.ProjectKey = projectKey
		d.Body = body.String
		d.StatusID = int(statusID.Int64)
		d.CreatedAt, d.UpdatedAt = parseTimes(created, updated)
		out[id] = d
	}
	return out, rows.Err()
}

// IssueText is the embedding input source for one issue row.
type IssueText struct {
	ID                   int64
	Summary, Description string
}

// DocumentText is the embedding input source for one document row.
type DocumentText struct {
	ID          int64
	Title, Body string
}

// AllIssueTexts returns every issue's embedding source. The whole set is
// materialized (a few MB for 10k issues) so callers can run other queries
// afterwards even on the single-connection :memory: DB.
func (db *DB) AllIssueTexts(ctx context.Context) ([]IssueText, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT id, summary, IFNULL(description, '') FROM issues ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IssueText
	for rows.Next() {
		var t IssueText
		if err := rows.Scan(&t.ID, &t.Summary, &t.Description); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// AllDocumentTexts returns every document's embedding source.
func (db *DB) AllDocumentTexts(ctx context.Context) ([]DocumentText, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT id, title, IFNULL(body, '') FROM documents ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DocumentText
	for rows.Next() {
		var t DocumentText
		if err := rows.Scan(&t.ID, &t.Title, &t.Body); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// EncodeVector serializes v as little-endian float32.
func EncodeVector(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}

// DecodeVector parses a blob written by EncodeVector.
func DecodeVector(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	decodeInto(v, b)
	return v
}

func decodeInto(dst []float32, b []byte) {
	for i := range dst {
		dst[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
}

// NormalizeVector scales v to unit length in place (no-op for zero vectors).
func NormalizeVector(v []float32) {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	if s == 0 {
		return
	}
	n := float32(1 / math.Sqrt(s))
	for i := range v {
		v[i] *= n
	}
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func int64Args(ids []int64) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

func parseTimes(created, updated string) (c, u time.Time) {
	c, _ = time.Parse(time.RFC3339, created)
	u, _ = time.Parse(time.RFC3339, updated)
	return c, u
}
