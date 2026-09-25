package decide

// Factors contains Jev's calibrated category probabilities.
type Factors map[string]float64

// EffortDistribution contains probabilities for effort levels 0 through 4.
type EffortDistribution map[string]float64

const categoryThreshold = 0.6

var factorOrder = [...]string{
	"touches_code",
	"frontend",
	"fix_existing",
	"judges_existing",
	"design_only",
	"many_steps",
	"transform_only",
	"exact_answer",
	"writes_tests",
}

func topFactor(f Factors) string {
	top := factorOrder[0]
	for _, name := range factorOrder[1:] {
		if f[name] > f[top] {
			top = name
		}
	}
	return top
}

// Category applies the calibrated first-match category classifier.
func Category(f Factors) string {
	const t = categoryThreshold
	transformOnly := f["transform_only"]
	exactAnswer := f["exact_answer"]
	touchesCode := f["touches_code"]
	designOnly := f["design_only"]
	judgesExisting := f["judges_existing"]
	fixExisting := f["fix_existing"]
	manySteps := f["many_steps"]
	writesTests := f["writes_tests"]

	// ponytail: T=0.6 and this order were calibrated on 39 cases (34/39 agree with Jev's direct verdict); re-run the fixtures before changing either.
	if transformOnly >= t && topFactor(f) == "transform_only" {
		return "extraction"
	}
	if exactAnswer >= 0.5 && touchesCode < t {
		return "math-data"
	}
	if designOnly >= t && designOnly >= judgesExisting {
		return "spec-design"
	}
	if fixExisting >= t && fixExisting >= judgesExisting {
		return "debugging"
	}
	if judgesExisting >= t && judgesExisting >= manySteps {
		return "review"
	}
	if writesTests >= t && manySteps < t && writesTests > touchesCode {
		return "review"
	}
	if touchesCode >= t && f["frontend"] >= t {
		return "webdev"
	}
	if manySteps >= t {
		return "agentic-terminal"
	}
	if touchesCode >= t {
		return "backend"
	}
	if judgesExisting >= t {
		return "review"
	}
	return "writing"
}

// CategoryConfidence returns the probability supporting the selected category.
func CategoryConfidence(f Factors) float64 {
	category := Category(f)
	switch category {
	case "extraction":
		return f["transform_only"]
	case "math-data":
		return min(f["exact_answer"], 1-f["touches_code"])
	case "spec-design":
		return f["design_only"]
	case "debugging":
		return f["fix_existing"]
	case "webdev":
		return min(f["touches_code"], f["frontend"])
	case "agentic-terminal":
		return f["many_steps"]
	case "backend":
		return f["touches_code"]
	case "review":
		if f["judges_existing"] >= categoryThreshold && f["judges_existing"] >= f["many_steps"] {
			return f["judges_existing"]
		}
		if f["writes_tests"] >= categoryThreshold && f["many_steps"] < categoryThreshold && f["writes_tests"] > f["touches_code"] {
			return min(f["writes_tests"], 1-f["many_steps"])
		}
		return f["judges_existing"]
	default:
		maxFactor := 0.0
		for _, name := range factorOrder {
			if value := f[name]; value > maxFactor {
				maxFactor = value
			}
		}
		return 1 - maxFactor
	}
}

// EffortMean computes the expected effort level.
func EffortMean(p EffortDistribution) float64 {
	return p["1"] + 2*p["2"] + 3*p["3"] + 4*p["4"]
}

// Difficulty maps expected effort to the calibrated difficulty label.
func Difficulty(p EffortDistribution) string {
	label, _ := difficultyLabel(p)
	return label
}

// difficultyLabel returns the final label and whether a one-step bump applied.
func difficultyLabel(p EffortDistribution) (string, bool) {
	mean := EffortMean(p)
	// ponytail: cuts and .35 bump threshold calibrated on 21 fixtures plus the RLS journal case.
	var label string
	switch {
	case mean < 0.5:
		label = Trivial
	case mean < 2:
		label = Routine
	case mean < 3.1:
		label = Hard
	default:
		label = Extreme
	}

	switch label {
	case Trivial:
		if p["1"] >= 0.35 {
			return Routine, true
		}
	case Routine:
		if p["3"] >= 0.35 {
			return Hard, true
		}
	case Hard:
		if p["4"] >= 0.35 {
			return Extreme, true
		}
	}
	return label, false
}

// DifficultyConfidence returns the probability mass supporting the label.
func DifficultyConfidence(p EffortDistribution) float64 {
	switch label, _ := difficultyLabel(p); label {
	case Trivial:
		return p["0"]
	case Routine:
		return p["1"] + p["2"]
	case Hard:
		return p["2"] + p["3"]
	default:
		return p["3"] + p["4"]
	}
}
