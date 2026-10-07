package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// RunOptions configures how fzf is launched.
type RunOptions struct {
	InitialQuery string // initial query at start-up
	PreviewCmd   string // command for fzf --preview (e.g. "bkfz preview {1}")
	// ReloadCmd is the command for start/change:reload (e.g. "bkfz --list {q}").
	// When set, fzf's own filtering is disabled (--disabled) so the list is
	// exactly what the command returns: bkfz matches on fields that are not
	// part of the visible line (descriptions, document bodies), and fzf
	// re-filtering would drop those hits.
	ReloadCmd string
	// CopyCmd / OpenCmd are bound to ctrl-y / ctrl-o via transform-header:
	// the command runs without leaving fzf and its stdout (a one-line
	// status such as "✓ Copied: <url>") replaces the header until the
	// query changes.
	CopyCmd string
	OpenCmd string
}

// MinFzfVersion is the oldest fzf that supports every action used here
// (transform-header / change-header were added in 0.40.0).
const MinFzfVersion = "0.40.0"

// ErrFzfNotFound is the sentinel error Run returns when the fzf binary is
// not on PATH. Callers (e.g. the cmd layer) match it via errors.Is to
// turn it into a context-aware message (install hints, etc.).
var ErrFzfNotFound = errors.New("fzf not found in PATH")

// Run launches fzf and returns the line the user selected.
// Cancellation (Ctrl-C / Esc) or no-match (exit codes 1, 130) returns ("", nil).
// Returns ErrFzfNotFound when fzf is missing from PATH.
func Run(ctx context.Context, opts RunOptions) (string, error) {
	fzfPath, err := exec.LookPath("fzf")
	if err != nil {
		return "", ErrFzfNotFound
	}

	cmd := exec.CommandContext(ctx, fzfPath, buildFzfArgs(opts)...)
	// start:reload populates the initial list, so stdin is empty. fzf itself
	// drives keyboard input and TUI rendering through /dev/tty, so it's fine
	// to redirect stdin/stdout.
	cmd.Stdin = bytes.NewReader(nil)
	cmd.Stderr = os.Stderr

	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	err = cmd.Run()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			switch ee.ExitCode() {
			case 1, 130: // 1 = no match, 130 = Ctrl-C → both treated as cancel
				return "", nil
			}
		}
		return "", fmt.Errorf("fzf: %w", err)
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

// buildFzfArgs assembles fzf command-line arguments from RunOptions.
// Empty fields are skipped. Split out from exec for testability.
func buildFzfArgs(opts RunOptions) []string {
	// List lines are tab-separated (type\tKEY\t...). Splitting on tabs
	// keeps {1}/{2} stable even when a summary contains spaces; a small
	// tabstop keeps the columns compact next to the preview.
	args := []string{"--delimiter", "\t", "--tabstop", "2"}
	if opts.PreviewCmd != "" {
		args = append(args,
			"--preview", opts.PreviewCmd,
			"--preview-window", "right:60%",
			"--bind", "ctrl-/:toggle-preview",
			"--bind", "shift-up:preview-up",
			"--bind", "shift-down:preview-down",
		)
	}
	header := headerText(opts)
	if header != "" {
		args = append(args, "--header", header)
	}
	if opts.ReloadCmd != "" {
		change := "change:reload(" + opts.ReloadCmd + ")"
		if header != "" {
			// Restore the key help after a ctrl-y / ctrl-o status message.
			// The colon form must come last; it takes the rest of the string.
			change += "+change-header:" + header
		}
		args = append(args,
			"--disabled",
			"--bind", "start:reload("+opts.ReloadCmd+")",
			"--bind", change,
		)
	}
	if opts.CopyCmd != "" {
		args = append(args, "--bind", "ctrl-y:transform-header("+opts.CopyCmd+")")
	}
	if opts.OpenCmd != "" {
		args = append(args, "--bind", "ctrl-o:transform-header("+opts.OpenCmd+")")
	}
	if opts.InitialQuery != "" {
		args = append(args, "--query", opts.InitialQuery)
	}
	return args
}

// headerText returns the key help shown next to the prompt, listing only
// the keys whose actions are configured (Enter = open is implied). Kept
// short because it shares the 40% list column with the results.
func headerText(opts RunOptions) string {
	var keys []string
	if opts.CopyCmd != "" {
		keys = append(keys, "ctrl-y: copy URL")
	}
	if opts.OpenCmd != "" {
		keys = append(keys, "ctrl-o: open (stay)")
	}
	if opts.PreviewCmd != "" {
		keys = append(keys, "ctrl-/: preview")
	}
	if len(keys) == 0 {
		return ""
	}
	return strings.Join(keys, " · ")
}
