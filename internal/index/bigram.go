package index

import (
	"database/sql/driver"
	"strings"

	"modernc.org/sqlite"
)

// init registers the BIGRAM(text) SQL function used by the FTS5 triggers.
// BIGRAM(text) returns the same space-separated rune-pair tokens as
// bigramTokens. Registered globally on the modernc.org/sqlite driver so
// CREATE TRIGGER statements can call it.
func init() {
	sqlite.MustRegisterDeterministicScalarFunction("BIGRAM", 1, bigramSQL)
}

// bigramSQL is the SQL-callable adapter for bigramTokens.
// NULL or non-string input is treated as the empty string.
func bigramSQL(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) != 1 || args[0] == nil {
		return "", nil
	}
	switch v := args[0].(type) {
	case string:
		return bigramTokens(v), nil
	case []byte:
		return bigramTokens(string(v)), nil
	default:
		return "", nil
	}
}

// bigramTokens returns space-separated bigrams (rune pairs) for storage in
// or querying against an FTS5 unicode61-tokenized index. Whitespace
// separates "words"; each word is bigrammed independently so spaces never
// appear inside a token. Words shorter than 2 runes contribute nothing.
//
// Examples:
//
//	bigramTokens("認証バグ")    -> "認証 証バ バグ"
//	bigramTokens("PROJ alice") -> "PR RO OJ al li ic ce"
//	bigramTokens("a")          -> ""
//	bigramTokens("")           -> ""
func bigramTokens(s string) string {
	var b strings.Builder
	first := true
	for _, word := range strings.Fields(s) {
		runes := []rune(word)
		for i := 0; i+2 <= len(runes); i++ {
			if !first {
				b.WriteByte(' ')
			}
			b.WriteString(string(runes[i : i+2]))
			first = false
		}
	}
	return b.String()
}

// bigramQuery converts a user query into:
//   - ftsExpr: an AND-joined FTS5 MATCH expression of quoted bigrams
//     (suitable for `MATCH ?`).
//   - likeNeedles: the original whitespace-split words for the LIKE
//     post-filter that verifies substring contiguity.
//   - matchable: false when every word has fewer than 2 runes; the caller
//     should return zero rows in that case (1-char queries cannot bigram).
//
// Each word in the input contributes overlapping bigrams that are ANDed
// inside the FTS query; multiple words are also ANDed. The LIKE
// post-filter requires every needle (one per word) to appear as a
// substring in at least one indexed column.
//
// Examples:
//
//	bigramQuery("認証 バグ") -> (`"認証" "バグ"`, ["認証","バグ"], true)
//	bigramQuery("a")         -> ("", nil, false)
func bigramQuery(s string) (ftsExpr string, likeNeedles []string, matchable bool) {
	var terms []string
	for _, word := range strings.Fields(s) {
		runes := []rune(word)
		if len(runes) < 2 {
			continue
		}
		for i := 0; i+2 <= len(runes); i++ {
			t := string(runes[i : i+2])
			t = strings.ReplaceAll(t, `"`, `""`)
			terms = append(terms, `"`+t+`"`)
		}
		likeNeedles = append(likeNeedles, word)
	}
	if len(terms) == 0 {
		return "", nil, false
	}
	return strings.Join(terms, " "), likeNeedles, true
}
