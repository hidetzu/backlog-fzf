package backlog

import "time"

// Issue represents a Backlog issue (minimal fields used for FTS index and sync).
type Issue struct {
	ID          int
	Key         string
	ProjectKey  string
	Summary     string
	Description string
	Status      string
	Assignee    string
	DueDate     *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Project represents a Backlog project (used for key↔id mapping).
type Project struct {
	ID         int
	ProjectKey string
	Name       string
}

// Document represents a Backlog document.
// ID is a string (32 hex chars, e.g. "019e04f9198d7fed902c9d7539e283a0").
// The list API leaves Body empty; `GET /documents/{id}` populates it.
type Document struct {
	ID         string // Backlog document ID
	ProjectKey string // populated by reverse-lookup of projectId via ListProjects
	Title      string
	Body       string // empty in list responses; populated by GetDocument with plain text
	StatusID   int
	CreatedAt  time.Time
	UpdatedAt  time.Time
}
