package decide

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

type composeFixtures struct {
	Category []struct {
		ID       string             `json:"id"`
		Factors  map[string]float64 `json:"factors"`
		Expected string             `json:"expected"`
	} `json:"category"`
	Difficulty []struct {
		ID       string             `json:"id"`
		EffortP  map[string]float64 `json:"effort_p"`
		Expected string             `json:"expected"`
	} `json:"difficulty"`
}

func loadComposeFixtures(t *testing.T) composeFixtures {
	t.Helper()
	data, err := os.ReadFile("testdata/jev_fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures composeFixtures
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	return fixtures
}

func TestCategoryFixtures(t *testing.T) {
	fixtures := loadComposeFixtures(t)
	if len(fixtures.Category) != 39 {
		t.Fatalf("category fixture count = %d, want 39", len(fixtures.Category))
	}
	for _, fixture := range fixtures.Category {
		fixture := fixture
		t.Run(fixture.ID, func(t *testing.T) {
			if got := Category(Factors(fixture.Factors)); got != fixture.Expected {
				t.Fatalf("Category() = %q, want %q", got, fixture.Expected)
			}
		})
	}
}

func TestDifficultyFixtures(t *testing.T) {
	fixtures := loadComposeFixtures(t)
	if len(fixtures.Difficulty) != 21 {
		t.Fatalf("difficulty fixture count = %d, want 21", len(fixtures.Difficulty))
	}
	for _, fixture := range fixtures.Difficulty {
		fixture := fixture
		t.Run(fixture.ID, func(t *testing.T) {
			p := EffortDistribution(fixture.EffortP)
			if got := Difficulty(p); got != fixture.Expected {
				t.Fatalf("Difficulty() = %q, want %q", got, fixture.Expected)
			}
			if got := EffortMean(p); math.Abs(got-meanFixture(p)) > 1e-12 {
				t.Fatalf("EffortMean() = %v, want %v", got, meanFixture(p))
			}
		})
	}
}

func meanFixture(p EffortDistribution) float64 {
	return p["1"] + 2*p["2"] + 3*p["3"] + 4*p["4"]
}

func TestCategoryThresholdsAndFirstMatch(t *testing.T) {
	tests := []struct {
		name string
		f    Factors
		want string
	}{
		{"extraction at threshold", Factors{"transform_only": .6}, "extraction"},
		{"extraction below threshold", Factors{"transform_only": .599999}, "writing"},
		{"math exact boundary", Factors{"exact_answer": .5, "touches_code": .599999}, "math-data"},
		{"math below boundary", Factors{"exact_answer": .499999, "touches_code": .1}, "writing"},
		{"spec design at threshold", Factors{"design_only": .6}, "spec-design"},
		{"debugging at threshold", Factors{"fix_existing": .6}, "debugging"},
		{"review at threshold", Factors{"judges_existing": .6}, "review"},
		{"tests review at threshold", Factors{"writes_tests": .6, "many_steps": .599999, "touches_code": .1}, "review"},
		{"webdev at threshold", Factors{"touches_code": .6, "frontend": .6}, "webdev"},
		{"terminal at threshold", Factors{"many_steps": .6}, "agentic-terminal"},
		{"backend at threshold", Factors{"touches_code": .6}, "backend"},
		{"first match extraction before math", Factors{"transform_only": .9, "exact_answer": .9}, "extraction"},
		{"first match design before debugging", Factors{"design_only": .7, "fix_existing": .9}, "spec-design"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Category(test.f); got != test.want {
				t.Fatalf("Category() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCategoryStableTopTie(t *testing.T) {
	f := Factors{"touches_code": .8, "transform_only": .8}
	for i := 0; i < 100; i++ {
		if got := Category(f); got != "backend" {
			t.Fatalf("Category() = %q on iteration %d, want backend", got, i)
		}
	}
}

func TestCategoryConfidenceSupportingEvidence(t *testing.T) {
	tests := []struct {
		name string
		f    Factors
		want float64
	}{
		{"extraction", Factors{"transform_only": .73}, .73},
		{"math uses complement", Factors{"exact_answer": .83, "touches_code": .22}, .78},
		{"spec ignores competitor", Factors{"design_only": .81, "judges_existing": .59}, .81},
		{"debugging", Factors{"fix_existing": .77, "judges_existing": .7}, .77},
		{"review judges", Factors{"judges_existing": .74, "many_steps": .7}, .74},
		{"review writes tests uses complement", Factors{"writes_tests": .88, "many_steps": .21, "touches_code": .1}, .79},
		{"webdev minimum", Factors{"touches_code": .91, "frontend": .64}, .64},
		{"terminal", Factors{"many_steps": .86}, .86},
		{"backend", Factors{"touches_code": .93}, .93},
		{"writing complement", Factors{"touches_code": .12, "frontend": .31, "exact_answer": .2}, .69},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := CategoryConfidence(test.f); math.Abs(got-test.want) > 1e-12 {
				t.Fatalf("CategoryConfidence() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestDifficultyBoundariesAndConfidenceMass(t *testing.T) {
	tests := []struct {
		name string
		p    EffortDistribution
		want string
		conf float64
	}{
		{"trivial below .5", EffortDistribution{"0": .6, "1": .4}, "trivial", .6},
		{"routine at .5", EffortDistribution{"0": .5, "1": .5}, "routine", .5},
		{"routine below 2", EffortDistribution{"0": .01, "1": .49, "2": .5}, "routine", .99},
		{"hard at 2", EffortDistribution{"0": 0, "2": 1}, "hard", 0},
		{"hard below 3.1", EffortDistribution{"3": .999, "4": .001}, "hard", .999},
		{"extreme at 3.1", EffortDistribution{"3": .9, "4": .1}, "extreme", .1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Difficulty(test.p); got != test.want {
				t.Fatalf("Difficulty() = %q, want %q", got, test.want)
			}
			if got := DifficultyConfidence(test.p); math.Abs(got-test.conf) > 1e-12 {
				t.Fatalf("DifficultyConfidence() = %v, want %v", got, test.conf)
			}
		})
	}
}
