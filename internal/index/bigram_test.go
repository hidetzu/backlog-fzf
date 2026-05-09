package index

import (
	"reflect"
	"testing"
)

func TestBigramTokens(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"single rune", "a", ""},
		{"two ASCII", "ab", "ab"},
		{"four ASCII", "PROJ", "PR RO OJ"},
		{"two Japanese runes", "認証", "認証"},
		{"four Japanese runes", "認証バグ", "認証 証バ バグ"},
		{"mixed JP and ASCII", "認証 fix", "認証 fi ix"},
		{"multi-word ASCII", "PROJ alice", "PR RO OJ al li ic ce"},
		{"tab and multiple spaces normalised", "  PROJ\talice  ", "PR RO OJ al li ic ce"},
		{"single-rune word ignored", "PR a OJ", "PR OJ"},
		{"hyphen treated as a regular rune", "a-b", "a- -b"},
		{"trailing whitespace", "認証 ", "認証"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := bigramTokens(tc.in); got != tc.want {
				t.Errorf("bigramTokens(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestBigramQuery(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		wantFTS     string
		wantNeedles []string
		wantOK      bool
	}{
		{"empty", "", "", nil, false},
		{"whitespace only", "   ", "", nil, false},
		{"single rune", "a", "", nil, false},
		{"only single-rune words", "a b c", "", nil, false},
		{"two Japanese", "認証", `"認証"`, []string{"認証"}, true},
		{"three Japanese", "認証バ", `"認証" "証バ"`, []string{"認証バ"}, true},
		{"two words", "認証 バグ", `"認証" "バグ"`, []string{"認証", "バグ"}, true},
		{"single-rune word skipped, longer kept", "a 認証", `"認証"`, []string{"認証"}, true},
		{"ASCII", "alice", `"al" "li" "ic" "ce"`, []string{"alice"}, true},
		{"escaped double quote", `say"hi`, `"sa" "ay" "y""" """h" "hi"`, []string{`say"hi`}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotFTS, gotNeedles, gotOK := bigramQuery(tc.in)
			if gotOK != tc.wantOK {
				t.Errorf("ok = %v, want %v", gotOK, tc.wantOK)
			}
			if gotFTS != tc.wantFTS {
				t.Errorf("ftsExpr = %q, want %q", gotFTS, tc.wantFTS)
			}
			if !reflect.DeepEqual(gotNeedles, tc.wantNeedles) {
				t.Errorf("needles = %v, want %v", gotNeedles, tc.wantNeedles)
			}
		})
	}
}
