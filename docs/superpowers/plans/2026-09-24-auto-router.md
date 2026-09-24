# cpa-plugin-auto-router — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A native CLIProxyAPI plugin that exposes a virtual model `auto-router`, classifies each request with one Jev call (category + difficulty), picks the concrete model + thinking level from a local benchmark table, and executes through the host; plus a Python updater on a weekly `systemd --user` timer that rebuilds the benchmark table from aggregators, filtered by the proxy's live `/v1/models`.

**Architecture:** One Go module producing `auto-router.so` (cgo, `-buildmode=c-shared`). The decision core (`internal/decide`) and table loader (`internal/table`) are pure Go, no cgo, unit-tested with recorded cases. The cgo shell (`main.go` + `host.go`) only marshals ABI envelopes and forwards streams via `host.model.execute_stream`. The updater is a separate Python package (`updater/`) that writes `models.yaml`; the plugin never fetches leaderboards.

**Tech Stack:** Go 1.27.1 (host has it), cgo, `github.com/router-for-me/CLIProxyAPI/v7` v7.2.159 (`sdk/pluginapi`, `sdk/pluginabi` — types only), `gopkg.in/yaml.v3`, `github.com/tidwall/gjson`. Python 3.12 stdlib + `pyyaml` + `pyarrow` (via `uv run --with`). `systemd --user` on hermes-chloe.

**Spec:** `docs/superpowers/specs/2026-09-24-auto-router-design.md` — executors read both.

## Global Constraints

- **NEVER restart, reconfigure, or install into the production proxy on port 8317 (`cliproxyapi.service`, `/home/hermes/cliproxyapi/`).** The orchestrating agent, OMP and Hermes ru⟪HERMES-CONTEXT-COMPRESSION: 1,204 of 1,404 chars omitted here by Hermes's context compressor. This is NOT part of the original tool call and must never be reproduced in new output — always write full, untruncated content.⟫- Plugin ABI `1`, RPC schema `6` (`sdk/pluginabi/types.go` at tag `v7.2.159`). Go module `replace` points at the local checkout `/home/hermes/projects/CLIProxyAPI` (already at `v7.2.159`).
- Plugin binary name is `auto-router.so`; host derives plugin id `auto-router` from the filename (`internal/pluginhost/platform.go:pluginFileFromPath`). Plugin id pattern: lowercase, digits, hyphen.
- Virtual model id is exactly `auto-router`. Provider identifier returned by `executor.identifier` and `model.register` is exactly `auto-router`.
- Jev call shape: `POST {base_url}{endpoint_path}` with `{"model","state","questions"}`; answers at `answers.<name>.choice`, `.probabilities`, `.confidence`. Defaults: `base_url=https://openrouter.ai`, `endpoint_path=/api/alpha/decisions`, `model=typesafe/jev-1.13`, key env `OPENROUTER_API_KEY`. https always; plain http only for loopback/RFC1918/CGNAT.
- Confidence threshold default `0.6`. Snippet default `1500` chars. Jev timeout default `2s`.
- Thinking level per difficulty: `trivial→low`, `routine→high`, `hard→xhigh`, `extreme→max`. Tier per difficulty: `trivial→flash`, `routine→mid`, `hard→top`, `extreme→top`.
- Thinking is passed as suffix `model(level)`; the host clamps unsupported levels (`internal/thinking/validate.go`, suffix path).
- Only-escalate rule: a session's difficulty never decreases; model changes only on `escalate-tier`, `vision-swap`, or `fallback`.
- Fail-open: any router failure → default decision + log; the request always proceeds.
- No personal policy in code or tables. Capability exclusions only: `gpt-image-*`, `abliterated-*`, `codex-auto-review`.
- `passthrough-headers` is `false` in the host config: `ExecutorResponse.Headers` are filtered by `downstreamHeadersFromExecutor` and will **not** reach the client. `X-Auto-Router` is best-effort; the JSON log line is the source of truth (Task 9 verifies and documents).
- Never write a secret to disk or logs. Jev key is read from the env var named in config only.
- Commits: native git in this repo (no GitButler here), SSH-signed (`commit.gpgsign=true` already set), one commit per task.
- Python runs as `uv run --with pyyaml --with pyarrow --python 3.12 python -m updater ...`; no global installs.

---

## File Structure

```
cpa-plugin-auto-router/
├── go.mod, go.sum                       module github.com/chloeassistant/cpa-plugin-auto-router
├── Makefile                             build .so, test, install to ~/cliproxyapi/plugins
├── main.go                              cgo exports + method dispatch (thin)
├── host.go                              callHost, envelopes, host.log, host.model.* wrappers
├── plugin.go                            config, registration, routeModel, execute, executeStream
├── internal/table/table.go              models.yaml + tiers.yaml loader + validation + mtime reload
├── internal/table/table_test.go
├── internal/decide/decide.go            pure decision core (choose + escalation)
├── internal/decide/decide_test.go
├── internal/jev/client.go               Jev HTTP client (URL policy, timeout, parse)
├── internal/jev/client_test.go
├── internal/session/session.go          TTL map keyed by session id, session-id extraction
├── internal/session/session_test.go
├── internal/snippet/snippet.go          last-user-message snippet + local signals per protocol
├── internal/snippet/snippet_test.go
├── table/tiers.yaml                     manual tiers (operator-owned)
├── table/models.yaml                    generated by updater (committed seed)
├── updater/__init__.py, __main__.py     CLI: --catalog --out --tiers --openrouter-key-env
├── updater/sources.py                   eee, openrouter, epoch, arena, modelsdev readers
├── updater/aliases.py                   explicit per-source name → catalog id maps
├── updater/merge.py                     merge rules (date ≥, keep old on source failure), validate, atomic write
├── updater/test_updater.py              fixture-driven tests
├── updater/testdata/                    recorded fixtures (small)
├── systemd/cpa-auto-router-update.service, .timer
└── docs/superpowers/{specs,plans}/
```

---

### Task 0: Empirical checks (ABI load, thinking suffix, session signal, stream passthrough)

**Files:**
- Create: `docs/superpowers/plans/2026-09-24-checks.md` (results only)

**Interfaces:**
- Produces: confirmed facts that Tasks 6–9 rely on; if any check fails, stop and report — do not proceed to Task 6.

- [x] **Step 1: Build the official example against the local checkout and load it in the running proxy**

```bash
cd /home/hermes/projects/CLIProxyAPI/examples/plugin/claude-web-search-router/go
go build -buildmode=c-shared -o /home/hermes/.hermes/cache/scratch/claude-web-search-router.so .
cp /home/hermes/.hermes/cache/scratch/claude-web-search-router.so /home/hermes/cliproxyapi-test/plugins/
```

Edit `/home/hermes/cliproxyapi-test/config.yaml` (TEST instance, never the 8317 one) — under `plugins.configs` add:

```yaml
    claude-web-search-router:
      enabled: false
```

Then: `XDG_RUNTIME_DIR=/run/user/1000 systemctl --user restart cliproxyapi-test && sleep 4 && journalctl --user -u cliproxyapi-test -n 60 --no-pager | grep -i plugin`

Expected: a line showing the plugin loaded (id `claude-web-search-router`, abi 1, schema 6) and no `abi mismatch` / `schema` error. Record the exact lines in the checks file.

- [x] **Step 2: Confirm the thinking suffix through the normal path is clamped, not rejected**

```bash
KEY=$(cat /home/hermes/cliproxyapi-test/api-key)
curl -s http://127.0.0.1:8318/v1/chat/completions -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"kimi-k3(max)","messages":[{"role":"user","content":"Say ok."}],"max_tokens":20}' | head -c 400
```

Expected: HTTP 200 with a completion (Kimi has no `max`; the host clamps to `high`). A 400 mentioning thinking means the suffix path is strict — record it; the plugin would then send `high` for models whose `levels` (models.dev) lack the requested level (small change in Task 4, `Thinking()`).

- [x] **Step 3: Record which session signal OMP and Hermes send**

```bash
grep -n -i "prompt_cache_key\|x-session\|session-id\|X-Session-Affinity" /home/hermes/.hermes/hermes-agent/agent/auxiliary_client.py | head
grep -rn -i "prompt_cache_key\|x-session-id\|x-session-affinity" /home/hermes/projects/oh-my-pi/packages/*/src --include=*.ts -l 2>/dev/null | head
```

Expected: at least one of `prompt_cache_key` (Responses) or `X-Session-ID`/`X-Session-Affinity` per client. Record which. If neither, the plugin falls back to the first-user-message hash (Task 5) — acceptable, note it.

- [x] **Step 4: Confirm host.model.execute_stream preserves Responses SSE and that executor headers are filtered**

Read `sdk/api/handlers/handlers_execution.go:223-226` and `handlers_interceptors.go:289-294` in the checkout: `downstreamHeadersFromExecutor(raw, PassthroughHeadersEnabled(cfg))` returns `nil` when passthrough is off. Record: "X-Auto-Router reaches the client only if `passthrough-headers: true`; default off; log line is authoritative." The SSE passthrough itself is exercised end-to-end in Task 9 (smoke); no separate check.

- [x] **Step 5: Revert the example plugin, commit the checks file**

```bash
rm /home/hermes/cliproxyapi-test/plugins/claude-web-search-router.so
# remove the claude-web-search-router entry from /home/hermes/cliproxyapi-test/config.yaml plugins.configs
XDG_RUNTIME_DIR=/run/user/1000 systemctl --user restart cliproxyapi-test
cd /home/hermes/projects/cpa-plugin-auto-router && git add docs/superpowers/plans/2026-09-24-checks.md && git commit -m "docs: record empirical checks (ABI load, thinking clamp, session signals, header filter)"
```

---

### Task 1: Module skeleton, Makefile, tiers.yaml

**Files:**
- Create: `go.mod`, `Makefile`, `table/tiers.yaml`, `.gitignore`

**Interfaces:**
- Produces: module path `github.com/chloeassistant/cpa-plugin-auto-router`; `make build` → `bin/auto-router.so`; `make test`; `make install` copies to `/home/hermes/cliproxyapi/plugins/auto-router.so`.

- [x] **Step 1: go.mod with local replace**

```
module github.com/chloeassistant/cpa-plugin-auto-router

go 1.27

require (
	github.com/router-for-me/CLIProxyAPI/v7 v7.0.0
	github.com/tidwall/gjson v1.18.0
	gopkg.in/yaml.v3 v3.0.1
)

replace github.com/router-for-me/CLIProxyAPI/v7 => /home/hermes/projects/CLIProxyAPI
```

Run `go mod tidy` (needs a `.go` file; create `doc.go` with `package main` and a one-line comment first).

- [x] **Step 2: Makefile**

```make
BIN_DIR := $(CURDIR)/bin
PLUGIN := $(BIN_DIR)/auto-router.so
INSTALL_DIR := /home/hermes/cliproxyapi/plugins
TEST_INSTALL_DIR := /home/hermes/cliproxyapi-test/plugins

.PHONY: build test install clean
build: $(PLUGIN)
$(PLUGIN): $(shell find . -name '*.go' -not -path './updater/*') go.mod
	mkdir -p $(BIN_DIR)
	go build -buildmode=c-shared -o $(PLUGIN) .
	rm -f $(BIN_DIR)/auto-router.h
test:
	go test ./...
	cd updater && uv run --with pyyaml --with pyarrow --python 3.12 python -m pytest -q
install: build
	mkdir -p $(INSTALL_DIR)
	install -m 0644 $(PLUGIN) $(INSTALL_DIR)/auto-router.so
install-test: build
	mkdir -p $(TEST_INSTALL_DIR)
	install -m 0644 $(PLUGIN) $(TEST_INSTALL_DIR)/auto-router.so
clean:
	rm -rf $(BIN_DIR)
```

- [x] **Step 3: tiers.yaml (operator-owned; the updater never writes it)**

```yaml
# Tier per catalog model id (exact id from GET /v1/models). Models in the
# proxy but absent here are WARNed by the updater and never routed.
flash: [glm-5.3-flash, deepseek-v4-flash, qwen-3.8-flash-next, mimo-v2.6-flash]
mid:   [gpt-5.6-luna, gpt-6-luna, gpt-5.6-terra, glm-5.3, kimi-k3, claude-sonnet-5,
        deepseek-v4-pro, qwen-3.8-max, minimax-m3]
top:   [gpt-6-astra, gpt-6-sol, gpt-5.6-sol, claude-opus-5, claude-opus-5-5,
        claude-fable-5, claude-fable-5-1]
```

- [x] **Step 4: .gitignore and commit**

```
bin/
updater/__pycache__/
updater/.pytest_cache/
```

```bash
git add go.mod go.sum doc.go Makefile table/tiers.yaml .gitignore
git commit -m "chore: module skeleton, Makefile, operator tiers"
```

---

### Task 2: Table loader (`internal/table`)

**Files:**
- Create: `internal/table/table.go`, `internal/table/table_test.go`, `internal/table/testdata/good.yaml`, `internal/table/testdata/bad-*.yaml`

**Interfaces:**
- Produces:

```go
package table

type Score struct {
	Effort string  `yaml:"effort"` // "", "low", "medium", "high", "xhigh", "max"
	Value  float64 `yaml:"value"`
	Margin float64 `yaml:"margin"`
	Date   string  `yaml:"date"`   // YYYY-MM-DD, required
	Note   string  `yaml:"note,omitempty"`
}
type Model struct {
	Tier   string             `yaml:"tier"`   // flash|mid|top
	Vision bool               `yaml:"vision"`
	Cost   struct{ Input, Output float64 } `yaml:"cost"`
	Scores map[string][]Score `yaml:"scores"` // benchmark id → scores
}
type Table struct {
	GeneratedAt string                       `yaml:"generated_at"`
	Benchmarks  map[string]struct{ Source, Unit string } `yaml:"benchmarks"`
	Models      map[string]Model             `yaml:"models"`
}
func Load(path string) (*Table, error)          // validates; error = whole file rejected
type Watched struct { /* path, mtime, current *Table */ }
func Watch(path string) (*Watched, error)        // initial Load must succeed
func (w *Watched) Get() *Table                   // stat; reload on mtime change; keep old on error
```

Validation rules (each is a test): unknown tier → error; score with empty `Date` or `Margin < 0` → error; score referencing a benchmark not in `Benchmarks` → error; effort outside the allowed set → error; `Models` empty → error.

- [x] **Step 1: Write failing tests**

```go
package table

import "testing"

func TestLoadGood(t *testing.T) {
	tb, err := Load("testdata/good.yaml")
	if err != nil { t.Fatal(err) }
	if tb.Models["gpt-6-astra"].Tier != "top" { t.Fatalf("tier = %q", tb.Models["gpt-6-astra"].Tier) }
	if got := tb.Models["gpt-6-astra"].Scores["terminal-bench-4"][0].Value; got != 58.18 { t.Fatalf("value = %v", got) }
}

func TestLoadRejects(t *testing.T) {
	for _, f := range []string{"bad-tier", "bad-nodate", "bad-unknown-bench", "bad-effort", "bad-empty"} {
		if _, err := Load("testdata/" + f + ".yaml"); err == nil {
			t.Errorf("%s: expected error", f)
		}
	}
}

func TestWatchKeepsOldOnBadReload(t *testing.T) {
	dir := t.TempDir()
	p := dir + "/models.yaml"
	good, _ := os.ReadFile("testdata/good.yaml")
	os.WriteFile(p, good, 0o644)
	w, err := Watch(p)
	if err != nil { t.Fatal(err) }
	first := w.Get()
	time.Sleep(1100 * time.Millisecond) // mtime granularity
	os.WriteFile(p, []byte("models: {}\n"), 0o644)
	if w.Get() != first { t.Fatal("bad reload must keep previous table") }
}
```

`testdata/good.yaml` is the spec's example table verbatim (section "Formato da tabela"). Each `bad-*.yaml` is `good.yaml` with one field broken (tier `ultra`; date removed; score under `nonexistent-bench`; effort `insane`; `models: {}`).

- [x] **Step 2: Run, expect compile failure** — `go test ./internal/table/` → `undefined: Load`.

- [x] **Step 3: Implement**

```go
package table

import (
	"fmt"
	"os"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

var validTiers = map[string]bool{"flash": true, "mid": true, "top": true}
var validEfforts = map[string]bool{"": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true}

func Load(path string) (*Table, error) {
	raw, err := os.ReadFile(path)
	if err != nil { return nil, err }
	var t Table
	if err := yaml.Unmarshal(raw, &t); err != nil { return nil, fmt.Errorf("%s: %w", path, err) }
	if len(t.Models) == 0 { return nil, fmt.Errorf("%s: no models", path) }
	for id, m := range t.Models {
		if !validTiers[m.Tier] { return nil, fmt.Errorf("%s: model %s: invalid tier %q", path, id, m.Tier) }
		for bench, scores := range m.Scores {
			if _, ok := t.Benchmarks[bench]; !ok { return nil, fmt.Errorf("%s: model %s: unknown benchmark %q", path, id, bench) }
			for _, s := range scores {
				if s.Date == "" || s.Margin < 0 || !validEfforts[s.Effort] {
					return nil, fmt.Errorf("%s: model %s: bad score in %s: %+v", path, id, bench, s)
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
	if err != nil { return nil, err }
	st, _ := os.Stat(path)
	return &Watched{path: path, mtime: st.ModTime(), cur: t}, nil
}

// Get reloads when mtime changed; a failed reload keeps the previous table.
func (w *Watched) Get() *Table {
	w.mu.Lock()
	defer w.mu.Unlock()
	st, err := os.Stat(w.path)
	if err != nil || st.ModTime().Equal(w.mtime) { return w.cur }
	w.mtime = st.ModTime()
	if t, err := Load(w.path); err == nil { w.cur = t } // ponytail: error is surfaced by the caller's log, not here
	return w.cur
}
```

- [x] **Step 4: Run tests, expect PASS** — `go test ./internal/table/ -v`.

- [x] **Step 5: Commit** — `git add internal/table && git commit -m "feat(table): models.yaml loader with validation and mtime reload"`.

---

### Task 3: Decision core — choose (`internal/decide`, part 1)

**Files:**
- Create: `internal/decide/decide.go`, `internal/decide/decide_test.go`

**Interfaces:**
- Consumes: `table.Table`, `table.Model`, `table.Score`.
- Produces:

```go
package decide

const (
	Trivial = "trivial"; Routine = "routine"; Hard = "hard"; Extreme = "extreme"
)
var Difficulties = []string{Trivial, Routine, Hard, Extreme}
var Categories = []string{"webdev","backend","agentic-terminal","debugging","review","spec-design","writing","extraction","math-data"}

func Rank(d string) int                 // trivial=0 … extreme=3; unknown = -1
func TierOf(d string) string            // flash|mid|top
func ThinkingOf(d string) string        // low|high|xhigh|max

// BenchmarksFor returns the ordered benchmark ids that weigh for a category
// ("" for unknown/low-confidence category → general fallback list).
func BenchmarksFor(category string) []string

type Input struct {
	Table      *table.Table
	Category   string   // "" = unknown
	Difficulty string
	HasImage   bool
	Available  func(model string) bool // nil = all available
	Exclude    func(model string) bool // capability exclusions
}
type Choice struct {
	Model, Tier, Thinking, Benchmark string
	Score  float64 // 0 when fallback
	Reason string  // "ranked" | "fallback-unscored" | "tier-raised"
}
func Choose(in Input) (Choice, error)   // error only when no model at all
```

`BenchmarksFor` encodes the spec table (ids as written in `models.yaml`):

```go
var benchByCategory = map[string][]string{
	"webdev":           {"arena-webdev", "arena-coding"},
	"backend":          {"swe-bench-pro-v2", "swe-atlas-refactoring", "arena-coding"},
	"agentic-terminal": {"terminal-bench-4", "arena-agent", "aa-coding-agent-index"},
	"debugging":        {"swe-atlas-qna", "terminal-bench-4-software", "terminal-bench-4"},
	"review":           {"swe-atlas-qna", "swe-atlas-test-writing", "terminal-bench-4-security", "arena-coding"},
	"spec-design":      {"arena-hard-prompts", "aa-intelligence-index", "gdpval-aa"},
	"writing":          {"arena-creative-writing", "arena-instruction-following"},
	"extraction":       {}, // flash, cheapest
	"math-data":        {"gpqa-diamond", "arena-math"},
}
var generalFallback = []string{"arena-overall", "aa-intelligence-index"}
```

Score selection per model: among `Scores[bench]`, take the entry whose effort is the **closest at or below** the requested effort (order `low<medium<high<xhigh<max`); entries with `Effort==""` match any effort; no entry at or below → unscored for that benchmark.

Ranking: winner = max Value; tie if `|a-b| <= max(a.Margin, b.Margin)` → lower `Cost.Input+Cost.Output`. Try benchmarks in order until at least one candidate is scored. No scored candidate → `fallback-unscored`: cheapest candidate. No candidate in tier → raise tier (`mid→top`, `flash→mid`), `Reason="tier-raised"`; top empty → error.

- [x] **Step 1: Write failing tests** (table built in code with a helper `mk(tier string, cost float64, scores map[string][]table.Score)`)

```go
func TestChooseRankedByCategoryBenchmark(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"a": mk("top", 60, s("terminal-bench-4", "xhigh", 57.9, 2.7)),
		"b": mk("top", 12, s("terminal-bench-4", "xhigh", 37.3, 3.8)),
	})
	c, _ := Choose(Input{Table: tb, Category: "agentic-terminal", Difficulty: Hard})
	if c.Model != "a" || c.Thinking != "xhigh" || c.Benchmark != "terminal-bench-4" { t.Fatalf("%+v", c) }
}
func TestTieBreaksOnCost(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"a": mk("top", 60, s("arena-webdev", "", 1800, 16)),
		"b": mk("top", 12, s("arena-webdev", "", 1790, 14)),
	})
	c, _ := Choose(Input{Table: tb, Category: "webdev", Difficulty: Hard})
	if c.Model != "b" { t.Fatalf("tie within margin must pick cheaper: %+v", c) }
}
func TestEffortPicksClosestBelowNeverAbove(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"a": mk("top", 60, s("terminal-bench-4", "max", 99, 1), s("terminal-bench-4", "high", 50, 1)),
		"b": mk("top", 60, s("terminal-bench-4", "xhigh", 55, 1)),
	})
	c, _ := Choose(Input{Table: tb, Category: "agentic-terminal", Difficulty: Hard}) // effort xhigh
	if c.Model != "b" { t.Fatalf("a's max row must not count at xhigh; got %+v", c) }
}
func TestUnscoredIsFallbackOnly(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"scored":   mk("mid", 60, s("arena-coding", "", 1500, 5)),
		"unscored": mk("mid", 1, nil),
	})
	c, _ := Choose(Input{Table: tb, Category: "webdev", Difficulty: Routine})
	if c.Model != "scored" { t.Fatalf("%+v", c) }
	c, _ = Choose(Input{Table: tb, Category: "webdev", Difficulty: Routine, Available: func(m string) bool { return m != "scored" }})
	if c.Model != "unscored" || c.Reason != "fallback-unscored" { t.Fatalf("%+v", c) }
}
func TestVisionFilterAndTierRaise(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"blind": mkv("flash", 1, false, s("arena-overall", "", 1400, 5)),
		"eyes":  mkv("mid", 5, true, s("arena-overall", "", 1450, 5)),
	})
	c, _ := Choose(Input{Table: tb, Category: "extraction", Difficulty: Trivial, HasImage: true})
	if c.Model != "eyes" || c.Reason != "tier-raised" || c.Tier != "mid" { t.Fatalf("%+v", c) }
}
func TestUnknownCategoryUsesGeneral(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"a": mk("top", 60, s("arena-overall", "", 1500, 5)),
		"b": mk("top", 60, s("arena-webdev", "", 1900, 5)),
	})
	c, _ := Choose(Input{Table: tb, Category: "", Difficulty: Hard})
	if c.Model != "a" || c.Benchmark != "arena-overall" { t.Fatalf("%+v", c) }
}
func TestExtractionPicksCheapestFlash(t *testing.T) {
	tb := tbl(map[string]table.Model{"x": mk("flash", 3, nil), "y": mk("flash", 1, nil)})
	c, _ := Choose(Input{Table: tb, Category: "extraction", Difficulty: Trivial})
	if c.Model != "y" { t.Fatalf("%+v", c) }
}
```

- [x] **Step 2: Run, expect compile failure** — `go test ./internal/decide/`.

- [x] **Step 3: Implement** (`decide.go`; ≈120 lines)

```go
package decide

import (
	"errors"
	"sort"

	"github.com/chloeassistant/cpa-plugin-auto-router/internal/table"
)

var effortRank = map[string]int{"low": 0, "medium": 1, "high": 2, "xhigh": 3, "max": 4}
var tierUp = map[string]string{"flash": "mid", "mid": "top"}

func Rank(d string) int {
	for i, x := range Difficulties { if x == d { return i } }
	return -1
}
func TierOf(d string) string     { return map[string]string{Trivial: "flash", Routine: "mid", Hard: "top", Extreme: "top"}[d] }
func ThinkingOf(d string) string { return map[string]string{Trivial: "low", Routine: "high", Hard: "xhigh", Extreme: "max"}[d] }

func BenchmarksFor(category string) []string {
	if b, ok := benchByCategory[category]; ok { return append(append([]string{}, b...), generalFallback...) }
	return generalFallback
}

// scoreAt returns the score usable at `effort`: exact or closest below; "" matches any.
func scoreAt(scores []table.Score, effort string) (table.Score, bool) {
	want := effortRank[effort]
	best, found, bestRank := table.Score{}, false, -1
	for _, s := range scores {
		r := -1
		if s.Effort != "" {
			r = effortRank[s.Effort]
			if r > want { continue }
		}
		if !found || r > bestRank { best, found, bestRank = s, true, r }
	}
	return best, found
}

func Choose(in Input) (Choice, error) {
	tier, effort := TierOf(in.Difficulty), ThinkingOf(in.Difficulty)
	reason := "ranked"
	for {
		cands := candidates(in, tier)
		if len(cands) == 0 {
			next, ok := tierUp[tier]
			if !ok { return Choice{}, errors.New("no candidate in any tier") }
			tier, reason = next, "tier-raised"
			continue
		}
		for _, bench := range BenchmarksFor(in.Category) {
			type scored struct{ id string; s table.Score; cost float64 }
			var ranked []scored
			for _, id := range cands {
				m := in.Table.Models[id]
				if s, ok := scoreAt(m.Scores[bench], effort); ok {
					ranked = append(ranked, scored{id, s, m.Cost.Input + m.Cost.Output})
				}
			}
			if len(ranked) == 0 { continue }
			sort.Slice(ranked, func(i, j int) bool { return ranked[i].s.Value > ranked[j].s.Value })
			win := ranked[0]
			for _, r := range ranked[1:] {
				m := win.s.Margin
				if r.s.Margin > m { m = r.s.Margin }
				if win.s.Value-r.s.Value <= m && r.cost < win.cost { win = r }
			}
			return Choice{Model: win.id, Tier: tier, Thinking: effort, Benchmark: bench, Score: win.s.Value, Reason: reason}, nil
		}
		// nobody scored on any benchmark → cheapest
		sort.Slice(cands, func(i, j int) bool {
			a, b := in.Table.Models[cands[i]].Cost, in.Table.Models[cands[j]].Cost
			return a.Input+a.Output < b.Input+b.Output
		})
		return Choice{Model: cands[0], Tier: tier, Thinking: effort, Reason: "fallback-unscored"}, nil
	}
}

func candidates(in Input, tier string) []string {
	var out []string
	for id, m := range in.Table.Models {
		if m.Tier != tier { continue }
		if in.HasImage && !m.Vision { continue }
		if in.Exclude != nil && in.Exclude(id) { continue }
		if in.Available != nil && !in.Available(id) { continue }
		out = append(out, id)
	}
	sort.Strings(out) // deterministic
	return out
}
```

Note the tie loop: after sorting by value desc, a cheaper model within margin of the current winner replaces it; this is the spec's "empate → menor custo". `extraction` has an empty list so it goes straight to `generalFallback`; with flash models typically unscored there, it lands on cheapest — matching the spec.

- [x] **Step 4: Run tests, expect PASS** — `go test ./internal/decide/ -v`.

- [x] **Step 5: Commit** — `git commit -am "feat(decide): choose model by category benchmarks, effort-aware, cost tie-break"`.

---

### Task 4: Decision core — session escalation (`internal/decide`, part 2)

**Files:**
- Modify: `internal/decide/decide.go`
- Test: `internal/decide/decide_test.go`

**Interfaces:**
- Produces:

```go
type State struct { Difficulty, Model, Thinking, Tier string } // zero value = no session
type Decision struct {
	Choice
	Reason string // new|keep|escalate-thinking|escalate-tier|vision-swap|fallback|jev-unavailable
	State  State  // to be stored
}
// Next applies the per-turn rule table from the spec. jevOK=false → difficulty/category are ignored.
func Next(in Input, prev State, jevOK bool) (Decision, error)
```

Rules (spec "Sessão e escalada"):
1. `!jevOK`: no prev → `Choose` with `Routine`, `""` category, reason `jev-unavailable`; prev exists → keep prev, reason `jev-unavailable`.
2. prev empty → `Choose`, reason `new`.
3. prev model unavailable (`in.Available != nil && !in.Available(prev.Model)`) → `Choose` at `prev`'s difficulty (or the new one if higher), reason `fallback`.
4. `HasImage` and `!Table.Models[prev.Model].Vision` → `Choose` at prev tier/difficulty **restricted to prev tier** (no raise unless empty), keep thinking, reason `vision-swap`.
5. `Rank(new) <= Rank(prev)` → keep, reason `keep`.
6. same tier → keep model, thinking = `ThinkingOf(new)`, reason `escalate-thinking`.
7. higher tier → `Choose` with new difficulty and this turn's category, reason `escalate-tier`.

- [x] **Step 1: Write failing tests**

```go
func TestNextNewThenKeep(t *testing.T) {
	tb := tbl(map[string]table.Model{"a": mk("mid", 5, s("arena-coding", "", 1500, 5))})
	d, _ := Next(Input{Table: tb, Category: "backend", Difficulty: Routine}, State{}, true)
	if d.Reason != "new" || d.State.Model != "a" { t.Fatalf("%+v", d) }
	d2, _ := Next(Input{Table: tb, Category: "webdev", Difficulty: Trivial}, d.State, true)
	if d2.Reason != "keep" || d2.Model != "a" || d2.Thinking != "high" { t.Fatalf("never downgrade: %+v", d2) }
}
func TestNextEscalateThinkingSameTier(t *testing.T) {
	tb := tbl(map[string]table.Model{"a": mk("top", 5, s("arena-coding", "", 1500, 5)), "b": mk("top", 1, s("arena-coding", "", 1600, 5))})
	prev := State{Difficulty: Hard, Model: "a", Thinking: "xhigh", Tier: "top"}
	d, _ := Next(Input{Table: tb, Category: "backend", Difficulty: Extreme}, prev, true)
	if d.Reason != "escalate-thinking" || d.Model != "a" || d.Thinking != "max" { t.Fatalf("%+v", d) }
}
func TestNextEscalateTierRechooses(t *testing.T) {
	tb := tbl(map[string]table.Model{"m": mk("mid", 5, s("arena-coding", "", 1500, 5)), "t": mk("top", 50, s("arena-coding", "", 1700, 5))})
	prev := State{Difficulty: Routine, Model: "m", Thinking: "high", Tier: "mid"}
	d, _ := Next(Input{Table: tb, Category: "backend", Difficulty: Hard}, prev, true)
	if d.Reason != "escalate-tier" || d.Model != "t" || d.Thinking != "xhigh" { t.Fatalf("%+v", d) }
}
func TestNextVisionSwapStaysInTier(t *testing.T) {
	tb := tbl(map[string]table.Model{
		"blind": mkv("mid", 1, false, s("arena-overall", "", 1500, 5)),
		"eyes":  mkv("mid", 5, true, s("arena-overall", "", 1450, 5)),
		"top":   mkv("top", 50, true, s("arena-overall", "", 1800, 5)),
	})
	prev := State{Difficulty: Routine, Model: "blind", Thinking: "high", Tier: "mid"}
	d, _ := Next(Input{Table: tb, Category: "writing", Difficulty: Routine, HasImage: true}, prev, true)
	if d.Reason != "vision-swap" || d.Model != "eyes" || d.Thinking != "high" { t.Fatalf("%+v", d) }
}
func TestNextJevUnavailable(t *testing.T) {
	tb := tbl(map[string]table.Model{"m": mk("mid", 5, s("arena-overall", "", 1500, 5))})
	d, _ := Next(Input{Table: tb}, State{}, false)
	if d.Reason != "jev-unavailable" || d.State.Difficulty != Routine { t.Fatalf("%+v", d) }
	prev := State{Difficulty: Hard, Model: "x", Thinking: "xhigh", Tier: "top"}
	d, _ = Next(Input{Table: tb}, prev, false)
	if d.Reason != "jev-unavailable" || d.Model != "x" { t.Fatalf("%+v", d) }
}
func TestNextFallbackWhenStoredUnavailable(t *testing.T) {
	tb := tbl(map[string]table.Model{"a": mk("mid", 5, s("arena-overall", "", 1500, 5)), "b": mk("mid", 5, s("arena-overall", "", 1400, 5))})
	prev := State{Difficulty: Routine, Model: "a", Thinking: "high", Tier: "mid"}
	d, _ := Next(Input{Table: tb, Category: "writing", Difficulty: Trivial, Available: func(m string) bool { return m != "a" }}, prev, true)
	if d.Reason != "fallback" || d.Model != "b" { t.Fatalf("%+v", d) }
}
```

- [x] **Step 2: Run, expect failure** (`undefined: Next`).

- [x] **Step 3: Implement**

```go
func Next(in Input, prev State, jevOK bool) (Decision, error) {
	has := prev.Model != ""
	wrap := func(c Choice, reason string) Decision {
		return Decision{Choice: c, Reason: reason, State: State{Difficulty: in.Difficulty, Model: c.Model, Thinking: c.Thinking, Tier: c.Tier}}
	}
	keep := func(reason string) Decision {
		return Decision{Choice: Choice{Model: prev.Model, Tier: prev.Tier, Thinking: prev.Thinking}, Reason: reason, State: prev}
	}
	if !jevOK {
		if has { return keep("jev-unavailable"), nil }
		in.Difficulty, in.Category = Routine, ""
		c, err := Choose(in)
		if err != nil { return Decision{}, err }
		return wrap(c, "jev-unavailable"), nil
	}
	if !has {
		c, err := Choose(in)
		if err != nil { return Decision{}, err }
		return wrap(c, "new"), nil
	}
	if Rank(in.Difficulty) < Rank(prev.Difficulty) { in.Difficulty = prev.Difficulty }
	if in.Available != nil && !in.Available(prev.Model) {
		c, err := Choose(in)
		if err != nil { return Decision{}, err }
		return wrap(c, "fallback"), nil
	}
	if in.HasImage && !in.Table.Models[prev.Model].Vision {
		sub := in
		sub.Difficulty = prev.Difficulty
		c, err := Choose(sub)
		if err != nil { return Decision{}, err }
		c.Thinking = prev.Thinking
		d := wrap(c, "vision-swap")
		d.State.Difficulty = prev.Difficulty
		return d, nil
	}
	if in.Difficulty == prev.Difficulty { return keep("keep"), nil }
	if TierOf(in.Difficulty) == prev.Tier {
		d := keep("escalate-thinking")
		d.Thinking = ThinkingOf(in.Difficulty)
		d.State.Thinking, d.State.Difficulty = d.Thinking, in.Difficulty
		return d, nil
	}
	c, err := Choose(in)
	if err != nil { return Decision{}, err }
	return wrap(c, "escalate-tier"), nil
}
```

(`vision-swap` calls `Choose` at the previous difficulty, whose tier equals `prev.Tier`; `Choose` only raises when that tier has no vision candidate — spec-consistent.)

- [x] **Step 4: Run tests, expect PASS** — `go test ./internal/decide/ -v`.

- [x] **Step 5: Commit** — `git commit -am "feat(decide): per-turn escalation rules (only-escalate, vision-swap, fallback, jev-unavailable)"`.

---

### Task 5: Session store and session-id extraction (`internal/session`)

**Files:**
- Create: `internal/session/session.go`, `internal/session/session_test.go`

**Interfaces:**
- Consumes: `decide.State`.
- Produces:

```go
package session

type Store struct { /* mu, entries map[string]entry, ttl, max */ }
func New(ttl time.Duration, max int) *Store
func (s *Store) Get(id string) (decide.State, bool)   // expired → false
func (s *Store) Put(id string, st decide.State)        // evicts oldest when len > max

// ID mirrors the host order (sdk/cliproxy/auth/selector.go ExtractSessionID):
// X-Session-ID, X-Session-Affinity, X-Client-Request-Id, body session_id/sessionId,
// prompt_cache_key, conversation(.id), metadata.user_id; else sha256 of the first
// user message text (first 8 bytes hex). Returns "" only for an empty body.
func ID(headers http.Header, body []byte) string
```

- [x] **Step 1: Write failing tests**

```go
func TestIDPrecedence(t *testing.T) {
	h := http.Header{"X-Session-Id": {"abc"}}
	if ID(h, []byte(`{"prompt_cache_key":"pck"}`)) != "abc" { t.Fatal("header wins") }
	if ID(nil, []byte(`{"prompt_cache_key":"pck"}`)) != "pck" { t.Fatal("prompt_cache_key") }
	a := ID(nil, []byte(`{"messages":[{"role":"system","content":"s"},{"role":"user","content":"hello"}]}`))
	b := ID(nil, []byte(`{"messages":[{"role":"system","content":"s"},{"role":"user","content":"hello"},{"role":"assistant","content":"x"}]}`))
	if a == "" || a != b { t.Fatalf("first-user hash must be stable across turns: %q %q", a, b) }
	r := ID(nil, []byte(`{"input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`))
	if r == "" { t.Fatal("responses input must hash") }
}
func TestStoreTTLAndEvict(t *testing.T) {
	s := New(50*time.Millisecond, 2)
	s.Put("a", decide.State{Model: "m"})
	if _, ok := s.Get("a"); !ok { t.Fatal("get") }
	time.Sleep(60 * time.Millisecond)
	if _, ok := s.Get("a"); ok { t.Fatal("expired") }
	s.Put("1", decide.State{}); s.Put("2", decide.State{}); s.Put("3", decide.State{})
	if _, ok := s.Get("1"); ok { t.Fatal("oldest evicted") }
}
```

- [x] **Step 2: Run, expect failure.**

- [x] **Step 3: Implement** — `ID` uses `gjson`: headers in order `X-Session-Id`, `X-Session-Affinity`, `X-Client-Request-Id`; then `session_id`, `sessionId`, `prompt_cache_key`, `conversation.id`, `conversation` (string), `metadata.user_id`; else first user text: `messages.#(role=="user").content` (string, or `#.text` joined when array) for chat; `input.#(role=="user").content` same for Responses; `input` as plain string. Hash: `sha256`, hex of first 8 bytes, prefixed `h:`. Store: map + `time.Now()` stamp per entry; eviction is a linear scan for the oldest (`// ponytail: O(n) evict, fine at 65k; heap if it ever shows in a profile`).

- [x] **Step 4: Run tests, expect PASS.**

- [x] **Step 5: Commit** — `git add internal/session && git commit -m "feat(session): TTL state store and host-compatible session id extraction"`.

---

### Task 6: Snippet + local signals (`internal/snippet`) and Jev client (`internal/jev`)

**Files:**
- Create: `internal/snippet/snippet.go`, `internal/snippet/snippet_test.go`, `internal/jev/client.go`, `internal/jev/client_test.go`

**Interfaces:**
- Produces:

```go
package snippet
type Signals struct { Tools, Images, Messages int; Format string; HasNewUserMessage bool }
// Extract returns the last user message text (tail ≤ max chars) and counters.
// Handles chat-completions (`messages`) and Responses (`input` string|array).
// Image = any content part with type image_url/input_image/image.
// HasNewUserMessage=false when the last message is not a user text (e.g. only tool results).
func Extract(format string, body []byte, max int) (string, Signals)
```

```go
package jev
type Config struct { BaseURL, EndpointPath, Model, APIKey string; Timeout time.Duration }
type Answer struct { Choice string; Probabilities map[string]float64; Confidence float64 }
type Result struct { Category, Difficulty Answer; Millis int64 }
var ErrUnavailable = errors.New("jev unavailable")
func (c Config) Validate() error   // URL policy: https anywhere; http only loopback/RFC1918/CGNAT/ULA; no userinfo
func Decide(ctx context.Context, c Config, item string, sig snippet.Signals) (Result, error)
```

Request body sent by `Decide`:

```json
{"model":"typesafe/jev-1.13",
 "state":{"context":"Request to an LLM proxy. Classify the task the user is asking for.",
          "item":"<snippet>","signals":{"tools":12,"images":0,"messages":37,"format":"responses"}},
 "questions":{
   "category":{"type":"choice","instructions":"Classify the task in `item` (use `signals` as hints).",
     "criteria":{"webdev":"front-end/web UI/HTML/CSS/JS apps","backend":"server code, APIs, data models, implementation in a repo","agentic-terminal":"multi-step work driving shell/tools/files","debugging":"find why something fails; trace behaviour","review":"read, critique or test existing code; security review","spec-design":"architecture, design, planning, specs","writing":"prose, docs, messages, summaries for humans","extraction":"extract/reformat/classify data, tiny transformations","math-data":"math, statistics, data analysis with a definite answer"}},
   "difficulty":{"type":"choice","instructions":"How hard is `item` for a strong model?",
     "criteria":{"trivial":"one-liner or lookup, no reasoning","routine":"standard task, known pattern","hard":"needs real reasoning, many constraints or a large codebase","extreme":"research-grade, ambiguous, or very long multi-step"}}}}
```

Response parsing: `answers.category.choice` (string), `answers.category.probabilities` (object) or fallback derive from `choice` = 1.0, `answers.category.confidence` (number, default 0). Any HTTP ≠ 200, JSON error, timeout, response > 1 MB → `ErrUnavailable`.

- [ ] **Step 1: Write failing tests** — snippet: chat body with system+user+assistant+user → returns last user text, `Messages=4`, `HasNewUserMessage=true`; Responses body with `input` array and an `input_image` part → `Images=1`; tool-result-only last message → `HasNewUserMessage=false`; tail truncation at `max`. jev: `httptest.Server` returning a canned answer → `Result` fields; server returning 500 → `ErrUnavailable`; `Validate` rejects `http://example.com`, accepts `http://127.0.0.1:9`, rejects `https://u:p@host`.

- [ ] **Step 2: Run, expect failure.**

- [ ] **Step 3: Implement** — `snippet` with `gjson` (chat: `messages`; Responses: `input`); `jev` with `net/http` + `context.WithTimeout`, `io.LimitReader(resp.Body, 1<<20)`, `Authorization: Bearer`. URL policy port of `hermes/plugins/jev/client.py:_check_url` using `net/netip` prefixes (`10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10, 127.0.0.0/8, ::1/128, fc00::/7, fe80::/10`).

- [ ] **Step 4: Run tests, expect PASS.**

- [ ] **Step 5: Commit** — `git add internal/snippet internal/jev && git commit -m "feat: request snippet/signals and Jev typed-decision client"`.

---

### Task 7: Plugin shell — registration, route, execute, stream (`main.go`, `host.go`, `plugin.go`)

**Files:**
- Create: `main.go` (copy the cgo preamble and the four exports from `examples/plugin/claude-web-search-router/go/main.go:1-186`, unchanged), `host.go`, `plugin.go`

**Interfaces:**
- Consumes: everything above.
- Produces: the `.so`. Method dispatch:

| method | handler |
|---|---|
| `plugin.register` / `plugin.reconfigure` | `configure(raw)` then registration JSON |
| `model.register` / `model.static` | `{Provider:"auto-router", Models:[{ID:"auto-router", Object:"model", OwnedBy:"auto-router", DisplayName:"Auto Router (Jev)", SupportedGenerationMethods:["chat"], ContextLength:1000000, UserDefined:true}]}` |
| `model.route` | `routeModel(raw)` |
| `executor.identifier` | `{"identifier":"auto-router"}` |
| `executor.execute` | `execute(raw)` |
| `executor.execute_stream` | `executeStream(raw)` |
| `executor.count_tokens` | `{"input_tokens":0}` |

Registration capabilities: `model_registrar:true, model_router:true, executor:true, executor_model_scope:"static", executor_input_formats:["chat-completions","responses"], executor_output_formats:["chat-completions","responses"]`. `ConfigFields`: `enabled` (boolean), `jev_api_key_env` (string, default `OPENROUTER_API_KEY`), `jev_base_url` (string), `jev_endpoint_path` (string), `jev_model` (string), `confidence_threshold` (number), `table_path` (string, default `<plugin dir>/auto-router/models.yaml` — resolve relative to the `.so` location via `os.Executable`? No: the `.so` has no executable; default to `/home/hermes/cliproxyapi/plugins/auto-router/models.yaml`, documented), `snippet_chars` (integer), `jev_timeout_ms` (integer).

`routeModel(raw)`:
1. Unmarshal `rpcModelRouteRequest`. If `RequestedModel != "auto-router"` (after `thinking.ParseSuffix`-style strip of a trailing `(...)`) → `{Handled:false}`.
2. `sid := session.ID(req.Headers, req.Body)`; `prev, _ := store.Get(sid)`.
3. `text, sig := snippet.Extract(req.SourceFormat, req.Body, cfg.SnippetChars)`.
4. If `prev.Difficulty == Extreme` or `!sig.HasNewUserMessage` → decision = keep (reason `keep`), skip Jev.
5. Else `res, err := jev.Decide(ctx, cfg.Jev, text, sig)`; `jevOK = err == nil`; category = `res.Category.Choice` if `Confidence >= threshold` else `""`; difficulty = `res.Difficulty.Choice` if `Confidence >= threshold` else (`prev.Difficulty` if set else `Routine`).
6. `in := decide.Input{Table: tbl.Get(), Category, Difficulty, HasImage: sig.Images > 0, Exclude: excluded}`. `Available` = nil in v1 (`// ponytail: host cooldown not visible to routers; use host.affinity.lookup if a stored model keeps failing`).
7. `d, err := decide.Next(in, prev, jevOK)`; on error → log error, `{Handled:false}` (host will 400 "unknown model" — acceptable, it's a table problem).
8. `store.Put(sid, d.State)`; `pending.Store(requestKey(req), d)` where `requestKey` = `Metadata["request_id"]` if present else `sid` (`// ponytail: keyed by session when no request id; concurrent turns in one session race harmlessly to the same decision`).
9. `hostLog("info", "auto-router decision", fields{...})` with `session` (sid), `category`, `category_p`, `difficulty`, `difficulty_p`, `confidence`, `tier`, `model`, `thinking`, `reason`, `jev_ms`.
10. Return `{Handled:true, TargetKind:"self", Reason:d.Reason}`.

`execute(raw)` / `executeStream(raw)`: unmarshal `rpcExecutorRequest`; look up `d` in `pending` by `Metadata["request_id"]`/session (from `req.Headers` + `req.OriginalRequest`); `model := d.Model + "(" + d.Thinking + ")"`; call `host.model.execute` (non-stream) or the stream-forward loop copied from `examples/.../stream_forward.go:125-180` with `EntryProtocol == ExitProtocol == req.SourceFormat`, `Body: req.OriginalRequest`, `HostCallbackID: req.HostCallbackID`. Response headers: `{"Content-Type": ..., "X-Auto-Router": model+";"+d.Reason}`. If `pending` has no entry (e.g. host restarted mid-flight) → run `routeModel` logic inline once (same function, refactored as `decideFor(headers, body, format) Decision`).

- [ ] **Step 1: Write the one test the shell needs** — `plugin_test.go`: `TestRouteIgnoresOtherModels` (`routeModel` with `RequestedModel:"gpt-6-astra"` → `Handled:false`) and `TestRouteDecidesWithoutJev` (config with `jev_base_url` pointing to a closed port; `RequestedModel:"auto-router"`, chat body → `Handled:true`, `TargetKind:"self"`, `Reason:"jev-unavailable"`, and `pending` holds a model from the seed table). Use `table/models.yaml` from Task 10's seed (write a minimal seed now in `testdata/`).

- [ ] **Step 2: Run, expect failure.**

- [ ] **Step 3: Implement** `host.go` (envelope types, `callHost`, `hostLog`, `hostModelExecute`, `hostModelStreamForward`, `emitPluginStreamChunk`, `closePluginStream` — all copied from the example, renamed to drop the Claude-specific parts) and `plugin.go` as specified. `configure` parses `config_yaml` into:

```go
type pluginConfig struct {
	Enabled             bool    `yaml:"enabled"`
	JevAPIKeyEnv        string  `yaml:"jev_api_key_env"`
	JevBaseURL          string  `yaml:"jev_base_url"`
	JevEndpointPath     string  `yaml:"jev_endpoint_path"`
	JevModel            string  `yaml:"jev_model"`
	ConfidenceThreshold float64 `yaml:"confidence_threshold"`
	TablePath           string  `yaml:"table_path"`
	SnippetChars        int     `yaml:"snippet_chars"`
	JevTimeoutMS        int     `yaml:"jev_timeout_ms"`
}
```

with defaults applied when zero, `table.Watch(TablePath)` opened on configure (error → plugin registers but `routeModel` returns `Handled:false` and logs once).

- [ ] **Step 4: Build and test** — `make build && go test ./...`. Expected: `bin/auto-router.so` exists; tests pass.

- [ ] **Step 5: Commit** — `git add main.go host.go plugin.go plugin_test.go testdata && git commit -m "feat(plugin): register auto-router, route via Jev+table, execute through host.model.*"`.

---

### Task 8: Updater — sources and merge (`updater/`)

**Files:**
- Create: `updater/__init__.py`, `updater/__main__.py`, `updater/sources.py`, `updater/aliases.py`, `updater/merge.py`, `updater/test_updater.py`, `updater/testdata/{eee_astra.json, openrouter.json, epoch_deepswe.csv, arena_webdev.parquet, modelsdev.json, catalog.json}`

**Interfaces:**
- Produces: `python -m updater --catalog URL --catalog-key-env CLIPROXY_API_KEY --tiers table/tiers.yaml --out table/models.yaml [--openrouter-key-env OPENROUTER_API_KEY] [--only eee,openrouter,epoch,arena]`. Exit 0 = wrote; 1 = validation failed (file untouched); 2 = catalog unreachable.

Internal shape: every source returns `list[Row]` with `Row = (model_id, benchmark_id, effort|None, value, margin, date, note)`; `model_id` already mapped through `aliases.py` (unknown → `WARN unmapped <source> <name>` and dropped).

Source readers (all take a `fetch(url) -> bytes` so tests inject fixtures):

- `eee(fetch, catalog_ids)`: for each `(source, org, model_dir)` in `aliases.EEE` (explicit list, e.g. `("vals-ai","openai","gpt-6-astra","gpt-6-astra")`), list `.../tree/main/data/{source}/{org}/{model_dir}`, fetch every `.json`, keep newest per `evaluation_name` by `benchmark_updated` or `cron_run_date`. Map `evaluation_name` → benchmark id through `aliases.EEE_EVALS` (e.g. `vals_ai.terminal-bench-4.overall → terminal-bench-4`, `vals_ai.terminal-bench-4.software → terminal-bench-4-software`, `vals_ai.terminal-bench-4.security → terminal-bench-4-security`, `llm_stats.swe-bench-pro → swe-bench-pro-v2`, `llm_stats.swe-atlas-codebase-qna → swe-atlas-qna`, `llm_stats.swe-atlas-test-writing → swe-atlas-test-writing`, `llm_stats.deepswe-1.1 → deepswe`, `artificial_analysis.artificial_analysis_intelligence_index → aa-intelligence-index`, `artificial_analysis.gpqa → gpqa-diamond`). Effort: `None` (EEE doesn't carry it). Margin: `0` unless the record has `score_details.details.ci` — use fixed per-benchmark margins from `aliases.MARGINS` (`terminal-bench-4: 3.0, swe-bench-pro-v2: 1.5, swe-atlas-*: 5.0, gpqa-diamond: 2.0, aa-*: 1.0`) — `# ponytail: fixed margins from the sources' published CI ranges; per-row CI when EEE carries it`.
- `openrouter(fetch, key)`: `GET https://openrouter.ai/api/v1/benchmarks` → rows by `source`: `artificial-analysis` → `aa-intelligence-index`/`aa-coding-index`/`aa-coding-agent-index`(if present); `openrouter` GPQA → `gpqa-diamond` with `margin = stddev`; `design-arena` `website`/`uicomponent` → `design-arena-website`/`design-arena-uicomponent` (extra webdev signals; add to `benchByCategory["webdev"]` in Task 3 only if coverage ≥ 10 catalog models — record the count in the run summary; do not wire yet). Model slug map: `aliases.OPENROUTER` (`openai/gpt-6-astra → gpt-6-astra`, …).
- `epoch(fetch)`: download `https://epoch.ai/data/benchmarks/benchmarks_data.zip`, read `deepswe_external.csv` (`Pass@1`, `95% CI half-width`, `Reasoning effort`, `Release date`) → `deepswe`; `webdev_arena_external.csv` (`Arena Score`) → `arena-webdev` (secondary; Arena parquet wins on date); `cursorbench_external.csv` (`Score`, `Reasoning level`) → `cursorbench`; `frontiercode_external.csv` (`Main score`) → `frontiercode`. `Model version` `id_effort` split on last `_`; map id through `aliases.EPOCH`.
- `arena(fetch)`: parquet `latest` for `text_style_control` (categories `overall, coding, math, creative_writing, instruction_following, hard_prompts` → `arena-overall, arena-coding, arena-math, arena-creative-writing, arena-instruction-following, arena-hard-prompts`), `webdev` → `arena-webdev`, `agent` → `arena-agent` (score×1000 to keep integers readable; note `unit: agent-score`). `margin = (rating_upper - rating_lower)/2`; date = `leaderboard_publish_date`. Effort from the `model_name` suffix (`-max`, `-xhigh`, `-high`, `(xHigh)`) via `aliases.arena_effort(name)`; `aliases.ARENA` maps names (`gpt-6-astra-max → gpt-6-astra`).
- `modelsdev(fetch)`: `https://models.dev/api.json` → per catalog id: `vision`, `cost.input/output` (first provider entry that has the id; `aliases.MODELSDEV` overrides when the id differs).

`merge.merge(old: Table|None, rows, catalog_ids, tiers, caps)`: start from old table's scores for ids still in catalog; for each row keep it if no existing `(model, bench, effort)` or `row.date >= existing.date`; drop rows for ids not in `catalog ∩ tiers`; WARN each catalog id without tier; result `benchmarks` = union of declared ids in `aliases.BENCHMARKS` (id → `{source, unit}`); `validate()` = same rules as the Go loader; `write_atomic(path)`.

- [ ] **Step 1: Write failing tests** (fixture-driven; fixtures are small hand-trimmed copies of the real payloads captured on 2026-09-24 — cut to ≤ 3 models each)

```python
def test_eee_keeps_newest_snapshot(fx):
    rows = sources.eee(fx, {"gpt-6-astra"})
    tb4 = [r for r in rows if r.model == "gpt-6-astra" and r.bench == "terminal-bench-4"]
    assert len(tb4) == 1 and tb4[0].value == 57.071 and tb4[0].date == "2026-09-22"

def test_openrouter_gpqa_margin_is_stddev(fx):
    rows = sources.openrouter(fx, key="x")
    g = next(r for r in rows if r.bench == "gpqa-diamond" and r.model == "gpt-6-astra")
    assert g.margin > 0

def test_epoch_effort_split(fx):
    rows = sources.epoch(fx)
    assert any(r.model == "gpt-5.6-sol" and r.effort == "max" and r.bench == "deepswe" for r in rows)

def test_arena_margin_from_ci(fx):
    rows = sources.arena(fx)
    r = next(r for r in rows if r.bench == "arena-webdev" and r.model == "gpt-6-astra")
    assert r.effort == "max" and abs(r.margin - 12.04) < 0.1

def test_merge_never_regresses_and_warns_untiered(fx, caplog):
    old = merge.load("testdata/old.yaml")
    new = merge.merge(old, rows=[], catalog_ids={"gpt-6-astra", "new-model"}, tiers={"gpt-6-astra": "top"}, caps={})
    assert new.models["gpt-6-astra"].scores == old.models["gpt-6-astra"].scores
    assert "new-model" in caplog.text and "no tier" in caplog.text

def test_merge_date_rule(fx):
    old = merge.load("testdata/old.yaml")  # astra tb4 xhigh dated 2026-09-03
    older = [Row("gpt-6-astra", "terminal-bench-4", "xhigh", 10, 1, "2026-08-01", "")]
    newer = [Row("gpt-6-astra", "terminal-bench-4", "xhigh", 60, 1, "2026-09-22", "")]
    assert merge.merge(old, older, {"gpt-6-astra"}, {"gpt-6-astra": "top"}, {}).models["gpt-6-astra"].scores["terminal-bench-4"][0].value != 10
    assert merge.merge(old, newer, {"gpt-6-astra"}, {"gpt-6-astra": "top"}, {}).models["gpt-6-astra"].scores["terminal-bench-4"][0].value == 60

def test_validate_rejects_missing_date(tmp_path):
    with pytest.raises(merge.ValidationError):
        merge.validate({"benchmarks": {"x": {"source": "s", "unit": "pct"}},
                        "models": {"m": {"tier": "top", "vision": True, "cost": {"input": 1, "output": 1},
                                         "scores": {"x": [{"effort": None, "value": 1, "margin": 0}]}}}})
```

`fx` is a fixture returning `fetch(url)` that serves `testdata/` by URL substring (`"EEE_datastore/tree" → listing json`, `"resolve/main" → snapshot`, `"openrouter.ai" → openrouter.json`, `"epoch.ai" → zip built on the fly from epoch_deepswe.csv`, `"leaderboard-dataset" → parquet`).

- [ ] **Step 2: Run, expect failure** — `cd updater && uv run --with pyyaml --with pyarrow --with pytest --python 3.12 python -m pytest -q`.

- [ ] **Step 3: Implement** `sources.py`, `aliases.py` (explicit dicts; **every** catalog id from Task 1's `tiers.yaml` must have an entry per source or an explicit `None`), `merge.py`, `__main__.py` (argparse; `fetch` = `urllib.request` with UA `cpa-auto-router-updater/0.1`, 60 s timeout, retry ×3 with backoff on 429/5xx; per-source `try/except` → `WARN source failed: <err>`; end summary line `updated=N kept=M dropped=K unmapped=U untiered=[...]`).

- [ ] **Step 4: Run tests, expect PASS.**

- [ ] **Step 5: Commit** — `git add updater && git commit -m "feat(updater): EEE/OpenRouter/Epoch/Arena/models.dev readers, merge rules, atomic write"`.

---

### Task 9: Seed table, systemd units, install, smoke test

**Files:**
- Create: `table/models.yaml` (generated), `systemd/cpa-auto-router-update.service`, `systemd/cpa-auto-router-update.timer`, `README.md`

- [ ] **Step 1: Generate the seed table against the live proxy**

```bash
cd /home/hermes/projects/cpa-plugin-auto-router
export CLIPROXY_API_KEY=$(cat /home/hermes/cliproxyapi-test/api-key)   # TEST instance key; same catalog as 8317
set -a; . /home/hermes/.hermes/.env; set +a   # OPENROUTER_API_KEY
uv run --with pyyaml --with pyarrow --python 3.12 python -m updater \
  --catalog http://127.0.0.1:8318 --catalog-key-env CLIPROXY_API_KEY \
  --tiers table/tiers.yaml --out table/models.yaml --openrouter-key-env OPENROUTER_API_KEY
```

Expected: exit 0; summary line; `table/models.yaml` has every tiered model; `journal`-style WARN lines list untiered catalog ids (`abliterated-*`, `gpt-image-*`, `codex-auto-review`, old Claude ids — expected). Then `go test ./internal/table/ -run TestLoadGood` after pointing a copy of the test at the generated file: `cp table/models.yaml internal/table/testdata/generated.yaml` and add `TestLoadGenerated` (Load must succeed).

- [ ] **Step 2: Install plugin + table into the TEST instance**

```bash
make install-test            # copies bin/auto-router.so to /home/hermes/cliproxyapi-test/plugins/
mkdir -p /home/hermes/cliproxyapi-test/plugins/auto-router
install -m 0644 table/models.yaml /home/hermes/cliproxyapi-test/plugins/auto-router/models.yaml
```

Edit `/home/hermes/cliproxyapi-test/config.yaml` `plugins.configs`:

```yaml
    auto-router:
      enabled: true
      priority: 10
      jev_api_key_env: OPENROUTER_API_KEY
      table_path: /home/hermes/cliproxyapi-test/plugins/auto-router/models.yaml
```

`OPENROUTER_API_KEY` for the test service: `grep -E '^OPENROUTER_API_KEY=' /home/hermes/.hermes/.env > /home/hermes/cliproxyapi-test/env && chmod 600 /home/hermes/cliproxyapi-test/env` (the unit already has `EnvironmentFile=-/home/hermes/cliproxyapi-test/env`; the value is copied file-to-file, never printed). Then `XDG_RUNTIME_DIR=/run/user/1000 systemctl --user restart cliproxyapi-test`.

Expected in journal: plugin `auto-router` loaded; `GET http://127.0.0.1:8318/v1/models` lists `auto-router`.

- [ ] **Step 3: Smoke — trivial and hard, chat and Responses**

```bash
KEY=$(cat /home/hermes/cliproxyapi-test/api-key)
curl -s http://127.0.0.1:8318/v1/chat/completions -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"auto-router","messages":[{"role":"user","content":"What is 12*7? Reply with the number only."}],"max_tokens":20}'
curl -sN http://127.0.0.1:8318/v1/responses -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"auto-router","stream":true,"input":"Design a migration plan to move a 40-table Postgres schema to multi-tenant row-level security without downtime; list risks and a rollback strategy."}' | head -c 1500
journalctl --user -u cliproxyapi-test -n 50 --no-pager | grep "auto-router decision"
```

Expected: first → 200 with an answer, log line `difficulty=trivial tier=flash thinking=low`; second → SSE `response.created … response.output_text.delta …`, log line `difficulty=hard|extreme tier=top thinking=xhigh|max`. Record both log lines in `README.md` under "Verified".

Then send a **second** trivial turn with the same `prompt_cache_key` as the hard one → log `reason=keep`, same model (only-escalate).

- [ ] **Step 4: systemd timer**

`systemd/cpa-auto-router-update.service`:

```ini
[Unit]
Description=Rebuild auto-router benchmark table
After=network-online.target
[Service]
Type=oneshot
WorkingDirectory=/home/hermes/projects/cpa-plugin-auto-router
EnvironmentFile=/home/hermes/cliproxyapi-test/env
ExecStart=/home/hermes/.local/bin/uv run --with pyyaml --with pyarrow --python 3.12 python -m updater --catalog http://127.0.0.1:8318 --catalog-key-file /home/hermes/cliproxyapi-test/api-key --tiers table/tiers.yaml --out /home/hermes/cliproxyapi-test/plugins/auto-router/models.yaml --openrouter-key-env OPENROUTER_API_KEY
```

(`--catalog-key-file` reads the proxy key from a 0600 file; add this flag in `__main__.py` alongside `--catalog-key-env`.) `systemd/cpa-auto-router-update.timer`:

```ini
[Unit]
Description=Weekly auto-router benchmark refresh
[Timer]
OnCalendar=weekly
RandomizedDelaySec=1h
Persistent=true
[Install]
WantedBy=timers.target
```

```bash
install -m 0644 systemd/*.service systemd/*.timer ~/.config/systemd/user/
systemctl --user daemon-reload && systemctl --user enable --now cpa-auto-router-update.timer
systemctl --user start cpa-auto-router-update.service && journalctl --user -u cpa-auto-router-update -n 30 --no-pager
```

Expected: run completes exit 0, summary line, `models.yaml` mtime updated, plugin log shows table reload on next decision (no restart).

- [ ] **Step 5: README + commit + push**

README must state: everything above ran against the TEST instance (8318). Production rollout is Task 10 and is done by the operator.

README: what it is, install (3 commands), config keys, `tiers.yaml` ownership, updater, timer, "Verified" section with the two log lines, known limits (`X-Auto-Router` needs `passthrough-headers: true`; session state lost on restart; `Available` not wired).

```bash
git add table/models.yaml systemd README.md internal/table/testdata/generated.yaml
git commit -m "feat: seed table, systemd timer, install and smoke-verified end to end"
git push -u origin main   # remote already exists: git@github.com:chloeassistant/cpa-plugin-auto-router.git
```

---


---

### Task 10: Production rollout (OPERATOR-GATED — do not execute; write the runbook only)

**Files:**
- Create: `docs/runbook-production.md`

The worker writes the runbook; **the operator runs it**. Production is `cliproxyapi.service` on 8317 — the proxy the orchestrator itself is using. No restart is needed: the proxy hot-reloads `config.yaml` (`sdk/cliproxy/service_config.go:171` → `pluginHost.ApplyConfig`) and picks up new files in `plugins/`.

- [ ] **Step 1: Write `docs/runbook-production.md`** with exactly these steps (commands verbatim):

```bash
# 1. Binary and table (no restart; the host loads the .so on the next config apply)
install -m 0644 ~/projects/cpa-plugin-auto-router/bin/auto-router.so ~/cliproxyapi/plugins/auto-router.so
mkdir -p ~/cliproxyapi/plugins/auto-router
install -m 0644 ~/cliproxyapi-test/plugins/auto-router/models.yaml ~/cliproxyapi/plugins/auto-router/models.yaml

# 2. Secret for the plugin: single-key env file, 0600, file-to-file copy
grep -E '^OPENROUTER_API_KEY=' ~/.hermes/.env > ~/cliproxyapi/plugins/auto-router/env && chmod 600 ~/cliproxyapi/plugins/auto-router/env
# add to ~/.config/systemd/user/cliproxyapi.service [Service]:  EnvironmentFile=-/home/hermes/cliproxyapi/plugins/auto-router/env
# systemctl --user daemon-reload   ← does NOT restart; the env line takes effect on the NEXT restart,
# so until then the plugin runs with jev-unavailable (routine/high) — still functional. Schedule the
# restart for a quiet moment.

# 3. Enable in config.yaml (hot-reloaded): set plugins.enabled: true, replace the `example` entry with
#     auto-router: {enabled: true, priority: 10, jev_api_key_env: OPENROUTER_API_KEY,
#                   table_path: /home/hermes/cliproxyapi/plugins/auto-router/models.yaml}
# 4. Verify without restart
journalctl --user -u cliproxyapi -n 40 --no-pager | grep -i "auto-router"
curl -s -H "Authorization: Bearer $KEY" http://127.0.0.1:8317/v1/models | grep -o '"auto-router"'

# 5. Point the weekly timer at production: edit ~/.config/systemd/user/cpa-auto-router-update.service
#    --catalog http://127.0.0.1:8317 --catalog-key-file <0600 file with the 8317 key>
#    --out /home/hermes/cliproxyapi/plugins/auto-router/models.yaml ; daemon-reload.

# Rollback: set auto-router.enabled: false in config.yaml (hot-reload) — no restart.
```

- [ ] **Step 2: Commit** — `git add docs/runbook-production.md && git commit -m "docs: production rollout runbook (operator-gated)"`.

## Self-review

**Spec coverage:** Fluxo por pedido → Task 7. Classificação (state, questions, threshold, fail-open) → Task 6 + Task 7 step 5. Tabela: dificuldade→tier/thinking, categoria→benchmarks, effort-aware scores, escolha, formato, regras de carga, tiers.yaml → Tasks 1–3. Updater (catalog live, WARN untiered, sources, aliases, date rule, keep-old, validate, atomic) → Task 8, timer → Task 9. Sessão e escalada (all 8 rows) → Tasks 4–5 (`keep` on `extreme`/no-new-user-message in Task 7 step 4). Observabilidade → Task 7 step 9 + header (documented limitation, Task 0 step 4). Segurança → Task 6 URL policy, key via env, Task 9 env file 0600. Testes → every task. Verificações → Task 0. Gap found and fixed: spec lists `Available` (host cooldown) — not exposed to routers; wired as `nil` with a `ponytail:` note and listed in README limits.

**Placeholders:** none; every step has code or an exact command.

**Type consistency:** `decide.State{Difficulty, Model, Thinking, Tier}` used identically in Tasks 4, 5, 7; `table.Score.Effort` string with `""` = any, matched by `scoreAt` and by the updater writing `effort: null`; benchmark ids in `benchByCategory` (Task 3) match `aliases.BENCHMARKS` ids (Task 8): `arena-webdev, arena-coding, arena-math, arena-creative-writing, arena-instruction-following, arena-hard-prompts, arena-overall, arena-agent, terminal-bench-4, terminal-bench-4-software, terminal-bench-4-security, swe-bench-pro-v2, swe-atlas-qna, swe-atlas-test-writing, swe-atlas-refactoring, deepswe, gpqa-diamond, aa-intelligence-index, aa-coding-index, aa-coding-agent-index, gdpval-aa, cursorbench, frontiercode`. `swe-atlas-refactoring` and `gdpval-aa` come only from EEE `llm-stats`/`artificial-analysis-llms` when present (`llm_stats.swe-atlas-refactoring` was not seen in the sample; if absent after the seed run, the category list simply skips to the next benchmark — no code change).
