package decide

import (
	"testing"

	"github.com/chloeassistant/cpa-plugin-auto-router/internal/table"
)

func tbl(models map[string]table.Model) *table.Table {
	return &table.Table{
		Benchmarks: map[string]struct{ Source, Unit string }{
			"arena-webdev":     {Source: "test", Unit: "elo"},
			"arena-coding":     {Source: "test", Unit: "elo"},
			"arena-overall":    {Source: "test", Unit: "elo"},
			"terminal-bench-4": {Source: "test", Unit: "pct"},
		},
		Models: models,
	}
}

func mk(tier string, cost float64, scores map[string][]table.Score) table.Model {
	var m table.Model
	m.Tier = tier
	m.Cost.Input = cost
	m.Scores = scores
	return m
}

func mkv(tier string, cost float64, vision bool, scores map[string][]table.Score) table.Model {
	m := mk(tier, cost, scores)
	m.Vision = vision
	return m
}

func s(bench, effort string, value, margin float64) map[string][]table.Score {
	return map[string][]table.Score{
		bench: {{Effort: effort, Value: value, Margin: margin, Date: "2026-09-24"}},
	}
}

func TestChooseRankedByCategoryBenchmark(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"a": mk("top", 60, s("terminal-bench-4", "xhigh", 57.9, 2.7)),
		"b": mk("top", 12, s("terminal-bench-4", "xhigh", 37.3, 3.8)),
	})
	c, _ := Choose(Input{Table: tb, Category: "agentic-terminal", Difficulty: Hard})
	if c.Model != "a" || c.Thinking != "xhigh" || c.Benchmark != "terminal-bench-4" {
		t.Fatalf("%+v", c)
	}
}

func TestTieBreaksOnCost(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"a": mk("top", 60, s("arena-webdev", "", 1800, 16)),
		"b": mk("top", 12, s("arena-webdev", "", 1790, 14)),
	})
	c, _ := Choose(Input{Table: tb, Category: "webdev", Difficulty: Hard})
	if c.Model != "b" {
		t.Fatalf("tie within margin must pick cheaper: %+v", c)
	}
}

func TestEffortPicksClosestBelowNeverAbove(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"a": mk("top", 60, map[string][]table.Score{
			"terminal-bench-4": {
				{Effort: "max", Value: 99, Margin: 1, Date: "2026-09-24"},
				{Effort: "high", Value: 50, Margin: 1, Date: "2026-09-24"},
			},
		}),
		"b": mk("top", 60, s("terminal-bench-4", "xhigh", 55, 1)),
	})
	c, _ := Choose(Input{Table: tb, Category: "agentic-terminal", Difficulty: Hard})
	if c.Model != "b" {
		t.Fatalf("a's max row must not count at xhigh; got %+v", c)
	}
}

func TestUnscoredIsFallbackOnly(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"scored":   mk("mid", 60, s("arena-coding", "", 1500, 5)),
		"unscored": mk("mid", 1, nil),
	})
	c, _ := Choose(Input{Table: tb, Category: "webdev", Difficulty: Routine})
	if c.Model != "scored" {
		t.Fatalf("%+v", c)
	}
	c, _ = Choose(Input{Table: tb, Category: "webdev", Difficulty: Routine, Available: func(m string) bool { return m != "scored" }})
	if c.Model != "unscored" || c.Reason != "fallback-unscored" {
		t.Fatalf("%+v", c)
	}
}

func TestVisionFilterAndTierRaise(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"blind": mkv("flash", 1, false, s("arena-overall", "", 1400, 5)),
		"eyes":  mkv("mid", 5, true, s("arena-overall", "", 1450, 5)),
	})
	c, _ := Choose(Input{Table: tb, Category: "extraction", Difficulty: Trivial, HasImage: true})
	if c.Model != "eyes" || c.Reason != "tier-raised" || c.Tier != "mid" {
		t.Fatalf("%+v", c)
	}
}

func TestUnknownCategoryUsesGeneral(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"a": mk("top", 60, s("arena-overall", "", 1500, 5)),
		"b": mk("top", 60, s("arena-webdev", "", 1900, 5)),
	})
	c, _ := Choose(Input{Table: tb, Category: "", Difficulty: Hard})
	if c.Model != "a" || c.Benchmark != "arena-overall" {
		t.Fatalf("%+v", c)
	}
}

func TestExtractionPicksCheapest(t *testing.T) {
	tb := tbl(map[string]table.Model{"x": mk("flash", 3, nil), "y": mk("flash", 1, nil)})
	c, _ := Choose(Input{Table: tb, Category: "extraction", Difficulty: Trivial})
	if c.Model != "y" {
		t.Fatalf("%+v", c)
	}
}

func TestExtractionIgnoresScores(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"expensive": mk("flash", 9, s("arena-overall", "", 2000, 1)),
		"cheap":     mk("flash", 1, s("arena-overall", "", 1000, 1)),
	})
	c, _ := Choose(Input{Table: tb, Category: "extraction", Difficulty: Trivial})
	if c.Model != "cheap" || c.Benchmark != "" || c.Reason != "fallback-unscored" {
		t.Fatalf("extraction must choose cheapest without ranking: %+v", c)
	}
}

func TestTieBreakAnchorsHighestScore(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"highest": mk("top", 10, s("arena-webdev", "", 100, 5)),
		"middle":  mk("top", 5, s("arena-webdev", "", 96, 5)),
		"lowest":  mk("top", 1, s("arena-webdev", "", 92, 5)),
	})
	c, _ := Choose(Input{Table: tb, Category: "webdev", Difficulty: Hard})
	if c.Model != "middle" {
		t.Fatalf("tie chain must anchor to highest score: %+v", c)
	}
}

func TestEqualTiesAreDeterministic(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"b": mk("top", 1, s("arena-webdev", "", 100, 0)),
		"a": mk("top", 1, s("arena-webdev", "", 100, 0)),
	})
	for i := 0; i < 20; i++ {
		c, _ := Choose(Input{Table: tb, Category: "webdev", Difficulty: Hard})
		if c.Model != "a" {
			t.Fatalf("equal ties must be deterministic: %+v", c)
		}
	}
}

func TestTierRaisePreservesReasonWhenUnscored(t *testing.T) {
	tb := tbl(map[string]table.Model{"top": mk("top", 1, nil)})
	c, _ := Choose(Input{Table: tb, Category: "webdev", Difficulty: Routine})
	if c.Model != "top" || c.Tier != "top" || c.Reason != "tier-raised" || c.Score != 0 {
		t.Fatalf("tier raise must survive unscored fallback: %+v", c)
	}
}
