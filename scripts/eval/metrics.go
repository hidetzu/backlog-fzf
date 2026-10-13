package main

import "fmt"

// result is the ranked list returned for one case, as "issue:KEY" /
// "doc:ID" refs, best first.
type result struct {
	c     evalCase
	ranks []string
}

// summary aggregates retrieval metrics over all cases.
type summary struct {
	n                 int
	hitsAt1, hitsAt5  int
	reciprocalRankSum float64
	misses            []evalCase // cases with no wanted ref in the returned list
}

// rankOf returns the 1-based rank of the first wanted ref in ranks, or 0.
func rankOf(ranks []string, want []string) int {
	wanted := map[string]bool{}
	for _, w := range want {
		wanted[w] = true
	}
	for i, r := range ranks {
		if wanted[r] {
			return i + 1
		}
	}
	return 0
}

// summarize computes Recall@1, Recall@5 and MRR (over the returned list).
func summarize(results []result) summary {
	var s summary
	for _, r := range results {
		s.n++
		rank := rankOf(r.ranks, r.c.want)
		if rank == 0 {
			s.misses = append(s.misses, r.c)
			continue
		}
		if rank == 1 {
			s.hitsAt1++
		}
		if rank <= 5 {
			s.hitsAt5++
		}
		s.reciprocalRankSum += 1 / float64(rank)
	}
	return s
}

func (s summary) recallAt1() float64 { return ratio(s.hitsAt1, s.n) }
func (s summary) recallAt5() float64 { return ratio(s.hitsAt5, s.n) }
func (s summary) mrr() float64       { return s.reciprocalRankSum / float64(max(s.n, 1)) }

func ratio(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

func (s summary) String() string {
	return fmt.Sprintf("cases=%d  Recall@1=%.2f  Recall@5=%.2f  MRR=%.2f", s.n, s.recallAt1(), s.recallAt5(), s.mrr())
}
