package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/hidetzu/backlog-fzf/internal/backlog"
	"github.com/hidetzu/backlog-fzf/internal/config"
	"github.com/hidetzu/backlog-fzf/internal/index"
	"github.com/hidetzu/backlog-fzf/internal/syncer"
	"github.com/hidetzu/backlog-fzf/internal/tui"
)

// Kind labels: shared identifier used as the first column of list output
// and as the type argument for preview / open commands.
const (
	kindIssue    = "issue"
	kindDocument = "doc"
)

const usage = `bkfz - cross-project fuzzy search for Backlog

Usage:
  bkfz                       Interactive fuzzy search (default)
  bkfz <query>               Non-interactive search across issues and documents
  bkfz init                  Create config scaffold and register API key
  bkfz sync [flags]          Sync issues + documents from Backlog into local index
  bkfz open <KEY>            Open issue in browser
  bkfz open <type> <KEY>     Open issue|doc explicitly (type = issue | doc)
  bkfz preview <KEY>         Print preview text for an issue
  bkfz preview <type> <KEY>  Print preview text (used by fzf --preview)
  bkfz --list <query>        Emit list lines for fzf change:reload

Sync flags:
  --refetch                  Re-fetch everything, ignoring watermarks (stale local records remain)
  -p PROJ                    Limit to project key

Environment:
  BACKLOG_API_KEY            Backlog personal API key (required for sync)
`

func main() {
	// SIGINT (Ctrl-C) / SIGTERM cancels ctx so an in-flight sync (etc.)
	// shuts down gracefully. Each layer (time.After / API calls / SQL
	// execution) honors ctx, so the cancel propagates and returns promptly.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:]); err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "bkfz: interrupted")
			os.Exit(130) // convention: 128 + SIGINT(2)
		}
		fmt.Fprintln(os.Stderr, "bkfz:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return runTUI(ctx)
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Print(usage)
		return nil
	case "init":
		return runInit(ctx, args[1:])
	case "sync":
		return runSync(ctx, args[1:])
	case "open":
		return runOpen(ctx, args[1:])
	case "preview":
		return runPreview(ctx, args[1:])
	case "--list":
		return runList(ctx, args[1:])
	default:
		return runSearch(ctx, args) // `bkfz <query>`
	}
}

// runTUI launches fzf for interactive search.
// The KEY is extracted from the line fzf returns and opened in a browser.
//
// We load the config and fail-fast before launching fzf: erroring out with
// "no config found" after the user already picked something would be a
// terrible UX.
func runTUI(ctx context.Context) error {
	cfg, err := loadConfigOrInitHint()
	if err != nil {
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		exe = "bkfz" // on failure, fall back to "bkfz" resolved via PATH
	}
	q := shellQuote(exe)

	selected, err := tui.Run(ctx, tui.RunOptions{
		ReloadCmd:  fmt.Sprintf("%s --list {q}", q),
		PreviewCmd: fmt.Sprintf("%s preview {1} {2}", q),
	})
	if err != nil {
		if errors.Is(err, tui.ErrFzfNotFound) {
			return fzfMissingError()
		}
		return err
	}
	if selected == "" {
		return nil // cancelled / no match
	}

	kind, key, ok := parseSelectedRef(selected)
	if !ok {
		return fmt.Errorf("could not parse type/key from selected line: %q", selected)
	}
	return openRefURL(ctx, cfg.SpaceDomain, kind, key)
}

// parseSelectedRef extracts (kind, key, ok) from a formatIssueLine /
// formatDocumentLine output (type\tKEY\t...). Returns ok=false when
// neither type nor key can be obtained.
func parseSelectedRef(line string) (kind, key string, ok bool) {
	parts := strings.SplitN(line, "\t", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// fzfMissingError returns a friendly error for when fzf is not installed.
// The message lists install instructions and the non-interactive fallback.
func fzfMissingError() error {
	return fmt.Errorf(`fzf not found in PATH.

Install fzf:
  macOS:    brew install fzf
  Linux:    apt install fzf  (or your distribution's package manager)
  Windows:  winget install junegunn.fzf
  Other:    https://github.com/junegunn/fzf

Without fzf, you can still search non-interactively:
  bkfz <query>`)
}

// shellQuote wraps a string for safe inclusion in fzf --bind command
// expressions. fzf evaluates these through the OS shell (sh on Unix,
// cmd.exe on Windows by default), so the quoting style is OS-dependent.
//
//	sh:      '...'  with embedded ' as '\''
//	cmd.exe: "..."  with embedded " as ""
func shellQuote(s string) string {
	if runtime.GOOS == "windows" {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// runInit interactively prompts for SpaceDomain and Projects and saves the config.
func runInit(_ context.Context, _ []string) error {
	existing, err := config.Load()
	if err != nil && !errors.Is(err, config.ErrNotFound) {
		return err
	}
	if existing != nil {
		ok, err := confirmYesNo(os.Stdin, os.Stdout,
			fmt.Sprintf("Config already exists (space=%q). Overwrite?", existing.SpaceDomain), false)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("aborted")
		}
	}

	domain, err := promptLine(os.Stdin, os.Stdout, "Backlog space domain (e.g. myteam.backlog.com): ")
	if err != nil {
		return err
	}
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return errors.New("space domain is required")
	}

	projectsLine, err := promptLine(os.Stdin, os.Stdout, "Project keys (comma-separated, empty for all): ")
	if err != nil {
		return err
	}
	var projects []string
	for _, p := range strings.Split(projectsLine, ",") {
		if p = strings.TrimSpace(p); p != "" {
			projects = append(projects, p)
		}
	}

	c := &config.Config{SpaceDomain: domain, Projects: projects}
	if err := config.Save(c); err != nil {
		return err
	}

	path, _ := config.Path()
	fmt.Fprintf(os.Stdout, "Config saved to %s\n", path)
	fmt.Fprintln(os.Stdout, "Set BACKLOG_API_KEY env var, then run: bkfz sync")
	return nil
}

// runSync loads the config and drives syncer.Issues / syncer.Documents.
func runSync(ctx context.Context, args []string) error {
	// ContinueOnError + SetOutput(io.Discard) avoid os.Exit so all errors
	// flow through main()'s single "bkfz: ..." error path.
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	refetch := fs.Bool("refetch", false, "Re-fetch all issues, ignoring watermark (stale local records remain)")
	project := fs.String("p", "", "Limit to project key")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("sync: %w", err)
	}

	cfg, err := loadConfigOrInitHint()
	if err != nil {
		return err
	}

	client, err := backlog.New(cfg.SpaceDomain)
	if err != nil {
		return err
	}

	db, err := openIndexDB()
	if err != nil {
		return err
	}
	defer db.Close()

	projectKeys := cfg.Projects
	if *project != "" {
		projectKeys = []string{*project}
	}

	pp := &progressPrinter{out: os.Stderr, isTTY: isTerminal(os.Stderr)}
	defer pp.Close() // close any open fetched line, even on error

	// Emit rate-limit notices in a way consistent with the progress display:
	// - OnThrottle: proactive throttle (when X-RateLimit-Remaining=0 is observed)
	// - OnRetry:    safety net for actual 429 responses (cases the proactive path could not prevent)
	client.OnThrottle = func(_ int, wait time.Duration) {
		pp.Notice(fmt.Sprintf("  rate limit window exhausted, waiting %s for reset...", wait.Round(time.Second)))
	}
	client.OnRetry = func(attempt int, wait time.Duration, _ error) {
		pp.Notice(fmt.Sprintf("  rate limited, waiting %s before retry %d/%d...", wait, attempt, maxClientRetries))
	}

	syncOpts := syncer.Options{
		Refetch:     *refetch,
		ProjectKeys: projectKeys,
		OnProgress:  pp.Print,
	}

	pp.SetKind("issues")
	if err := syncer.Issues(ctx, client, db, syncOpts); err != nil {
		return err
	}

	pp.SetKind("documents")
	if err := syncer.Documents(ctx, client, db, syncOpts); err != nil {
		return err
	}
	return nil
}

// maxClientRetries is the total retry count used for display (matches backlog.maxRetries).
const maxClientRetries = 5

// progressPrinter formats syncer.OnProgress events for stderr.
// On a TTY, "[fetched: N]" updates the same line via \r (monotonically
// increasing N, so no leftover characters).
// On a non-TTY (pipe / redirect), each event is printed on its own line.
//
// kind can be switched with SetKind. `bkfz sync` flips between "issues"
// and "documents" while streaming progress. With kind == "" it falls back
// to a labelless format.
type progressPrinter struct {
	out           io.Writer
	isTTY         bool
	kind          string    // e.g. "issues" / "documents"; empty = no label
	total         int       // denominator received via StageTotal; 0 = no "/N" display
	started       time.Time // set on StageFetching for ETA calculation; reset by SetKind
	fetchedActive bool      // whether a [fetched] line is currently open in TTY mode
}

// SetKind sets the kind label for the next section.
// Any open "[fetched]" line is closed, and total / started are reset
// (each section has independent progress — its own denominator and elapsed).
func (p *progressPrinter) SetKind(kind string) {
	p.endFetched()
	p.kind = kind
	p.total = 0
	p.started = time.Time{}
}

func (p *progressPrinter) Print(stage syncer.ProgressStage, n int) {
	switch stage {
	case syncer.StageTotal:
		p.total = n
	case syncer.StageFetching:
		p.started = time.Now()
		if p.kind != "" {
			fmt.Fprintf(p.out, "syncing %s...\n", p.kind)
		} else {
			fmt.Fprintln(p.out, "fetching...")
		}
	case syncer.StageFetched:
		var elapsed time.Duration
		if !p.started.IsZero() {
			elapsed = time.Since(p.started)
		}
		line := p.formatFetched(n, elapsed)
		if p.isTTY {
			fmt.Fprint(p.out, "\r"+line)
			p.fetchedActive = true
		} else {
			fmt.Fprintln(p.out, line)
		}
	case syncer.StageFiltered:
		p.endFetched()
		fmt.Fprintf(p.out, "filtered to %d new\n", n)
	case syncer.StageUpserting:
		fmt.Fprintf(p.out, "upserting %d...\n", n)
	case syncer.StageDone:
		p.endFetched() // safety net for paths that ended after StageFetched without StageFiltered
		if p.kind != "" {
			fmt.Fprintf(p.out, "%s sync ok (%d upserted)\n", p.kind, n)
		} else {
			fmt.Fprintf(p.out, "sync ok (%d upserted)\n", n)
		}
	}
}

// formatFetched returns "[fetched: N]" or a richer form including total / ETA.
// ETA is only attached when elapsed is large enough (>= etaWarmup) and total is known.
func (p *progressPrinter) formatFetched(n int, elapsed time.Duration) string {
	if p.total <= 0 {
		return fmt.Sprintf("  [fetched: %d]", n)
	}
	if eta, ok := computeETA(n, p.total, elapsed); ok {
		return fmt.Sprintf("  [fetched: %d/%d ETA %s]", n, p.total, eta)
	}
	return fmt.Sprintf("  [fetched: %d/%d]", n, p.total)
}

// etaWarmup is the minimum elapsed time before ETA is shown.
// During the early period the rate is still unstable and predictions
// would jitter; the warmup suppresses that.
const etaWarmup = 5 * time.Second

// computeETA returns the string representation of the remaining time
// derived from cumulative n / total / elapsed. Returns ok=false (caller
// skips ETA display) when the inputs are unsuitable:
//   - total <= 0           (denominator unknown)
//   - n <= 0 or n >= total (boundary)
//   - elapsed < etaWarmup  (too early; rate still unstable)
//   - rate <= 0            (numerically degenerate)
func computeETA(n, total int, elapsed time.Duration) (string, bool) {
	if total <= 0 || n <= 0 || n >= total {
		return "", false
	}
	if elapsed < etaWarmup {
		return "", false
	}
	rate := float64(n) / elapsed.Seconds()
	if rate <= 0 {
		return "", false
	}
	remaining := time.Duration(float64(total-n)/rate*float64(time.Second)) * 1
	return formatDuration(remaining), true
}

// formatDuration formats durations as "Xs" under 1 minute, otherwise "XmYs"
// (we don't use the hour unit; durations are expected to fall within tens of minutes).
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	totalSec := int(d.Seconds())
	return fmt.Sprintf("%dm%02ds", totalSec/60, totalSec%60)
}

// endFetched finalizes an open "[fetched]" line with a newline (TTY mode).
func (p *progressPrinter) endFetched() {
	if p.fetchedActive {
		fmt.Fprintln(p.out)
		p.fetchedActive = false
	}
}

// Close finalizes any open fetched line on shutdown. Intended to be used
// with defer pp.Close(). This prevents progress output from concatenating
// with later writes (e.g. "[fetched: N]bkfz: error...") on early return.
func (p *progressPrinter) Close() {
	p.endFetched()
}

// Notice closes any in-flight progress line and prints msg on its own line.
// Used for rate-limit retry notifications and similar out-of-band messages.
func (p *progressPrinter) Notice(msg string) {
	p.endFetched()
	fmt.Fprintln(p.out, msg)
}

// isTerminal reports whether f is a terminal (CharDevice).
// Returns false for pipes / file redirects, used to decide whether to
// suppress the same-line progress updates.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// runSearch and runList share the same implementation, differing only in limit.
func runSearch(ctx context.Context, args []string) error {
	return search(ctx, strings.Join(args, " "), 50)
}

func runList(ctx context.Context, args []string) error {
	return search(ctx, strings.Join(args, " "), 100)
}

// search collects matches from both issues and documents, merges them in
// updated_at DESC order, and prints up to `limit` rows on stdout.
// Cross-type filtering from a single query is backlog-fzf's core concept.
func search(ctx context.Context, query string, limit int) error {
	db, err := openIndexDB()
	if err != nil {
		return err
	}
	defer db.Close()

	issues, err := db.SearchIssues(ctx, query, limit)
	if err != nil {
		return err
	}
	docs, err := db.SearchDocuments(ctx, query, limit)
	if err != nil {
		return err
	}

	type entry struct {
		line string
		ts   time.Time
	}
	entries := make([]entry, 0, len(issues)+len(docs))
	for _, i := range issues {
		entries = append(entries, entry{formatIssueLine(i), i.UpdatedAt})
	}
	for _, d := range docs {
		entries = append(entries, entry{formatDocumentLine(d), d.UpdatedAt})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].ts.After(entries[j].ts)
	})
	if len(entries) > limit {
		entries = entries[:limit]
	}
	for _, e := range entries {
		fmt.Println(e.line)
	}
	return nil
}

// runOpen builds a URL from cfg.SpaceDomain and opens it in the browser.
//
// Argument forms:
//
//	bkfz open <KEY>             KEY is treated as an issue (manual CLI use)
//	bkfz open issue <KEY>       explicit
//	bkfz open doc <DOC_ID>      same form used when invoked from fzf
func runOpen(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return errors.New("open: KEY required")
	}
	cfg, err := loadConfigOrInitHint()
	if err != nil {
		return err
	}
	kind, key := parseTypeKeyArgs(args)
	return openRefURL(ctx, cfg.SpaceDomain, kind, key)
}

// parseTypeKeyArgs accepts either <TYPE> <KEY> or just <KEY> (where the
// omitted type defaults to "issue").
func parseTypeKeyArgs(args []string) (kind, key string) {
	if len(args) >= 2 && (args[0] == kindIssue || args[0] == kindDocument) {
		return args[0], args[1]
	}
	return kindIssue, args[0]
}

// openRefURL picks a URL pattern based on kind and opens it in the browser.
//
//	issue: https://{space}/view/{KEY}
//	doc:   https://{space}/document/{PROJECT_KEY}/{DOC_ID}
//
// For docs, project_key is looked up in the index DB (it is intentionally
// not embedded in the list line).
func openRefURL(ctx context.Context, spaceDomain, kind, key string) error {
	var url string
	switch kind {
	case kindIssue:
		url = fmt.Sprintf("https://%s/view/%s", spaceDomain, key)
	case kindDocument:
		db, err := openIndexDB()
		if err != nil {
			return err
		}
		defer db.Close()
		doc, err := db.GetDocument(ctx, key)
		if err != nil {
			return fmt.Errorf("open doc %s: %w", key, err)
		}
		url = fmt.Sprintf("https://%s/document/%s/%s", spaceDomain, doc.ProjectKey, doc.ID)
	default:
		return fmt.Errorf("unknown kind: %q", kind)
	}
	name, args := browserCommand(runtime.GOOS, url)
	if name == "" {
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	return exec.CommandContext(ctx, name, args...).Run()
}

// loadConfigOrInitHint loads the config and, when it is missing,
// returns a friendly error directing the user to run `bkfz init`.
func loadConfigOrInitHint() (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		if errors.Is(err, config.ErrNotFound) {
			return nil, fmt.Errorf("no config found; run `bkfz init` first")
		}
		return nil, err
	}
	return cfg, nil
}

// runPreview prints the preview text for one record on stdout (used by fzf --preview).
//
// Argument forms:
//
//	bkfz preview <KEY>             KEY is treated as an issue (backward compatible)
//	bkfz preview issue <KEY>
//	bkfz preview doc <DOC_ID>
func runPreview(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return errors.New("preview: KEY required")
	}
	kind, key := parseTypeKeyArgs(args)

	db, err := openIndexDB()
	if err != nil {
		return err
	}
	defer db.Close()

	cfg, _ := config.Load() // only used for the URL line; preview still works without it

	switch kind {
	case kindIssue:
		return previewIssue(ctx, db, cfg, key)
	case kindDocument:
		return previewDocument(ctx, db, cfg, key)
	default:
		return fmt.Errorf("unknown kind: %q", kind)
	}
}

func previewIssue(ctx context.Context, db *index.DB, cfg *config.Config, key string) error {
	issue, err := db.GetIssue(ctx, key)
	if err != nil {
		return err
	}
	fmt.Printf("# %s — %s\n\n", issue.Key, issue.Summary)
	if issue.Status != "" {
		fmt.Printf("Status:   %s\n", issue.Status)
	}
	if issue.Assignee != "" {
		fmt.Printf("Assignee: %s\n", issue.Assignee)
	}
	if issue.DueDate != nil {
		fmt.Printf("Due:      %s\n", issue.DueDate.Format("2006-01-02"))
	}
	if cfg != nil {
		fmt.Printf("URL:      https://%s/view/%s\n", cfg.SpaceDomain, key)
	}
	if issue.Description != "" {
		fmt.Println()
		fmt.Println(issue.Description)
	}
	return nil
}

func previewDocument(ctx context.Context, db *index.DB, cfg *config.Config, id string) error {
	doc, err := db.GetDocument(ctx, id)
	if err != nil {
		return err
	}
	fmt.Printf("# %s\n\n", doc.Title)
	fmt.Printf("Project:  %s\n", doc.ProjectKey)
	if cfg != nil {
		fmt.Printf("URL:      https://%s/document/%s/%s\n", cfg.SpaceDomain, doc.ProjectKey, doc.ID)
	}
	if doc.Body != "" {
		fmt.Println()
		fmt.Println(doc.Body)
	}
	return nil
}

// --- helpers ---

// openIndexDB resolves DataPath and opens the index DB, creating the parent directory as needed.
func openIndexDB() (*index.DB, error) {
	path, err := config.DataPath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return index.Open(path)
}

// formatIssueLine returns the formatted line for one issue.
// The type prefix and KEY come first, so fzf can pick them up as {1}=type, {2}=key.
//
//	issue\tPROJ-123\t[Open]\talice\tfix auth bug\t2026-06-30
func formatIssueLine(i backlog.Issue) string {
	due := "-"
	if i.DueDate != nil {
		due = i.DueDate.Format("2006-01-02")
	}
	status := i.Status
	if status == "" {
		status = "-"
	}
	assignee := i.Assignee
	if assignee == "" {
		assignee = "-"
	}
	return fmt.Sprintf("%s\t%s\t[%s]\t%s\t%s\t%s", kindIssue, i.Key, status, assignee, i.Summary, due)
}

// formatDocumentLine returns the formatted line for one document.
// Documents have no status / assignee / due, so those columns are filled
// with "-" to keep the column count consistent.
//
//	doc\tDOC_ID\t-\t-\tDocument Title\t2026-04-15
func formatDocumentLine(d backlog.Document) string {
	updated := "-"
	if !d.UpdatedAt.IsZero() {
		updated = d.UpdatedAt.Format("2006-01-02")
	}
	return fmt.Sprintf("%s\t%s\t-\t-\t%s\t%s", kindDocument, d.ID, d.Title, updated)
}

// browserCommand returns (name, args) for a "open URL" command per GOOS.
// Returns name="" for unsupported GOOS values. runtime.GOOS is passed in
// as a parameter so the function is easy to test.
func browserCommand(goos, url string) (string, []string) {
	switch goos {
	case "darwin":
		return "open", []string{url}
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}
	case "linux", "freebsd", "openbsd", "netbsd":
		return "xdg-open", []string{url}
	default:
		return "", nil
	}
}

// promptLine writes prompt to w and reads a single line from r (trailing newline stripped).
func promptLine(r io.Reader, w io.Writer, prompt string) (string, error) {
	fmt.Fprint(w, prompt)
	sc := bufio.NewScanner(r)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return "", err
		}
		return "", io.EOF
	}
	return sc.Text(), nil
}

// confirmYesNo returns a bool from a y/N prompt. With defaultYes=false, plain Enter means no.
func confirmYesNo(r io.Reader, w io.Writer, prompt string, defaultYes bool) (bool, error) {
	suffix := " [y/N]: "
	if defaultYes {
		suffix = " [Y/n]: "
	}
	line, err := promptLine(r, w, prompt+suffix)
	if err != nil {
		return false, err
	}
	line = strings.ToLower(strings.TrimSpace(line))
	if line == "" {
		return defaultYes, nil
	}
	return line == "y" || line == "yes", nil
}
