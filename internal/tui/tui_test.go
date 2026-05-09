package tui

import (
	"strings"
	"testing"
)

func TestBuildFzfArgs_FullOptions(t *testing.T) {
	args := buildFzfArgs(RunOptions{
		InitialQuery: "auth",
		PreviewCmd:   "bkfz preview {1}",
		ReloadCmd:    "bkfz --list {q}",
	})
	joined := strings.Join(args, "\x00")

	for _, want := range []string{
		"--preview\x00bkfz preview {1}",
		"--preview-window\x00right:60%",
		"--bind\x00start:reload(bkfz --list {q})",
		"--bind\x00change:reload(bkfz --list {q})",
		"--query\x00auth",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected args to contain %q, got args=%v", want, args)
		}
	}
}

func TestBuildFzfArgs_EmptyOptionsProducesNoArgs(t *testing.T) {
	if args := buildFzfArgs(RunOptions{}); len(args) != 0 {
		t.Errorf("empty options should produce no args, got: %v", args)
	}
}

func TestBuildFzfArgs_OmitsFieldsThatAreEmpty(t *testing.T) {
	args := buildFzfArgs(RunOptions{
		ReloadCmd: "bkfz --list {q}",
		// PreviewCmd and InitialQuery left unset.
	})
	for _, a := range args {
		if a == "--preview" || a == "--query" {
			t.Errorf("%s should not appear when corresponding field is empty (args=%v)", a, args)
		}
	}
	// Only the reload bind should remain.
	joined := strings.Join(args, "\x00")
	if !strings.Contains(joined, "start:reload(bkfz --list {q})") {
		t.Errorf("expected reload bind in args, got: %v", args)
	}
}
