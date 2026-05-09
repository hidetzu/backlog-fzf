package index

// schemaSQL is the initial DDL for the backlog-fzf local index.
// Open applies it when the DB is empty (idempotent thanks to CREATE IF NOT EXISTS).
const schemaSQL = `
CREATE TABLE IF NOT EXISTS issues (
  id INTEGER PRIMARY KEY,
  issue_key TEXT UNIQUE NOT NULL,
  project_key TEXT NOT NULL,
  summary TEXT NOT NULL,
  description TEXT,
  status TEXT,
  assignee TEXT,
  due_date TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sync_state (
  scope TEXT PRIMARY KEY,
  last_synced_at TEXT NOT NULL,
  last_count INTEGER
);

-- FTS5 index. The trigram tokenizer handles Japanese + substring matching well.
-- In addition to summary / description, we index metadata (status, assignee,
-- project_key, issue_key) so that queries like "alice", "Open", or "PROJ-123"
-- all work from a single search box.
CREATE VIRTUAL TABLE IF NOT EXISTS issues_fts USING fts5(
  summary, description, status, assignee, project_key, issue_key,
  content='issues', content_rowid='id',
  tokenize='trigram'
);

CREATE TRIGGER IF NOT EXISTS issues_ai AFTER INSERT ON issues BEGIN
  INSERT INTO issues_fts(rowid, summary, description, status, assignee, project_key, issue_key)
  VALUES (new.id, new.summary, new.description, new.status, new.assignee, new.project_key, new.issue_key);
END;

CREATE TRIGGER IF NOT EXISTS issues_ad AFTER DELETE ON issues BEGIN
  INSERT INTO issues_fts(issues_fts, rowid, summary, description, status, assignee, project_key, issue_key)
  VALUES('delete', old.id, old.summary, old.description, old.status, old.assignee, old.project_key, old.issue_key);
END;

CREATE TRIGGER IF NOT EXISTS issues_au AFTER UPDATE ON issues BEGIN
  INSERT INTO issues_fts(issues_fts, rowid, summary, description, status, assignee, project_key, issue_key)
  VALUES('delete', old.id, old.summary, old.description, old.status, old.assignee, old.project_key, old.issue_key);
  INSERT INTO issues_fts(rowid, summary, description, status, assignee, project_key, issue_key)
  VALUES (new.id, new.summary, new.description, new.status, new.assignee, new.project_key, new.issue_key);
END;

-- Documents: Backlog IDs are strings, so we use a synthetic INTEGER PRIMARY KEY
-- and keep backlog_id under a UNIQUE constraint (FTS5 contentless tables
-- require an INTEGER rowid).
CREATE TABLE IF NOT EXISTS documents (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  backlog_id TEXT UNIQUE NOT NULL,
  project_key TEXT NOT NULL,
  title TEXT NOT NULL,
  body TEXT,
  status_id INTEGER,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE VIRTUAL TABLE IF NOT EXISTS documents_fts USING fts5(
  title, body, project_key,
  content='documents', content_rowid='id',
  tokenize='trigram'
);

CREATE TRIGGER IF NOT EXISTS documents_ai AFTER INSERT ON documents BEGIN
  INSERT INTO documents_fts(rowid, title, body, project_key)
  VALUES (new.id, new.title, new.body, new.project_key);
END;

CREATE TRIGGER IF NOT EXISTS documents_ad AFTER DELETE ON documents BEGIN
  INSERT INTO documents_fts(documents_fts, rowid, title, body, project_key)
  VALUES('delete', old.id, old.title, old.body, old.project_key);
END;

CREATE TRIGGER IF NOT EXISTS documents_au AFTER UPDATE ON documents BEGIN
  INSERT INTO documents_fts(documents_fts, rowid, title, body, project_key)
  VALUES('delete', old.id, old.title, old.body, old.project_key);
  INSERT INTO documents_fts(rowid, title, body, project_key)
  VALUES (new.id, new.title, new.body, new.project_key);
END;
`
