package table

import (
	"os"
	"testing"
	"time"
)

func TestLoadGood(t *testing.T) {
	tb, err := Load("testdata/good.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if tb.Models["gpt-6-astra"].Tier != "top" {
		t.Fatalf("tier = %q", tb.Models["gpt-6-astra"].Tier)
	}
	if got := tb.Models["gpt-6-astra"].Scores["terminal-bench-4"][0].Value; got != 58.18 {
		t.Fatalf("value = %v", got)
	}
}

func TestLoadContextWindow(t *testing.T) {
	tb, err := Load("testdata/good.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if got := tb.Models["gpt-6-astra"].ContextWindow; got != 400000 {
		t.Fatalf("gpt-6-astra context window = %d, want 400000", got)
	}
	if got := tb.Models["mimo-v2.6-flash"].ContextWindow; got != 0 {
		t.Fatalf("omitted context window = %d, want 0 for unlimited", got)
	}
}
func TestLoadAllowsZeroMargin(t *testing.T) {
	p := t.TempDir() + "/models.yaml"
	const contents = `benchmarks: {bench: {source: s, unit: pct}}
models:
  m:
    tier: flash
    scores:
      bench: [{effort: low, value: 1, margin: 0, date: 2026-09-24}]
`
	if err := os.WriteFile(p, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err != nil {
		t.Fatalf("zero margin must be accepted: %v", err)
	}
}

func TestLoadRejectsInvalidNumericScores(t *testing.T) {
	cases := []struct {
		name  string
		score string
	}{
		{name: "missing-value", score: "{effort: low, margin: 0, date: 2026-09-24}"},
		{name: "null-value", score: "{effort: low, value: null, margin: 0, date: 2026-09-24}"},
		{name: "nan-value", score: "{effort: low, value: .nan, margin: 0, date: 2026-09-24}"},
		{name: "positive-infinity-value", score: "{effort: low, value: .inf, margin: 0, date: 2026-09-24}"},
		{name: "negative-infinity-value", score: "{effort: low, value: -.inf, margin: 0, date: 2026-09-24}"},
		{name: "missing-margin", score: "{effort: low, value: 1, date: 2026-09-24}"},
		{name: "null-margin", score: "{effort: low, value: 1, margin: null, date: 2026-09-24}"},
		{name: "nan-margin", score: "{effort: low, value: 1, margin: .nan, date: 2026-09-24}"},
		{name: "positive-infinity-margin", score: "{effort: low, value: 1, margin: .inf, date: 2026-09-24}"},
		{name: "negative-infinity-margin", score: "{effort: low, value: 1, margin: -.inf, date: 2026-09-24}"},
		{name: "negative-margin", score: "{effort: low, value: 1, margin: -1, date: 2026-09-24}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := t.TempDir() + "/models.yaml"
			if err := os.WriteFile(p, []byte(numericScoreTable(tc.score)), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(p); err == nil {
				t.Fatal("expected invalid numeric score to be rejected")
			}
		})
	}
}

func TestWatchKeepsOldOnInvalidNumericReload(t *testing.T) {
	cases := []struct {
		name  string
		score string
	}{
		{name: "missing-value", score: "{effort: low, margin: 0, date: 2026-09-24}"},
		{name: "null-value", score: "{effort: low, value: null, margin: 0, date: 2026-09-24}"},
		{name: "nan-value", score: "{effort: low, value: .nan, margin: 0, date: 2026-09-24}"},
		{name: "positive-infinity-value", score: "{effort: low, value: .inf, margin: 0, date: 2026-09-24}"},
		{name: "negative-infinity-value", score: "{effort: low, value: -.inf, margin: 0, date: 2026-09-24}"},
		{name: "missing-margin", score: "{effort: low, value: 1, date: 2026-09-24}"},
		{name: "null-margin", score: "{effort: low, value: 1, margin: null, date: 2026-09-24}"},
		{name: "nan-margin", score: "{effort: low, value: 1, margin: .nan, date: 2026-09-24}"},
		{name: "positive-infinity-margin", score: "{effort: low, value: 1, margin: .inf, date: 2026-09-24}"},
		{name: "negative-infinity-margin", score: "{effort: low, value: 1, margin: -.inf, date: 2026-09-24}"},
		{name: "negative-margin", score: "{effort: low, value: 1, margin: -1, date: 2026-09-24}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := dir + "/models.yaml"
			good := numericScoreTable("{effort: low, value: 42, margin: 0, date: 2026-09-24}")
			if err := os.WriteFile(p, []byte(good), 0o644); err != nil {
				t.Fatal(err)
			}
			w, err := Watch(p)
			if err != nil {
				t.Fatal(err)
			}
			first := w.Get()
			if err := os.WriteFile(p, []byte(numericScoreTable(tc.score)), 0o644); err != nil {
				t.Fatal(err)
			}
			changed := time.Now().Add(time.Hour)
			if err := os.Chtimes(p, changed, changed); err != nil {
				t.Fatal(err)
			}
			if got := w.Get(); got != first {
				t.Fatal("invalid reload replaced previous valid table")
			}
			if got := first.Models["m"].Scores["bench"][0].Value; got != 42 {
				t.Fatalf("previous value = %v, want 42", got)
			}
		})
	}
}

func numericScoreTable(score string) string {
	return "benchmarks: {bench: {source: s, unit: pct}}\nmodels:\n  m:\n    tier: flash\n    scores:\n      bench: [" + score + "]\n"
}

func TestLoadRejects(t *testing.T) {
	for _, f := range []string{
		"bad-tier",
		"bad-nodate",
		"bad-date",
		"bad-missing-margin",
		"bad-negative-margin",
		"bad-unknown-bench",
		"bad-effort",
		"bad-empty",
	} {
		t.Run(f, func(t *testing.T) {
			if _, err := Load("testdata/" + f + ".yaml"); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestWatchKeepsOldOnBadReload(t *testing.T) {
	dir := t.TempDir()
	p := dir + "/models.yaml"
	good, err := os.ReadFile("testdata/good.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, good, 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := Watch(p)
	if err != nil {
		t.Fatal(err)
	}
	first := w.Get()
	time.Sleep(1100 * time.Millisecond)
	if err := os.WriteFile(p, []byte("models: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if w.Get() != first {
		t.Fatal("bad reload must keep previous table")
	}
}

func TestLoadGenerated(t *testing.T) {
	if _, err := Load("testdata/generated.yaml"); err != nil {
		t.Fatalf("generated table: %v", err)
	}
}
