package main

import (
	"strings"
	"testing"

	"github.com/hidetzu/backlog-fzf/internal/syncer"
)

func TestProgressPrinter_EmbeddedLabel(t *testing.T) {
	var buf strings.Builder
	p := &progressPrinter{out: &buf}
	p.SetKind("embeddings")
	p.Print(syncer.StageTotal, 10)
	p.Print(syncer.StageEmbedded, 4)
	p.Print(syncer.StageDone, 10)
	out := buf.String()
	if !strings.Contains(out, "[embedded: 4/10]") {
		t.Errorf("expected embedded progress, got %q", out)
	}
	if !strings.Contains(out, "embeddings ok (10 embedded)") {
		t.Errorf("expected embed summary, got %q", out)
	}
	if strings.Contains(out, "upserted") {
		t.Errorf("embed stage must not say upserted: %q", out)
	}
}
