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
