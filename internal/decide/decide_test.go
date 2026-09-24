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
func TestNextNewThenKeep(t *testing.T) {
	tb := tbl(map[string]table.Model{"a": mk("mid", 5, s("arena-coding", "", 1500, 5))})
	d, _ := Next(Input{Table: tb, Category: "backend", Difficulty: Routine}, State{}, true)
	if d.Reason != "new" || d.State.Model != "a" {
		t.Fatalf("%+v", d)
	}
	d2, _ := Next(Input{Table: tb, Category: "webdev", Difficulty: Trivial}, d.State, true)
	if d2.Reason != "keep" || d2.Model != "a" || d2.Thinking != "high" {
		t.Fatalf("never downgrade: %+v", d2)
	}
}

func TestNextEscalateThinkingSameTier(t *testing.T) {
	tb := tbl(map[string]table.Model{"a": mk("top", 5, s("arena-coding", "", 1500, 5)), "b": mk("top", 1, s("arena-coding", "", 1600, 5))})
	prev := State{Difficulty: Hard, Model: "a", Thinking: "xhigh", Tier: "top"}
	d, _ := Next(Input{Table: tb, Category: "backend", Difficulty: Extreme}, prev, true)
	if d.Reason != "escalate-thinking" || d.Model != "a" || d.Thinking != "max" {
		t.Fatalf("%+v", d)
	}
}

func TestNextEscalateTierRechooses(t *testing.T) {
	tb := tbl(map[string]table.Model{"m": mk("mid", 5, s("arena-coding", "", 1500, 5)), "t": mk("top", 50, s("arena-coding", "", 1700, 5))})
	prev := State{Difficulty: Routine, Model: "m", Thinking: "high", Tier: "mid"}
	d, _ := Next(Input{Table: tb, Category: "backend", Difficulty: Hard}, prev, true)
	if d.Reason != "escalate-tier" || d.Model != "t" || d.Thinking != "xhigh" {
		t.Fatalf("%+v", d)
	}
}

func TestNextVisionSwapStaysInTier(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"blind": mkv("mid", 1, false, s("arena-overall", "", 1500, 5)),
		"eyes":  mkv("mid", 5, true, s("arena-overall", "", 1450, 5)),
		"top":   mkv("top", 50, true, s("arena-overall", "", 1800, 5)),
	})
	prev := State{Difficulty: Routine, Model: "blind", Thinking: "high", Tier: "mid"}
	d, _ := Next(Input{Table: tb, Category: "writing", Difficulty: Routine, HasImage: true}, prev, true)
	if d.Reason != "vision-swap" || d.Model != "eyes" || d.Thinking != "high" {
		t.Fatalf("%+v", d)
	}
}

func TestNextJevUnavailable(t *testing.T) {
	tb := tbl(map[string]table.Model{"m": mk("mid", 5, s("arena-overall", "", 1500, 5))})
	d, _ := Next(Input{Table: tb}, State{}, false)
	if d.Reason != "jev-unavailable" || d.State.Difficulty != Routine {
		t.Fatalf("%+v", d)
	}
	prev := State{Difficulty: Hard, Model: "x", Thinking: "xhigh", Tier: "top"}
	d, _ = Next(Input{Table: tb}, prev, false)
	if d.Reason != "jev-unavailable" || d.Model != "x" {
		t.Fatalf("%+v", d)
	}
}

func TestNextFallbackWhenStoredUnavailable(t *testing.T) {
	tb := tbl(map[string]table.Model{"a": mk("mid", 5, s("arena-overall", "", 1500, 5)), "b": mk("mid", 5, s("arena-overall", "", 1400, 5))})
	prev := State{Difficulty: Routine, Model: "a", Thinking: "high", Tier: "mid"}
	d, _ := Next(Input{Table: tb, Category: "writing", Difficulty: Trivial, Available: func(m string) bool { return m != "a" }}, prev, true)
	if d.Reason != "fallback" || d.Model != "b" {
		t.Fatalf("%+v", d)
	}
}

func TestNextFallbackHonorsRaisedTier(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"flash": mk("flash", 1, nil),
		"old":   mk("mid", 2, nil),
		"new":   mk("mid", 3, nil),
	})
	prev := State{Difficulty: Trivial, Model: "old", Thinking: "low", Tier: "mid"}
	d, _ := Next(Input{Table: tb, Difficulty: Trivial, Available: func(m string) bool { return m != "old" }}, prev, true)
	if d.Reason != "fallback" || d.Model != "new" || d.Tier != "mid" {
		t.Fatalf("fallback must not lower a raised tier: %+v", d)
	}
}

func TestNextVisionSwapHonorsRaisedTier(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"flash": mkv("flash", 1, true, nil),
		"blind": mkv("mid", 2, false, nil),
		"eyes":  mkv("mid", 3, true, nil),
	})
	prev := State{Difficulty: Trivial, Model: "blind", Thinking: "low", Tier: "mid"}
	d, _ := Next(Input{Table: tb, Difficulty: Trivial, HasImage: true}, prev, true)
	if d.Reason != "vision-swap" || d.Model != "eyes" || d.Tier != "mid" {
		t.Fatalf("vision swap must not lower a raised tier: %+v", d)
	}
}

func TestNextKeepPreservesRaisedTier(t *testing.T) {
	tb := tbl(map[string]table.Model{"old": mk("top", 2, nil)})
	prev := State{Difficulty: Routine, Model: "old", Thinking: "high", Tier: "top"}
	d, _ := Next(Input{Table: tb, Difficulty: Routine}, prev, true)
	if d.Reason != "keep" || d.Model != "old" || d.Tier != "top" {
		t.Fatalf("keep must preserve actual stored tier: %+v", d)
	}
}

func TestNextJevUnavailableVisionSwapPreservesState(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"blind": mkv("mid", 1, false, nil),
		"eyes":  mkv("mid", 2, true, nil),
	})
	prev := State{Difficulty: Routine, Model: "blind", Thinking: "high", Tier: "mid"}
	d, err := Next(Input{Table: tb, Category: "writing", Difficulty: Routine, HasImage: true}, prev, false)
	if err != nil {
		t.Fatal(err)
	}
	if d.Reason != "vision-swap" || d.Model != "eyes" || d.Tier != "mid" || d.State.Difficulty != Routine || d.Thinking != "high" {
		t.Fatalf("Jev outage must preserve state while swapping vision model: %+v", d)
	}
}
