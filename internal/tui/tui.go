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
	ReloadCmd    string // command for start/change:reload (e.g. "bkfz --list {q}")
}

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
	var args []string
	if opts.PreviewCmd != "" {
		args = append(args, "--preview", opts.PreviewCmd, "--preview-window", "right:60%")
	}
	if opts.ReloadCmd != "" {
		args = append(args,
			"--bind", "start:reload("+opts.ReloadCmd+")",
			"--bind", "change:reload("+opts.ReloadCmd+")",
		)
	}
	if opts.InitialQuery != "" {
		args = append(args, "--query", opts.InitialQuery)
	}
	return args
}
