package table

import (
	"fmt"
	"log"
	"math"
	"os"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type Score struct {
	Effort string  `yaml:"effort"`
	Value  float64 `yaml:"value"`
	Margin float64 `yaml:"margin"`
	Date   string  `yaml:"date"`
	Note   string  `yaml:"note,omitempty"`

	valueSet  bool
	marginSet bool
}

func (s *Score) UnmarshalYAML(node *yaml.Node) error {
	var raw struct {
		Effort *string  `yaml:"effort"`
		Value  *float64 `yaml:"value"`
		Margin *float64 `yaml:"margin"`
		Date   *string  `yaml:"date"`
		Note   string   `yaml:"note,omitempty"`
	}
	if err := node.Decode(&raw); err != nil {
		return err
	}
	s.Effort = ""
	if raw.Effort != nil {
		s.Effort = *raw.Effort
	}
	s.Value = 0
	s.valueSet = raw.Value != nil
	if raw.Value != nil {
		s.Value = *raw.Value
	}
	s.Margin = 0
	s.marginSet = raw.Margin != nil
	if raw.Margin != nil {
		s.Margin = *raw.Margin
	}
	s.Date = ""
	if raw.Date != nil {
		s.Date = *raw.Date
	}
	s.Note = raw.Note
	return nil
}

type Model struct {
	Tier          string                          `yaml:"tier"`
	Vision        bool                            `yaml:"vision"`
	ContextWindow int                             `yaml:"context_window"`
	Cost          struct{ Input, Output float64 } `yaml:"cost"`
	Scores        map[string][]Score              `yaml:"scores"`
}

type Table struct {
	GeneratedAt string                                   `yaml:"generated_at"`
	Benchmarks  map[string]struct{ Source, Unit string } `yaml:"benchmarks"`
	Models      map[string]Model                         `yaml:"models"`
}

var validTiers = map[string]bool{
	"flash": true,
	"mid":   true,
	"top":   true,
}

var validEfforts = map[string]bool{
	"":       true,
	"low":    true,
	"medium": true,
	"high":   true,
	"xhigh":  true,
	"max":    true,
}

func Load(path string) (*Table, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var t Table
	if err := yaml.Unmarshal(raw, &t); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(t.Models) == 0 {
		return nil, fmt.Errorf("%s: no models", path)
	}
	for id, model := range t.Models {
		if !validTiers[model.Tier] {
			return nil, fmt.Errorf("%s: model %s: invalid tier %q", path, id, model.Tier)
		}
		if model.ContextWindow < 0 {
			return nil, fmt.Errorf("%s: model %s: negative context window %d", path, id, model.ContextWindow)
		}
		for benchmark, scores := range model.Scores {
			if _, ok := t.Benchmarks[benchmark]; !ok {
				return nil, fmt.Errorf("%s: model %s: unknown benchmark %q", path, id, benchmark)
			}
			for _, score := range scores {
				if score.Date == "" {
					return nil, fmt.Errorf("%s: model %s: score in %s has no date", path, id, benchmark)
				}
				if _, err := time.Parse("2006-01-02", score.Date); err != nil {
					return nil, fmt.Errorf("%s: model %s: score in %s has invalid date %q", path, id, benchmark, score.Date)
				}
				if !score.valueSet {
					return nil, fmt.Errorf("%s: model %s: score in %s has no value", path, id, benchmark)
				}
				if !score.marginSet {
					return nil, fmt.Errorf("%s: model %s: score in %s has no margin", path, id, benchmark)
				}
				if math.IsNaN(score.Value) || math.IsInf(score.Value, 0) {
					return nil, fmt.Errorf("%s: model %s: score in %s has non-finite value %v", path, id, benchmark, score.Value)
				}
				if math.IsNaN(score.Margin) || math.IsInf(score.Margin, 0) {
					return nil, fmt.Errorf("%s: model %s: score in %s has non-finite margin %v", path, id, benchmark, score.Margin)
				}
				if score.Margin < 0 {
					return nil, fmt.Errorf("%s: model %s: score in %s has negative margin %v", path, id, benchmark, score.Margin)
				}
				if !validEfforts[score.Effort] {
					return nil, fmt.Errorf("%s: model %s: score in %s has invalid effort %q", path, id, benchmark, score.Effort)
				}
			}
		}
	}
	return &t, nil
}

type Watched struct {
	path  string
	mu    sync.Mutex
	mtime time.Time
	cur   *Table
}

func Watch(path string) (*Watched, error) {
	t, err := Load(path)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	return &Watched{path: path, mtime: st.ModTime(), cur: t}, nil
}

// Get reloads when mtime changed; a failed reload keeps the previous table.
func (w *Watched) Get() *Table {
	w.mu.Lock()
	defer w.mu.Unlock()

	st, err := os.Stat(w.path)
	if err != nil {
		log.Printf("auto-router table stat failed path=%s: %v", w.path, err)
		return w.cur
	}
	if st.ModTime().Equal(w.mtime) {
		return w.cur
	}
	w.mtime = st.ModTime()
	t, err := Load(w.path)
	if err != nil {
		log.Printf("auto-router table reload failed path=%s: %v", w.path, err)
		return w.cur
	}
	w.cur = t
	log.Printf("auto-router table reloaded path=%s", w.path)
	return w.cur
}
