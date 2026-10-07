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

func TestBuildFzfArgs_EmptyOptionsOnlySetsLayout(t *testing.T) {
	args := buildFzfArgs(RunOptions{})
	want := []string{"--delimiter", "\t", "--tabstop", "2"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("empty options: got %q, want %q", args, want)
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

func TestBuildFzfArgs_ReloadDisablesFzfFiltering(t *testing.T) {
	// bkfz matches descriptions / document bodies that are not part of
	// the visible line, so fzf must not re-filter reloaded results.
	args := buildFzfArgs(RunOptions{ReloadCmd: "bkfz --list {q}"})
	if !contains(args, "--disabled") {
		t.Errorf("expected --disabled when ReloadCmd is set, got: %v", args)
	}
	if contains(buildFzfArgs(RunOptions{}), "--disabled") {
		t.Error("--disabled should not be set without ReloadCmd")
	}
}

func TestBuildFzfArgs_KeyActions(t *testing.T) {
	args := buildFzfArgs(RunOptions{
		PreviewCmd: "bkfz preview {1} {2}",
		ReloadCmd:  "bkfz --list {q}",
		CopyCmd:    "bkfz --action copy {1} {2}",
		OpenCmd:    "bkfz --action open {1} {2}",
	})
	joined := strings.Join(args, "\x00")
	header := "ctrl-y: copy URL · ctrl-o: open (stay) · ctrl-/: preview"
	for _, want := range []string{
		"--bind\x00ctrl-y:transform-header(bkfz --action copy {1} {2})",
		"--bind\x00ctrl-o:transform-header(bkfz --action open {1} {2})",
		"--bind\x00ctrl-/:toggle-preview",
		"--bind\x00shift-up:preview-up",
		"--bind\x00shift-down:preview-down",
		"--header\x00" + header,
		// The status line from ctrl-y / ctrl-o is replaced by the key help
		// again on the next query change.
		"--bind\x00change:reload(bkfz --list {q})+change-header:" + header,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected args to contain %q, got args=%q", want, args)
		}
	}
}

func TestHeaderText_ListsOnlyConfiguredKeys(t *testing.T) {
	if got := headerText(RunOptions{ReloadCmd: "x"}); got != "" {
		t.Errorf("no actions → empty header, got %q", got)
	}
	if got, want := headerText(RunOptions{CopyCmd: "x"}), "ctrl-y: copy URL"; got != want {
		t.Errorf("headerText = %q, want %q", got, want)
	}
}

func TestBuildFzfArgs_NoHeaderKeepsPlainChangeBind(t *testing.T) {
	args := buildFzfArgs(RunOptions{ReloadCmd: "bkfz --list {q}"})
	if !contains(args, "change:reload(bkfz --list {q})") {
		t.Errorf("expected plain change bind, got: %q", args)
	}
	if contains(args, "--header") {
		t.Errorf("no --header expected without actions, got: %q", args)
	}
}

func contains(args []string, s string) bool {
	for _, a := range args {
		if a == s {
			return true
		}
	}
	return false
}
