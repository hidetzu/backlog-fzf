package main

import (
	"bytes"
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hidetzu/backlog-fzf/internal/backlog"
	"github.com/hidetzu/backlog-fzf/internal/syncer"
)

func TestFormatIssueLine_TypePrefixedTabSeparated(t *testing.T) {
	due := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	i := backlog.Issue{
		Key:      "PROJ-123",
		Summary:  "認証バグ修正",
		Status:   "Open",
		Assignee: "alice",
		DueDate:  &due,
	}
	got := formatIssueLine(i)
	want := "issue\tPROJ-123\t[Open]\talice\t認証バグ修正\t2026-06-30"
	if got != want {
		t.Errorf("formatIssueLine = %q\nwant %q", got, want)
	}
}

func TestFormatIssueLine_NilOptionalFields(t *testing.T) {
	i := backlog.Issue{
		Key:     "P-1",
		Summary: "no fluff",
	}
	got := formatIssueLine(i)
	want := "issue\tP-1\t[-]\t-\tno fluff\t-"
	if got != want {
		t.Errorf("formatIssueLine = %q\nwant %q", got, want)
	}
}

func TestFormatDocumentLine_TypePrefixedTabSeparated(t *testing.T) {
	d := backlog.Document{
		ID:         "abc123",
		ProjectKey: "PROJ",
		Title:      "ユーザーガイド",
		UpdatedAt:  time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC),
	}
	got := formatDocumentLine(d)
	want := "doc\tabc123\t-\t-\tユーザーガイド\t2026-04-15"
	if got != want {
		t.Errorf("formatDocumentLine = %q\nwant %q", got, want)
	}
}

func TestBrowserCommand_PerGOOS(t *testing.T) {
	cases := []struct {
		goos     string
		wantName string
		wantArg0 string
	}{
		{"darwin", "open", "https://example/view/P-1"},
		{"linux", "xdg-open", "https://example/view/P-1"},
		{"freebsd", "xdg-open", "https://example/view/P-1"},
		{"windows", "rundll32", "url.dll,FileProtocolHandler"},
		{"plan9", "", ""}, // unsupported
	}
	for _, tc := range cases {
		t.Run(tc.goos, func(t *testing.T) {
			name, args := browserCommand(tc.goos, "https://example/view/P-1")
			if name != tc.wantName {
				t.Errorf("name = %q, want %q", name, tc.wantName)
			}
			if tc.wantName == "" {
				return
			}
			if len(args) == 0 || args[0] != tc.wantArg0 {
				t.Errorf("args[0] = %v, want %q", args, tc.wantArg0)
			}
		})
	}
}

func TestPromptLine_ReadsSingleLineWithoutNewline(t *testing.T) {
	in := strings.NewReader("hello world\nignored\n")
	var out bytes.Buffer
	got, err := promptLine(in, &out, "> ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
	if !strings.Contains(out.String(), "> ") {
		t.Errorf("prompt not written to writer; got %q", out.String())
	}
}

// An unknown flag must return an error rather than os.Exit (regression guard for ContinueOnError).
func TestRunSync_UnknownFlagReturnsErrorWithoutExit(t *testing.T) {
	err := run(context.Background(), []string{"sync", "-bogus"})
	if err == nil {
		t.Fatal("expected error for unknown flag, got nil")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error should mention the bad flag; got %v", err)
	}
}

// Verify the fail-fast path: we check for config before launching fzf.
// Pointing XDG_CONFIG_HOME at an empty tmp puts config.Load into the
// ErrNotFound state, and we expect to bail out before reaching tui.Run
// (which would spawn the fzf process).
func TestFzfMissingError_HasInstallHintAndFallback(t *testing.T) {
	msg := fzfMissingError().Error()
	for _, want := range []string{
		"fzf not found",
		"brew install fzf",            // macOS
		"apt install fzf",             // Linux
		"winget install junegunn.fzf", // Windows
		"github.com/junegunn/fzf",     // Other / fallback URL
		"bkfz <query>",                // non-interactive fallback
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message should contain %q, got: %s", want, msg)
		}
	}
}

func TestRunTUI_FailsFastWithoutConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	err := runTUI(context.Background())
	if err == nil {
		t.Fatal("expected error when config is missing, got nil")
	}
	if !strings.Contains(err.Error(), "bkfz init") {
		t.Errorf("error should hint to run `bkfz init`, got: %v", err)
	}
}

func TestRunOpen_FailsFastWithoutConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	err := runOpen(context.Background(), []string{"P-1"})
	if err == nil {
		t.Fatal("expected error when config is missing, got nil")
	}
	if !strings.Contains(err.Error(), "bkfz init") {
		t.Errorf("error should hint to run `bkfz init`, got: %v", err)
	}
}

func TestParseSelectedRef(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantKind string
		wantKey  string
		wantOK   bool
	}{
		{"issue line", "issue\tPROJ-123\t[Open]\talice\tsummary\t-", "issue", "PROJ-123", true},
		{"doc line", "doc\tabc123\t-\t-\ttitle\t2026-04-15", "doc", "abc123", true},
		{"single tab boundary", "issue\tPROJ-1", "issue", "PROJ-1", true},
		{"no tab", "STANDALONE", "", "", false},
		{"empty", "", "", "", false},
		{"empty type", "\tPROJ-1\trest", "", "", false},
		{"empty key", "issue\t\trest", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, key, ok := parseSelectedRef(tc.in)
			if ok != tc.wantOK || kind != tc.wantKind || key != tc.wantKey {
				t.Errorf("parseSelectedRef(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.in, kind, key, ok, tc.wantKind, tc.wantKey, tc.wantOK)
			}
		})
	}
}

func TestParseTypeKeyArgs(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantKind string
		wantKey  string
	}{
		{"single arg defaults to issue", []string{"PROJ-123"}, "issue", "PROJ-123"},
		{"explicit issue", []string{"issue", "PROJ-123"}, "issue", "PROJ-123"},
		{"explicit doc", []string{"doc", "abc123"}, "doc", "abc123"},
		{"unknown first arg treated as KEY", []string{"foo", "bar"}, "issue", "foo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, key := parseTypeKeyArgs(tc.args)
			if kind != tc.wantKind || key != tc.wantKey {
				t.Errorf("parseTypeKeyArgs(%v) = (%q, %q), want (%q, %q)",
					tc.args, kind, key, tc.wantKind, tc.wantKey)
			}
		})
	}
}

func TestShellQuote(t *testing.T) {
	type tc struct{ in, want string }
	var cases []tc
	if runtime.GOOS == "windows" {
		cases = []tc{
			{"foo", `"foo"`},
			{"with space", `"with space"`},
			{`C:\Users\me\go\bin\bkfz.exe`, `"C:\Users\me\go\bin\bkfz.exe"`},
			{`a"b`, `"a""b"`},
		}
	} else {
		cases = []tc{
			{"foo", "'foo'"},
			{"with space", "'with space'"},
			{"/usr/local/bin/bkfz", "'/usr/local/bin/bkfz'"},
			{"it's", `'it'\''s'`},
		}
	}
	for _, c := range cases {
		if got := shellQuote(c.in); got != c.want {
			t.Errorf("shellQuote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestComputeETA(t *testing.T) {
	cases := []struct {
		name    string
		n       int
		total   int
		elapsed time.Duration
		wantOK  bool
	}{
		{"too early (under warmup)", 100, 16000, 4 * time.Second, false},
		{"steady rate, halfway", 800, 1600, 10 * time.Second, true},
		{"slow rate, big remaining", 100, 16000, 30 * time.Second, true},
		{"n >= total (done)", 1000, 1000, 60 * time.Second, false},
		{"total unknown", 100, 0, 30 * time.Second, false},
		{"n is zero", 0, 1000, 30 * time.Second, false},
		{"normal pace", 1000, 16000, 60 * time.Second, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := computeETA(tc.n, tc.total, tc.elapsed)
			if ok != tc.wantOK {
				t.Errorf("ok = %v, want %v (got value %q)", ok, tc.wantOK, got)
			}
			if tc.wantOK && got == "" {
				t.Errorf("expected non-empty ETA")
			}
		})
	}
}

func TestComputeETA_HalfwayValue(t *testing.T) {
	// 800 of 1600 fetched / 10s elapsed → rate=80/s, remaining 800/80=10s.
	got, ok := computeETA(800, 1600, 10*time.Second)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got != "10s" {
		t.Errorf("got %q, want 10s", got)
	}
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{30 * time.Second, "30s"},
		{59 * time.Second, "59s"},
		{60 * time.Second, "1m00s"},
		{90 * time.Second, "1m30s"},
		{14*time.Minute + 32*time.Second, "14m32s"},
		{2 * time.Hour, "120m00s"},
	}
	for _, tc := range cases {
		if got := formatDuration(tc.d); got != tc.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

// formatFetched picks one of three forms (no denominator / denominator only / denominator + ETA) based on elapsed and total.
func TestProgressPrinter_FormatFetchedVariants(t *testing.T) {
	cases := []struct {
		name       string
		n          int
		total      int
		elapsed    time.Duration
		wantPrefix string
		wantHasETA bool
	}{
		{"no total", 100, 0, 30 * time.Second, "  [fetched: 100]", false},
		{"with total, before warmup", 100, 1000, 2 * time.Second, "  [fetched: 100/1000]", false},
		{"with total, after warmup", 100, 16000, 10 * time.Second, "  [fetched: 100/16000 ETA ", true},
		{"complete (n == total)", 1000, 1000, 30 * time.Second, "  [fetched: 1000/1000]", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &progressPrinter{total: tc.total}
			got := p.formatFetched(tc.n, tc.elapsed)
			if !strings.HasPrefix(got, tc.wantPrefix) {
				t.Errorf("got %q, want prefix %q", got, tc.wantPrefix)
			}
			hasETA := strings.Contains(got, "ETA ")
			if hasETA != tc.wantHasETA {
				t.Errorf("hasETA = %v, want %v (got %q)", hasETA, tc.wantHasETA, got)
			}
		})
	}
}

// StageFetched after StageTotal must include the denominator.
func TestProgressPrinter_FetchedShowsDenominatorAfterTotal(t *testing.T) {
	var buf bytes.Buffer
	p := &progressPrinter{out: &buf, isTTY: true}
	p.SetKind("documents")

	p.Print(syncer.StageTotal, 1000)
	p.Print(syncer.StageFetching, 0)
	p.Print(syncer.StageFetched, 100)
	p.Print(syncer.StageFetched, 200)
	p.Print(syncer.StageDone, 200)

	want := "syncing documents...\n" +
		"\r  [fetched: 100/1000]" +
		"\r  [fetched: 200/1000]" +
		"\n" +
		"documents sync ok (200 upserted)\n"
	if got := buf.String(); got != want {
		t.Errorf("output mismatch:\n got: %q\nwant: %q", got, want)
	}
}

// Without StageTotal, fall back to the legacy form (no denominator).
func TestProgressPrinter_FetchedNoDenominatorWithoutTotal(t *testing.T) {
	var buf bytes.Buffer
	p := &progressPrinter{out: &buf, isTTY: false}
	p.SetKind("documents")

	p.Print(syncer.StageFetching, 0)
	p.Print(syncer.StageFetched, 100)

	want := "syncing documents...\n  [fetched: 100]\n"
	if got := buf.String(); got != want {
		t.Errorf("output mismatch:\n got: %q\nwant: %q", got, want)
	}
}

// SetKind also resets total so the previous section's denominator does not leak into the next.
func TestProgressPrinter_SetKindResetsTotal(t *testing.T) {
	var buf bytes.Buffer
	p := &progressPrinter{out: &buf, isTTY: false}

	p.SetKind("documents")
	p.Print(syncer.StageTotal, 1000)
	p.Print(syncer.StageFetched, 50)

	p.SetKind("issues")
	p.Print(syncer.StageFetched, 10) // total reset → no denominator

	want := "  [fetched: 50/1000]\n  [fetched: 10]\n"
	if got := buf.String(); got != want {
		t.Errorf("output mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestProgressPrinter_TTYUsesCarriageReturn(t *testing.T) {
	var buf bytes.Buffer
	p := &progressPrinter{out: &buf, isTTY: true}
	p.SetKind("issues")

	// Typical sync sequence: multiple fetched → filtered → upserting → done.
	p.Print(syncer.StageFetching, 0)
	p.Print(syncer.StageFetched, 100)
	p.Print(syncer.StageFetched, 200)
	p.Print(syncer.StageFiltered, 5)
	p.Print(syncer.StageUpserting, 5)
	p.Print(syncer.StageDone, 5)

	want := "syncing issues...\n" +
		"\r  [fetched: 100]" +
		"\r  [fetched: 200]" +
		"\n" + // newline that closes the fetched line
		"filtered to 5 new\n" +
		"upserting 5...\n" +
		"issues sync ok (5 upserted)\n"
	if got := buf.String(); got != want {
		t.Errorf("TTY output mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestProgressPrinter_NonTTYUsesPerLineOutput(t *testing.T) {
	var buf bytes.Buffer
	p := &progressPrinter{out: &buf, isTTY: false}
	p.SetKind("issues")

	p.Print(syncer.StageFetching, 0)
	p.Print(syncer.StageFetched, 100)
	p.Print(syncer.StageFetched, 200)
	p.Print(syncer.StageFiltered, 5)
	p.Print(syncer.StageUpserting, 5)
	p.Print(syncer.StageDone, 5)

	want := "syncing issues...\n" +
		"  [fetched: 100]\n" +
		"  [fetched: 200]\n" +
		"filtered to 5 new\n" +
		"upserting 5...\n" +
		"issues sync ok (5 upserted)\n"
	if got := buf.String(); got != want {
		t.Errorf("non-TTY output mismatch:\n got: %q\nwant: %q", got, want)
	}
}

// Close finalizes any open "[fetched]" line with a newline (safety net for early returns).
func TestProgressPrinter_CloseFlushesActiveFetched(t *testing.T) {
	var buf bytes.Buffer
	p := &progressPrinter{out: &buf, isTTY: true}
	p.SetKind("issues")
	p.Print(syncer.StageFetching, 0)
	p.Print(syncer.StageFetched, 100)
	// Close before Filtered/Done (the early-error scenario).
	p.Close()

	want := "syncing issues...\n\r  [fetched: 100]\n"
	if got := buf.String(); got != want {
		t.Errorf("Close output mismatch:\n got: %q\nwant: %q", got, want)
	}
}

// Notice closes the in-flight fetched line, then prints msg on its own line (used for rate-limit notices, etc.).
func TestProgressPrinter_NoticeClosesFetchedAndPrintsMessage(t *testing.T) {
	var buf bytes.Buffer
	p := &progressPrinter{out: &buf, isTTY: true}
	p.SetKind("issues")
	p.Print(syncer.StageFetching, 0)
	p.Print(syncer.StageFetched, 100)
	p.Notice("rate limited, waiting 5s...")

	want := "syncing issues...\n\r  [fetched: 100]\nrate limited, waiting 5s...\n"
	if got := buf.String(); got != want {
		t.Errorf("Notice output mismatch:\n got: %q\nwant: %q", got, want)
	}
}

// On a zero-result sync the syncer does not fire StageFetched, so no closing newline is inserted.
func TestProgressPrinter_NoSpuriousNewlineWhenNoFetched(t *testing.T) {
	var buf bytes.Buffer
	p := &progressPrinter{out: &buf, isTTY: true}
	p.SetKind("issues")

	p.Print(syncer.StageFetching, 0)
	// No StageFetched (the syncer suppresses empty pages).
	p.Print(syncer.StageFiltered, 0)
	p.Print(syncer.StageDone, 0)

	want := "syncing issues...\n" +
		"filtered to 0 new\n" +
		"issues sync ok (0 upserted)\n"
	if got := buf.String(); got != want {
		t.Errorf("empty-fetch output mismatch:\n got: %q\nwant: %q", got, want)
	}
}

// When kind is unset (SetKind never called), fall back to "fetching..." / "sync ok (...)".
func TestProgressPrinter_FallbackWhenKindUnset(t *testing.T) {
	var buf bytes.Buffer
	p := &progressPrinter{out: &buf, isTTY: false}

	p.Print(syncer.StageFetching, 0)
	p.Print(syncer.StageDone, 5)

	want := "fetching...\nsync ok (5 upserted)\n"
	if got := buf.String(); got != want {
		t.Errorf("fallback output mismatch:\n got: %q\nwant: %q", got, want)
	}
}

// Behavior when sections switch (issues → documents) consecutively.
// On SetKind switch, any open "[fetched]" line must be closed with a
// newline, and each section must have its own kind-labeled header / footer.
func TestProgressPrinter_KindSwitch(t *testing.T) {
	var buf bytes.Buffer
	p := &progressPrinter{out: &buf, isTTY: true}

	p.SetKind("issues")
	p.Print(syncer.StageFetching, 0)
	p.Print(syncer.StageFetched, 100)
	p.Print(syncer.StageDone, 100)

	p.SetKind("documents")
	p.Print(syncer.StageFetching, 0)
	p.Print(syncer.StageFetched, 50)
	p.Print(syncer.StageDone, 50)

	want := "syncing issues...\n" +
		"\r  [fetched: 100]" +
		"\n" +
		"issues sync ok (100 upserted)\n" +
		"syncing documents...\n" +
		"\r  [fetched: 50]" +
		"\n" +
		"documents sync ok (50 upserted)\n"
	if got := buf.String(); got != want {
		t.Errorf("kind switch output mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestConfirmYesNo(t *testing.T) {
	cases := []struct {
		name       string
		input      string
		defaultYes bool
		want       bool
	}{
		{"empty default no", "\n", false, false},
		{"empty default yes", "\n", true, true},
		{"y", "y\n", false, true},
		{"yes", "yes\n", false, true},
		{"n", "n\n", true, false},
		{"random", "maybe\n", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := confirmYesNo(strings.NewReader(tc.input), &bytes.Buffer{}, "ok?", tc.defaultYes)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
