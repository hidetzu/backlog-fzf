package tui

import (
	"strings"
	"testing"
)

func TestBuildFzfArgs_SemanticOneShotBindings(t *testing.T) {
	args := buildFzfArgs(RunOptions{
		ReloadCmd:   "bkfz --list {q}",
		CopyCmd:     "bkfz --action copy {1} {2}",
		SemanticCmd: "bkfz --list --semantic {q}",
	})
	joined := strings.Join(args, "\x00")
	header := "ctrl-y: copy URL · ctrl-s: semantic"
	for _, want := range []string{
		// change-header: (colon form) must stay last in every chain.
		"--bind\x00change:reload(bkfz --list {q})+change-prompt(> )+change-header:" + header,
		"--bind\x00ctrl-s:reload(bkfz --list --semantic {q})+change-prompt(semantic> )+change-header:" + header,
		"--bind\x00alt-s:reload(bkfz --list --semantic {q})+change-prompt(semantic> )+change-header:" + header,
		"--header\x00" + header,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected args to contain %q, got args=%v", want, args)
		}
	}
}

func TestBuildFzfArgs_SemanticHintWhenNotConfigured(t *testing.T) {
	args := buildFzfArgs(RunOptions{
		ReloadCmd:    "bkfz --list {q}",
		SemanticHint: "semantic search is not configured",
	})
	joined := strings.Join(args, "\x00")
	for _, want := range []string{
		"--bind\x00ctrl-s:change-header(semantic search is not configured)",
		"--bind\x00alt-s:change-header(semantic search is not configured)",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected %q in %v", want, args)
		}
	}
	// Without SemanticCmd the change binding is unchanged (no prompt reset).
	if strings.Contains(joined, "change-prompt") {
		t.Errorf("change-prompt should not appear without SemanticCmd: %v", args)
	}
	if strings.Contains(strings.Join(args, " "), "ctrl-s: semantic") {
		t.Error("header must not advertise ctrl-s when semantic search is off")
	}
}

func TestBuildFzfArgs_NoSemanticBindingsByDefault(t *testing.T) {
	args := buildFzfArgs(RunOptions{ReloadCmd: "bkfz --list {q}"})
	for _, a := range args {
		if strings.HasPrefix(a, "ctrl-s:") || strings.HasPrefix(a, "alt-s:") {
			t.Errorf("unexpected binding %q", a)
		}
	}
}
