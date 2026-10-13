// Command demo-seed writes a fictional Backlog space into a bkfz config
// and index DB, so the README demo (docs/demo.tape) can be recorded
// without an API key, network access, or any real customer data.
//
// Usage:
//
//	go run ./scripts/demo-seed <dir>
//
// It creates <dir>/config/bkfz/config.yaml and <dir>/data/bkfz/index.db;
// point XDG_CONFIG_HOME / XDG_DATA_HOME at those to use them. An existing
// index DB under <dir> is replaced so the output stays deterministic.
// The data itself lives in scripts/demodata (shared with scripts/eval).
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hidetzu/backlog-fzf/internal/config"
	"github.com/hidetzu/backlog-fzf/internal/index"
	"github.com/hidetzu/backlog-fzf/scripts/demodata"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: demo-seed <dir>")
		os.Exit(2)
	}
	if err := run(context.Background(), os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "demo-seed:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, dir string) error {
	// config.Save / config.DataPath resolve paths from XDG_* variables.
	if err := os.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config")); err != nil {
		return err
	}
	if err := os.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data")); err != nil {
		return err
	}

	if err := config.Save(&config.Config{
		SpaceDomain: "example.backlog.com",
		Projects:    demodata.Projects,
	}); err != nil {
		return err
	}

	dbPath, err := config.DataPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return err
	}
	// Remove SQLite sidecar files too, so a stale journal from an
	// interrupted run can't be replayed onto the fresh DB.
	for _, suffix := range []string{"", "-journal", "-wal", "-shm"} {
		if err := os.Remove(dbPath + suffix); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	db, err := index.Open(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	issues := demodata.Issues()
	if err := db.UpsertIssues(ctx, issues); err != nil {
		return err
	}
	docs := demodata.Documents()
	if err := db.UpsertDocuments(ctx, docs); err != nil {
		return err
	}

	fmt.Printf("seeded %d issues and %d documents into %s\n", len(issues), len(docs), dir)
	return nil
}
