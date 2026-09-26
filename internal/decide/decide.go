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
	EstTokens  int
	Available  func(model string) bool
	Exclude    func(model string) bool
}

type Choice struct {
	Model, Tier, Thinking, Benchmark string
	Score                            float64
	Reason                           string
	ContextFiltered                  bool
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
	return chooseAtOrAbove(in, tier, thinking)
}

func chooseAtOrAbove(in Input, startTier, thinking string) (Choice, error) {
	tier := startTier
	reason := "ranked"
	contextFiltered := false
	overflowModel := ""
	overflowWindow := 0
	for {
		all := candidates(in, tier)
		for _, id := range all {
			window := in.Table.Models[id].ContextWindow
			if window > overflowWindow || (window == overflowWindow && window > 0 && (overflowModel == "" || id < overflowModel)) {
				overflowModel, overflowWindow = id, window
			}
		}
		fitting, filtered := filterContextCandidates(in, all)
		contextFiltered = contextFiltered || filtered
		if len(fitting) > 0 {
			if in.Category == "extraction" {
				return Choice{Model: cheapest(in.Table, fitting), Tier: tier, Thinking: thinking, Reason: fallbackReason(reason), ContextFiltered: contextFiltered}, nil
			}
			for _, benchmark := range BenchmarksFor(in.Category) {
				ranked := make([]scored, 0, len(fitting))
				for _, id := range fitting {
					model := in.Table.Models[id]
					score, ok := scoreAt(model.Scores[benchmark], thinking)
					if !ok {
						continue
					}
					ranked = append(ranked, scored{id: id, score: score, cost: model.Cost.Input + model.Cost.Output})
				}
				if len(ranked) == 0 {
					continue
				}
				winner := bestScored(ranked)
				return Choice{Model: winner.id, Tier: tier, Thinking: thinking, Benchmark: benchmark, Score: winner.score.Value, Reason: reason, ContextFiltered: contextFiltered}, nil
			}
			return Choice{Model: cheapest(in.Table, fitting), Tier: tier, Thinking: thinking, Reason: fallbackReason(reason), ContextFiltered: contextFiltered}, nil
		}

		next, ok := tierUp[tier]
		if !ok {
			if contextFiltered {
				if overflowModel != "" {
					return Choice{Model: overflowModel, Tier: in.Table.Models[overflowModel].Tier, Thinking: thinking, Reason: "context_overflow_risk", ContextFiltered: true}, nil
				}
			}
			return Choice{}, errors.New("no candidate in any tier")
		}
		tier = next
		reason = "tier-raised"
	}
}

func filterContextCandidates(in Input, ids []string) ([]string, bool) {
	if in.EstTokens <= 0 {
		return ids, false
	}
	out := ids[:0]
	filtered := false
	for _, id := range ids {
		if contextFits(in.EstTokens, in.Table.Models[id].ContextWindow) {
			out = append(out, id)
			continue
		}
		filtered = true
	}
	return out, filtered
}

func contextFits(estTokens, contextWindow int) bool {
	if estTokens <= 0 || contextWindow <= 0 {
		return true
	}
	quotient, remainder := contextWindow/10, contextWindow%10
	limit := quotient*9 + remainder*9/10
	return estTokens <= limit
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

type State struct {
	Difficulty string
	Model      string
	Thinking   string
	Tier       string
	// ErrorEpisode is the trailing tool-failure run that already raised
	// difficulty in this session, identified by the call it started with. The
	// same run never raises twice; a run that starts elsewhere can raise once.
	ErrorEpisode string
}

type Decision struct {
	Choice
	Reason string
	State  State
}

func Next(in Input, prev State, jevOK bool) (Decision, error) {
	hasPrev := prev.Model != ""
	prevTier := prev.Tier
	if prevTier == "" {
		prevTier = TierOf(prev.Difficulty)
	}
	prevThinking := prev.Thinking
	if prevThinking == "" {
		prevThinking = ThinkingOf(prev.Difficulty)
	}

	wrap := func(choice Choice, reason, difficulty string) Decision {
		if choice.Reason == "context_overflow_risk" {
			reason = choice.Reason
		}
		state := State{Difficulty: difficulty, Model: choice.Model, Thinking: choice.Thinking, Tier: choice.Tier}
		return Decision{Choice: choice, Reason: reason, State: state}
	}
	keep := func(reason string) Decision {
		choice := Choice{Model: prev.Model, Tier: prevTier, Thinking: prevThinking}
		state := prev
		state.Tier = prevTier
		state.Thinking = prevThinking
		return Decision{Choice: choice, Reason: reason, State: state}
	}

	modelGone := false
	contextGone := false
	if hasPrev && in.Table != nil {
		model, present := in.Table.Models[prev.Model]
		modelGone = !present || (in.Exclude != nil && in.Exclude(prev.Model)) || (in.HasImage && !model.Vision)
		contextGone = present && !contextFits(in.EstTokens, model.ContextWindow)
	}
	if hasPrev && (modelGone || contextGone || (in.Available != nil && !in.Available(prev.Model))) {
		difficulty := prev.Difficulty
		if Rank(in.Difficulty) > Rank(prev.Difficulty) {
			difficulty = in.Difficulty
		}
		tier := maxTier(prevTier, TierOf(difficulty))
		thinking := ThinkingOf(difficulty)
		if effortRank[prevThinking] > effortRank[thinking] {
			thinking = prevThinking
		}
		choice, err := chooseAtOrAbove(in, tier, thinking)
		if err != nil {
			return Decision{}, err
		}
		reason := "fallback"
		if modelGone {
			reason = "model-gone"
		}
		decision := wrap(choice, reason, difficulty)
		decision.ContextFiltered = decision.ContextFiltered || contextGone
		return decision, nil
	}

	if !jevOK {
		if hasPrev {
			return keep("jev-unavailable"), nil
		}
		in.Difficulty, in.Category = Routine, ""
		choice, err := Choose(in)
		if err != nil {
			return Decision{}, err
		}
		return wrap(choice, "jev-unavailable", Routine), nil
	}
	if !hasPrev {
		choice, err := Choose(in)
		if err != nil {
			return Decision{}, err
		}
		return wrap(choice, "new", in.Difficulty), nil
	}

	if Rank(in.Difficulty) <= Rank(prev.Difficulty) {
		return keep("keep"), nil
	}

	targetTier := maxTier(prevTier, TierOf(in.Difficulty))
	if targetTier == prevTier {
		thinking := ThinkingOf(in.Difficulty)
		if effortRank[prevThinking] > effortRank[thinking] {
			thinking = prevThinking
		}
		decision := keep("escalate-thinking")
		decision.Thinking = thinking
		decision.State.Thinking = decision.Thinking
		decision.State.Difficulty = in.Difficulty
		return decision, nil
	}

	choice, err := Choose(in)
	if err != nil {
		return Decision{}, err
	}
	return wrap(choice, "escalate-tier", in.Difficulty), nil
}

func maxTier(a, b string) string {
	if tierRank(a) >= tierRank(b) {
		return a
	}
	return b
}

func tierRank(tier string) int {
	switch tier {
	case "flash":
		return 0
	case "mid":
		return 1
	case "top":
		return 2
	default:
		return -1
	}
}
