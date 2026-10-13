package embed

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// TextOptions controls how records are turned into embedding inputs.
type TextOptions struct {
	DocumentPrefix string // prepended to every document / issue text (model instruction)
	MaxIssueChars  int    // runes of the issue description kept (<= 0: none)
	ChunkChars     int    // target runes per document chunk (<= 0: single chunk of everything)
	ChunkOverlap   int    // runes repeated between neighbouring chunks
	MaxChunks      int    // chunks kept per document (<= 0: unlimited)
}

// DefaultChunkOverlap is the overlap used when TextOptions.ChunkOverlap is 0.
const DefaultChunkOverlap = 100

// IssueText returns the single string embedded for an issue:
//
//	{prefix}title: {summary} | text: {description truncated to MaxIssueChars}
//
// The "title: ... | text: ..." shape is the EmbeddingGemma document
// format; other models treat it as plain text.
func IssueText(opts TextOptions, summary, description string) string {
	return opts.DocumentPrefix + formatDoc(summary, truncateRunes(normalize(description), opts.MaxIssueChars))
}

// DocumentTexts splits a document body into chunks and returns one
// embedding input per chunk, each carrying the title. Chunk boundaries
// prefer paragraph breaks (blank lines); a paragraph longer than
// ChunkChars is cut at ChunkChars with ChunkOverlap runes repeated. An
// empty body yields a single title-only input so the document is still
// searchable by its title.
func DocumentTexts(opts TextOptions, title, body string) []string {
	chunks := chunkText(normalize(body), opts.ChunkChars, overlapOf(opts))
	if opts.MaxChunks > 0 && len(chunks) > opts.MaxChunks {
		chunks = chunks[:opts.MaxChunks]
	}
	out := make([]string, len(chunks))
	for i, c := range chunks {
		out[i] = opts.DocumentPrefix + formatDoc(title, c)
	}
	return out
}

func overlapOf(opts TextOptions) int {
	if opts.ChunkOverlap > 0 {
		return opts.ChunkOverlap
	}
	return DefaultChunkOverlap
}

func formatDoc(title, text string) string {
	title = strings.Join(strings.Fields(title), " ")
	if title == "" {
		title = "none"
	}
	return "title: " + title + " | text: " + text
}

// Hash identifies the input that produced a stored vector: the model and
// the text itself. A stored row whose hash differs from Hash(current
// text) is stale. The dimension count is deliberately not part of the
// hash: it is checked against embedding_meta instead, so switching the
// config from "native" (0) to the same explicit value does not silently
// re-embed everything.
func Hash(model, text string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00", model)
	h.Write([]byte(text))
	return hex.EncodeToString(h.Sum(nil))
}

// normalize collapses runs of spaces/tabs and trims each line, and folds
// three or more consecutive newlines into two (one blank line). Markup
// is left alone on purpose: stripping Backlog/Markdown syntax is a
// quality experiment for later, not an MVP requirement.
func normalize(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	var b strings.Builder
	blank := 0
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			blank++
			continue
		}
		if b.Len() > 0 {
			if blank > 0 {
				b.WriteString("\n\n")
			} else {
				b.WriteByte('\n')
			}
		}
		blank = 0
		b.WriteString(line)
	}
	return b.String()
}

func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// chunkText packs paragraphs into chunks of at most size runes. A single
// paragraph longer than size is split into windows of size runes that
// overlap by `overlap` runes. size <= 0 returns the whole text as one chunk.
func chunkText(s string, size, overlap int) []string {
	if s == "" {
		return []string{""}
	}
	if size <= 0 {
		return []string{s}
	}
	if overlap >= size {
		overlap = size / 4
	}
	var chunks []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			chunks = append(chunks, string(cur))
			cur = nil
		}
	}
	for _, para := range strings.Split(s, "\n\n") {
		pr := []rune(para)
		if len(pr) > size {
			flush()
			for start := 0; start < len(pr); start += size - overlap {
				end := min(start+size, len(pr))
				chunks = append(chunks, string(pr[start:end]))
				if end == len(pr) {
					break
				}
			}
			continue
		}
		if len(cur) > 0 && len(cur)+2+len(pr) > size {
			flush()
		}
		if len(cur) > 0 {
			cur = append(cur, '\n', '\n')
		}
		cur = append(cur, pr...)
	}
	flush()
	return chunks
}
