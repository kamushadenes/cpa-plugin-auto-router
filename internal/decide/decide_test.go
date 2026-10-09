package decide

import (
	"testing"

	"github.com/kamushadenes/cpa-plugin-auto-router/internal/table"
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

func TestChooseExcludeTopCandidateSelectsNextSameTier(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"top-a": mk("top", 10, s("arena-webdev", "xhigh", 1900, 1)),
		"top-b": mk("top", 20, s("arena-webdev", "xhigh", 1700, 1)),
	})
	c, _ := Choose(Input{
		Table:      tb,
		Category:   "webdev",
		Difficulty: Hard,
		Exclude:    func(model string) bool { return model == "top-a" },
	})
	if c.Model != "top-b" || c.Tier != "top" || c.Reason != "ranked" {
		t.Fatalf("excluding top candidate must select next ranked model in same tier: %+v", c)
	}
}

func TestChooseExcludeWholeTierRaisesToNextTier(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"mid-a": mk("mid", 10, s("arena-webdev", "high", 1600, 1)),
		"mid-b": mk("mid", 20, s("arena-webdev", "high", 1500, 1)),
		"top":   mk("top", 30, s("arena-webdev", "high", 1800, 1)),
	})
	c, _ := Choose(Input{
		Table:      tb,
		Category:   "webdev",
		Difficulty: Routine,
		Exclude:    func(model string) bool { return model == "mid-a" || model == "mid-b" },
	})
	if c.Model != "top" || c.Tier != "top" || c.Reason != "tier-raised" {
		t.Fatalf("excluding whole tier must raise to next tier: %+v", c)
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

func TestNextModelGoneNoVisionStaysInTier(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"blind": mkv("mid", 1, false, s("arena-overall", "", 1500, 5)),
		"eyes":  mkv("mid", 5, true, s("arena-overall", "", 1450, 5)),
		"top":   mkv("top", 50, true, s("arena-overall", "", 1800, 5)),
	})
	prev := State{Difficulty: Routine, Model: "blind", Thinking: "high", Tier: "mid"}
	d, _ := Next(Input{Table: tb, Category: "writing", Difficulty: Routine, HasImage: true}, prev, true)
	if d.Reason != "model-gone" || d.Model != "eyes" || d.Thinking != "high" {
		t.Fatalf("%+v", d)
	}
}

func TestNextJevUnavailable(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"m": mk("mid", 5, s("arena-overall", "", 1500, 5)),
		"x": mk("top", 5, nil),
	})
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

func TestNextModelGoneNoVisionHonorsRaisedTier(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"flash": mkv("flash", 1, true, nil),
		"blind": mkv("mid", 2, false, nil),
		"eyes":  mkv("mid", 3, true, nil),
	})
	prev := State{Difficulty: Trivial, Model: "blind", Thinking: "low", Tier: "mid"}
	d, _ := Next(Input{Table: tb, Difficulty: Trivial, HasImage: true}, prev, true)
	if d.Reason != "model-gone" || d.Model != "eyes" || d.Tier != "mid" {
		t.Fatalf("model-gone vision replacement must not lower a raised tier: %+v", d)
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

func TestNextJevUnavailableModelGoneNoVisionPreservesState(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"blind": mkv("mid", 1, false, nil),
		"eyes":  mkv("mid", 2, true, nil),
	})
	prev := State{Difficulty: Routine, Model: "blind", Thinking: "high", Tier: "mid"}
	d, err := Next(Input{Table: tb, Category: "writing", Difficulty: Routine, HasImage: true}, prev, false)
	if err != nil {
		t.Fatal(err)
	}
	if d.Reason != "model-gone" || d.Model != "eyes" || d.Tier != "mid" || d.State.Difficulty != Routine || d.Thinking != "high" {
		t.Fatalf("Jev outage must preserve state while replacing unavailable model: %+v", d)
	}
}

func TestNextModelGoneChoosesReplacement(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"replacement": mk("top", 5, nil),
	})
	prev := State{Difficulty: Hard, Model: "removed", Thinking: "max", Tier: "top"}
	d, err := Next(Input{Table: tb, Category: "writing", Difficulty: Trivial}, prev, true)
	if err != nil {
		t.Fatal(err)
	}
	if d.Reason != "model-gone" || d.Model != "replacement" || d.Tier != "top" || d.State.Difficulty != Hard || d.Thinking != "max" {
		t.Fatalf("missing previous model must be replaced without lowering state: %+v", d)
	}
}

func TestNextExcludedPreviousModelChoosesReplacement(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"old":         mk("mid", 1, nil),
		"replacement": mk("mid", 5, nil),
	})
	prev := State{Difficulty: Routine, Model: "old", Thinking: "high", Tier: "mid"}
	d, err := Next(Input{
		Table:      tb,
		Category:   "writing",
		Difficulty: Trivial,
		Exclude:    func(model string) bool { return model == "old" },
	}, prev, true)
	if err != nil {
		t.Fatal(err)
	}
	if d.Reason != "model-gone" || d.Model != "replacement" || d.Tier != "mid" || d.State.Difficulty != Routine || d.Thinking != "high" {
		t.Fatalf("excluded previous model must be replaced without lowering state: %+v", d)
	}
}

func TestNextModelGoneImageChoosesVisionReplacement(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"replacement": mkv("mid", 5, true, nil),
	})
	prev := State{Difficulty: Routine, Model: "removed", Thinking: "high", Tier: "mid"}
	d, err := Next(Input{Table: tb, Category: "writing", Difficulty: Trivial, HasImage: true}, prev, true)
	if err != nil {
		t.Fatal(err)
	}
	if d.Reason != "model-gone" || d.Model != "replacement" || d.Tier != "mid" || !tb.Models[d.Model].Vision {
		t.Fatalf("image request must replace missing non-vision model with vision model: %+v", d)
	}
}

func TestNextModelGoneJevUnavailableChoosesReplacement(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"replacement": mk("top", 5, nil),
	})
	prev := State{Difficulty: Extreme, Model: "removed", Thinking: "max", Tier: "top"}
	d, err := Next(Input{Table: tb, Category: "writing", Difficulty: Extreme}, prev, false)
	if err != nil {
		t.Fatal(err)
	}
	if d.Reason != "model-gone" || d.Model != "replacement" || d.Tier != "top" || d.State.Difficulty != Extreme || d.Thinking != "max" {
		t.Fatalf("missing previous model must not be kept during Jev outage: %+v", d)
	}
}

func TestNextModelGoneWithoutEligibleCandidateReturnsError(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"flash": mk("flash", 1, nil),
	})
	prev := State{Difficulty: Hard, Model: "removed", Thinking: "xhigh", Tier: "top"}
	if _, err := Next(Input{Table: tb, Category: "writing", Difficulty: Hard}, prev, true); err == nil {
		t.Fatal("missing previous model with no eligible replacement must return an error")
	}
}

func withWindow(m table.Model, window int) table.Model {
	m.ContextWindow = window
	return m
}

func TestChooseContextWindowThreshold(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"tight": withWindow(mk("mid", 1, s("arena-coding", "", 1600, 5)), 200_000),
		"roomy": withWindow(mk("mid", 5, s("arena-coding", "", 1500, 5)), 1_000_000),
	})
	in := Input{Table: tb, Category: "backend", Difficulty: Routine, EstTokens: 180_000}
	c, err := Choose(in)
	if err != nil {
		t.Fatal(err)
	}
	if c.Model != "tight" || c.ContextFiltered {
		t.Fatalf("estimate exactly at 90%% of the window must fit: %+v", c)
	}
	in.EstTokens = 180_001
	c, err = Choose(in)
	if err != nil {
		t.Fatal(err)
	}
	if c.Model != "roomy" || c.Tier != "mid" || !c.ContextFiltered {
		t.Fatalf("estimate past 90%% of the window must drop the model: %+v", c)
	}
}

func TestChooseMissingContextWindowIsUnlimited(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"unknown": mk("mid", 1, s("arena-coding", "", 1600, 5)),
		"zero":    withWindow(mk("mid", 5, s("arena-coding", "", 1500, 5)), 0),
	})
	c, err := Choose(Input{Table: tb, Category: "backend", Difficulty: Routine, EstTokens: 5_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if c.Model != "unknown" || c.Reason != "ranked" || c.ContextFiltered {
		t.Fatalf("unknown context window must fail open: %+v", c)
	}
}

func TestChooseRaisesTierWhenNoModelFitsContext(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"mid-tight": withWindow(mk("mid", 1, s("arena-coding", "", 1600, 5)), 200_000),
		"top-roomy": withWindow(mk("top", 50, s("arena-coding", "", 1400, 5)), 1_000_000),
	})
	in := Input{Table: tb, Category: "backend", Difficulty: Routine, EstTokens: 100_000}
	c, err := Choose(in)
	if err != nil {
		t.Fatal(err)
	}
	if c.Model != "mid-tight" || c.Tier != "mid" || c.Reason != "ranked" || c.ContextFiltered {
		t.Fatalf("a fitting same-tier model must win: %+v", c)
	}
	in.EstTokens = 500_000
	c, err = Choose(in)
	if err != nil {
		t.Fatal(err)
	}
	if c.Model != "top-roomy" || c.Tier != "top" || c.Thinking != "high" || c.Reason != "tier-raised" || !c.ContextFiltered {
		t.Fatalf("a tier with no fitting model must escalate: %+v", c)
	}
}

func TestChooseOverflowTakesLargestEligibleWindow(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"flash-huge": withWindow(mk("flash", 0, s("arena-coding", "", 1700, 5)), 4_000_000),
		"mid-small":  withWindow(mk("mid", 1, s("arena-coding", "", 1600, 5)), 200_000),
		"mid-big":    withWindow(mk("mid", 2, s("arena-coding", "", 1000, 5)), 900_000),
		"top-middle": withWindow(mk("top", 50, s("arena-coding", "", 1500, 5)), 500_000),
	})
	c, err := Choose(Input{Table: tb, Category: "backend", Difficulty: Routine, EstTokens: 5_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if c.Model != "mid-big" || c.Tier != "mid" || c.Thinking != "high" {
		t.Fatalf("overflow must take the largest window at or above the tier floor: %+v", c)
	}
	if c.Reason != "context_overflow_risk" || !c.ContextFiltered {
		t.Fatalf("overflow must be reported: %+v", c)
	}
}

func TestChooseOverflowKeepsVisionRequirement(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"blind": withWindow(mkv("mid", 1, false, nil), 900_000),
		"eyes":  withWindow(mkv("mid", 2, true, nil), 400_000),
	})
	c, err := Choose(Input{Table: tb, Category: "backend", Difficulty: Routine, HasImage: true, EstTokens: 5_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if c.Model != "eyes" || c.Reason != "context_overflow_risk" {
		t.Fatalf("overflow must still honour vision: %+v", c)
	}
}

func TestNextDropsSessionModelThatNoLongerFitsContext(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"small": withWindow(mk("mid", 1, s("arena-coding", "", 1600, 5)), 200_000),
		"roomy": withWindow(mk("mid", 5, s("arena-coding", "", 1500, 5)), 1_000_000),
	})
	prev := State{Difficulty: Routine, Model: "small", Thinking: "high", Tier: "mid"}
	in := Input{Table: tb, Category: "backend", Difficulty: Routine, EstTokens: 500_000}
	for _, jevOK := range []bool{true, false} {
		d, err := Next(in, prev, jevOK)
		if err != nil {
			t.Fatal(err)
		}
		if d.Model != "roomy" || d.Tier != "mid" || d.Thinking != "high" || !d.ContextFiltered {
			t.Fatalf("session model that no longer fits must be replaced (jevOK=%v): %+v", jevOK, d)
		}
		if d.State.Model != "roomy" {
			t.Fatalf("state must follow the replacement (jevOK=%v): %+v", jevOK, d.State)
		}
	}
}

func TestNextEscalateThinkingRechecksContext(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"small": withWindow(mk("top", 5, s("arena-coding", "", 1500, 5)), 200_000),
		"roomy": withWindow(mk("top", 1, s("arena-coding", "", 1600, 5)), 1_000_000),
	})
	prev := State{Difficulty: Hard, Model: "small", Thinking: "xhigh", Tier: "top"}
	d, err := Next(Input{Table: tb, Category: "backend", Difficulty: Extreme, EstTokens: 500_000}, prev, true)
	if err != nil {
		t.Fatal(err)
	}
	if d.Model != "roomy" || d.Tier != "top" || d.Thinking != "max" || d.State.Difficulty != Extreme || !d.ContextFiltered {
		t.Fatalf("same-tier escalation must re-check capacity: %+v", d)
	}
}
