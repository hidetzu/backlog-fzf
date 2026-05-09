package index

// schemaSQL is the initial DDL for the backlog-fzf local index (v0.1.1+).
// Open applies it when the DB is empty (idempotent thanks to CREATE IF NOT EXISTS).
//
// Search strategy: bigram pre-processing + FTS5 with the unicode61
// tokenizer. Each indexed text is converted to space-separated rune-pair
// bigrams via the BIGRAM(text) SQL function (registered in Go) before
// being inserted into the contentless FTS5 table; queries follow the
// same shape so 2+ character queries — including Japanese — match
// correctly. A LIKE post-filter verifies substring contiguity.
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

-- Contentless FTS5: original text lives in the issues table; the FTS
-- columns store bigram tokens (BIGRAM()) which the unicode61 tokenizer
-- then splits on whitespace to produce the search index.
CREATE VIRTUAL TABLE IF NOT EXISTS issues_fts USING fts5(
  summary, description, status, assignee, project_key, issue_key,
  content='',
  contentless_delete=1,
  tokenize='unicode61'
);

CREATE TRIGGER IF NOT EXISTS issues_ai AFTER INSERT ON issues BEGIN
  INSERT INTO issues_fts(rowid, summary, description, status, assignee, project_key, issue_key)
  VALUES (new.id,
          BIGRAM(new.summary),
          BIGRAM(IFNULL(new.description, '')),
          BIGRAM(IFNULL(new.status, '')),
          BIGRAM(IFNULL(new.assignee, '')),
          BIGRAM(new.project_key),
          BIGRAM(new.issue_key));
END;

CREATE TRIGGER IF NOT EXISTS issues_ad AFTER DELETE ON issues BEGIN
  DELETE FROM issues_fts WHERE rowid = old.id;
END;

CREATE TRIGGER IF NOT EXISTS issues_au AFTER UPDATE ON issues BEGIN
  DELETE FROM issues_fts WHERE rowid = old.id;
  INSERT INTO issues_fts(rowid, summary, description, status, assignee, project_key, issue_key)
  VALUES (new.id,
          BIGRAM(new.summary),
          BIGRAM(IFNULL(new.description, '')),
          BIGRAM(IFNULL(new.status, '')),
          BIGRAM(IFNULL(new.assignee, '')),
          BIGRAM(new.project_key),
          BIGRAM(new.issue_key));
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
  content='',
  contentless_delete=1,
  tokenize='unicode61'
);

CREATE TRIGGER IF NOT EXISTS documents_ai AFTER INSERT ON documents BEGIN
  INSERT INTO documents_fts(rowid, title, body, project_key)
  VALUES (new.id,
          BIGRAM(new.title),
          BIGRAM(IFNULL(new.body, '')),
          BIGRAM(new.project_key));
END;

CREATE TRIGGER IF NOT EXISTS documents_ad AFTER DELETE ON documents BEGIN
  DELETE FROM documents_fts WHERE rowid = old.id;
END;

CREATE TRIGGER IF NOT EXISTS documents_au AFTER UPDATE ON documents BEGIN
  DELETE FROM documents_fts WHERE rowid = old.id;
  INSERT INTO documents_fts(rowid, title, body, project_key)
  VALUES (new.id,
          BIGRAM(new.title),
          BIGRAM(IFNULL(new.body, '')),
          BIGRAM(new.project_key));
END;
`
