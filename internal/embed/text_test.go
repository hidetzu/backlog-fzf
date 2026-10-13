package embed

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestIssueText_FormatAndTruncation(t *testing.T) {
	opts := TextOptions{DocumentPrefix: "P:", MaxIssueChars: 5}
	got := IssueText(opts, "  認証  バグ ", "本文です。長い本文")
	want := "P:title: 認証 バグ | text: 本文です。"
	if got != want {
		t.Errorf("IssueText = %q, want %q", got, want)
	}
}

func TestIssueText_EmptyDescriptionAndTitle(t *testing.T) {
	if got := IssueText(TextOptions{MaxIssueChars: 10}, "", ""); got != "title: none | text: " {
		t.Errorf("got %q", got)
	}
}

func TestNormalize(t *testing.T) {
	in := "a  b\t c\r\n\n\n\n d \n\ne"
	want := "a b c\n\nd\n\ne"
	if got := normalize(in); got != want {
		t.Errorf("normalize = %q, want %q", got, want)
	}
}

func TestDocumentTexts_EmptyBodyStillYieldsTitle(t *testing.T) {
	got := DocumentTexts(TextOptions{ChunkChars: 10}, "Guide", "")
	if len(got) != 1 || got[0] != "title: Guide | text: " {
		t.Errorf("got %q", got)
	}
}

func TestDocumentTexts_PacksParagraphsAndCapsChunks(t *testing.T) {
	body := strings.Repeat("あ", 40) + "\n\n" + strings.Repeat("い", 40) + "\n\n" + strings.Repeat("う", 40)
	got := DocumentTexts(TextOptions{ChunkChars: 90, MaxChunks: 10}, "T", body)
	if len(got) != 2 {
		t.Fatalf("expected 2 chunks (2 paragraphs fit in 90 runes, 3 do not), got %d: %q", len(got), got)
	}
	if !strings.HasPrefix(got[0], "title: T | text: ") || !strings.Contains(got[0], "あ") || !strings.Contains(got[0], "い") {
		t.Errorf("chunk 0 = %q", got[0])
	}
	if !strings.Contains(got[1], "う") || strings.Contains(got[1], "あ") {
		t.Errorf("chunk 1 = %q", got[1])
	}
	capped := DocumentTexts(TextOptions{ChunkChars: 40, MaxChunks: 1}, "T", body)
	if len(capped) != 1 {
		t.Errorf("MaxChunks=1 should cap to 1 chunk, got %d", len(capped))
	}
}

func TestChunkText_LongParagraphWindowsWithOverlap(t *testing.T) {
	para := strings.Repeat("x", 250)
	chunks := chunkText(para, 100, 20)
	// Windows start at 0, 80, 160 (160+100 > 250 ends the walk) → 3 chunks;
	// the last holds the remaining 90 runes.
	if got := lens(chunks); len(got) != 3 || got[0] != 100 || got[1] != 100 || got[2] != 90 {
		t.Fatalf("chunk lengths = %v, want [100 100 90]", got)
	}
	// Overlap: chunk 1 starts 20 runes before chunk 0 ends.
	if !strings.HasSuffix(chunks[0], strings.Repeat("x", 20)) {
		t.Error("expected overlap at window boundary")
	}
}

func TestChunkText_EdgeCases(t *testing.T) {
	if got := chunkText("", 10, 2); len(got) != 1 || got[0] != "" {
		t.Errorf("empty: %q", got)
	}
	if got := chunkText("a", 10, 2); len(got) != 1 || got[0] != "a" {
		t.Errorf("single rune: %q", got)
	}
	if got := chunkText("whole text", 0, 0); len(got) != 1 || got[0] != "whole text" {
		t.Errorf("size<=0: %q", got)
	}
	// overlap >= size must not loop forever.
	if got := chunkText(strings.Repeat("y", 30), 10, 50); len(got) == 0 {
		t.Error("overlap >= size produced no chunks")
	}
}

func TestHash_DependsOnModelAndText(t *testing.T) {
	a := Hash("m", "t")
	if a != Hash("m", "t") {
		t.Error("Hash is not deterministic")
	}
	for _, other := range []string{Hash("m2", "t"), Hash("m", "t2")} {
		if other == a {
			t.Error("Hash should change with model / text")
		}
	}
	if len(a) != 64 {
		t.Errorf("Hash length = %d, want 64 hex chars", len(a))
	}
}

func lens(ss []string) []int {
	out := make([]int, len(ss))
	for i, s := range ss {
		out[i] = utf8.RuneCountInString(s)
	}
	return out
}
