package decide

import (
	"errors"
	"sort"

	"github.com/chloeassistant/cpa-plugin-auto-router/internal/table"
)

const (
	Trivial = "trivial"
	Routine = "routine"
	Hard    = "hard"
	Extreme = "extreme"
)

var Difficulties = []string{Trivial, Routine, Hard, Extreme}

var Categories = []string{
	"webdev",
	"backend",
	"agentic-terminal",
	"debugging",
	"review",
	"spec-design",
	"writing",
	"extraction",
	"math-data",
}

var benchByCategory = map[string][]string{
	"webdev":           {"arena-webdev", "arena-coding"},
	"backend":          {"swe-bench-pro-v2", "swe-atlas-refactoring", "arena-coding"},
	"agentic-terminal": {"terminal-bench-4", "arena-agent", "aa-coding-agent-index"},
	"debugging":        {"swe-atlas-qna", "terminal-bench-4-software", "terminal-bench-4"},
	"review":           {"swe-atlas-qna", "swe-atlas-test-writing", "terminal-bench-4-security", "arena-coding"},
	"spec-design":      {"arena-hard-prompts", "aa-intelligence-index", "gdpval-aa"},
	"writing":          {"arena-creative-writing", "arena-instruction-following"},
	"extraction":       {},
	"math-data":        {"gpqa-diamond", "arena-math"},
}

var generalFallback = []string{"arena-overall", "aa-intelligence-index"}

var effortRank = map[string]int{
	"low":    0,
	"medium": 1,
	"high":   2,
	"xhigh":  3,
	"max":    4,
}

var tierUp = map[string]string{
	"flash": "mid",
	"mid":   "top",
}

func Rank(difficulty string) int {
	for rank, value := range Difficulties {
		if value == difficulty {
			return rank
		}
	}
	return -1
}

func TierOf(difficulty string) string {
	switch difficulty {
	case Trivial:
		return "flash"
	case Routine:
		return "mid"
	case Hard, Extreme:
		return "top"
	default:
		return ""
	}
}

func ThinkingOf(difficulty string) string {
	switch difficulty {
	case Trivial:
		return "low"
	case Routine:
		return "high"
	case Hard:
		return "xhigh"
	case Extreme:
		return "max"
	default:
		return ""
	}
}

// BenchmarksFor returns the category benchmarks followed by the general fallback list.
func BenchmarksFor(category string) []string {
	benchmarks, ok := benchByCategory[category]
	if !ok {
		return append([]string(nil), generalFallback...)
	}
	out := make([]string, 0, len(benchmarks)+len(generalFallback))
	out = append(out, benchmarks...)
	out = append(out, generalFallback...)
	return out
}

type Input struct {
	Table      *table.Table
	Category   string
	Difficulty string
	HasImage   bool
	Available  func(model string) bool
	Exclude    func(model string) bool
}

type Choice struct {
	Model, Tier, Thinking, Benchmark string
	Score                            float64
	Reason                           string
}

// scoreAt returns the score usable at effort: exact or closest below; an empty
// effort matches every requested effort but is less specific than a declared effort.
func scoreAt(scores []table.Score, effort string) (table.Score, bool) {
	want, ok := effortRank[effort]
	if !ok {
		return table.Score{}, false
	}
	var best table.Score
	bestRank := -2
	found := false
	for _, score := range scores {
		rank := -1
		if score.Effort != "" {
			var known bool
			rank, known = effortRank[score.Effort]
			if !known || rank > want {
				continue
			}
		}
		if !found || rank > bestRank || (rank == bestRank && score.Value > best.Value) {
			best = score
			bestRank = rank
			found = true
		}
	}
	return best, found
}

type scored struct {
	id    string
	score table.Score
	cost  float64
}

func Choose(in Input) (Choice, error) {
	if in.Table == nil {
		return Choice{}, errors.New("nil decision table")
	}
	tier := TierOf(in.Difficulty)
	thinking := ThinkingOf(in.Difficulty)
	if tier == "" || thinking == "" {
		return Choice{}, errors.New("unknown difficulty")
	}
	reason := "ranked"
	for {
		candidates := candidates(in, tier)
		if len(candidates) == 0 {
			next, ok := tierUp[tier]
			if !ok {
				return Choice{}, errors.New("no candidate in any tier")
			}
			tier = next
			reason = "tier-raised"
			continue
		}

		if in.Category == "extraction" {
			return Choice{
				Model:    cheapest(in.Table, candidates),
				Tier:     tier,
				Thinking: thinking,
				Reason:   fallbackReason(reason),
			}, nil
		}

		for _, benchmark := range BenchmarksFor(in.Category) {
			ranked := make([]scored, 0, len(candidates))
			for _, id := range candidates {
				model := in.Table.Models[id]
				score, ok := scoreAt(model.Scores[benchmark], thinking)
				if !ok {
					continue
				}
				ranked = append(ranked, scored{
					id:    id,
					score: score,
					cost:  model.Cost.Input + model.Cost.Output,
				})
			}
			if len(ranked) == 0 {
				continue
			}
			winner := bestScored(ranked)
			return Choice{
				Model:     winner.id,
				Tier:      tier,
				Thinking:  thinking,
				Benchmark: benchmark,
				Score:     winner.score.Value,
				Reason:    reason,
			}, nil
		}

		return Choice{
			Model:    cheapest(in.Table, candidates),
			Tier:     tier,
			Thinking: thinking,
			Reason:   fallbackReason(reason),
		}, nil
	}
}

func fallbackReason(reason string) string {
	if reason == "tier-raised" {
		return reason
	}
	return "fallback-unscored"
}

func bestScored(ranked []scored) scored {
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score.Value != ranked[j].score.Value {
			return ranked[i].score.Value > ranked[j].score.Value
		}
		return ranked[i].id < ranked[j].id
	})
	anchor := ranked[0]
	winner := anchor
	for _, candidate := range ranked[1:] {
		margin := anchor.score.Margin
		if candidate.score.Margin > margin {
			margin = candidate.score.Margin
		}
		if anchor.score.Value-candidate.score.Value > margin {
			continue
		}
		if candidate.cost < winner.cost || (candidate.cost == winner.cost && candidate.id < winner.id) {
			winner = candidate
		}
	}
	return winner
}

func cheapest(tb *table.Table, ids []string) string {
	best := ids[0]
	bestCost := tb.Models[best].Cost.Input + tb.Models[best].Cost.Output
	for _, id := range ids[1:] {
		cost := tb.Models[id].Cost.Input + tb.Models[id].Cost.Output
		if cost < bestCost || (cost == bestCost && id < best) {
			best, bestCost = id, cost
		}
	}
	return best
}

func candidates(in Input, tier string) []string {
	out := make([]string, 0)
	for id, model := range in.Table.Models {
		if model.Tier != tier {
			continue
		}
		if in.HasImage && !model.Vision {
			continue
		}
		if in.Available != nil && !in.Available(id) {
			continue
		}
		if in.Exclude != nil && in.Exclude(id) {
			continue
		}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
