package main

import (
	"math"
	"testing"
)

func TestRankOf(t *testing.T) {
	ranks := []string{"issue:A", "doc:1", "issue:B"}
	if got := rankOf(ranks, []string{"issue:B"}); got != 3 {
		t.Errorf("rankOf = %d, want 3", got)
	}
	if got := rankOf(ranks, []string{"doc:9", "doc:1"}); got != 2 {
		t.Errorf("any-of: rankOf = %d, want 2", got)
	}
	if got := rankOf(ranks, []string{"issue:Z"}); got != 0 {
		t.Errorf("missing: rankOf = %d, want 0", got)
	}
}

func TestSummarize(t *testing.T) {
	results := []result{
		{evalCase{"q1", []string{"issue:A"}}, []string{"issue:A", "issue:B"}},           // rank 1
		{evalCase{"q2", []string{"issue:B"}}, []string{"issue:A", "issue:B"}},           // rank 2
		{evalCase{"q3", []string{"doc:1"}}, []string{"a", "b", "c", "d", "e", "doc:1"}}, // rank 6 (> 5)
		{evalCase{"q4", []string{"doc:2"}}, []string{"a"}},                              // miss
	}
	s := summarize(results)
	if s.n != 4 || s.hitsAt1 != 1 || s.hitsAt5 != 2 {
		t.Errorf("counts = %+v", s)
	}
	if math.Abs(s.recallAt1()-0.25) > 1e-9 || math.Abs(s.recallAt5()-0.5) > 1e-9 {
		t.Errorf("recall@1=%v recall@5=%v", s.recallAt1(), s.recallAt5())
	}
	wantMRR := (1 + 0.5 + 1.0/6) / 4
	if math.Abs(s.mrr()-wantMRR) > 1e-9 {
		t.Errorf("mrr = %v, want %v", s.mrr(), wantMRR)
	}
	if len(s.misses) != 1 || s.misses[0].query != "q4" {
		t.Errorf("misses = %+v", s.misses)
	}
}

func TestCases_ReferToSeededRecords(t *testing.T) {
	refs := seededRefs()
	for _, c := range cases {
		if len(c.want) == 0 {
			t.Errorf("case %q has no expected record", c.query)
		}
		for _, w := range c.want {
			if !refs[w] {
				t.Errorf("case %q expects %q, which is not in scripts/demodata", c.query, w)
			}
		}
	}
}
