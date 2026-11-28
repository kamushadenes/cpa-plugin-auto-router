package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kamushadenes/cpa-plugin-auto-router/internal/table"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// 2026-11-25 12:00 BRT.
var statsNow = time.Date(2026, 11, 25, 15, 0, 0, 0, time.UTC)

func guardedCfg() pluginConfig {
	return pluginConfig{RefusalFallback: []refusalFallbackAlias{{Alias: "opus-guarded", Primary: "claude-opus-5-5(high)", Fallback: fallbackModels{"asteras/canopus", "orcarouter/auto-cyber"}}}}
}

func TestRecordDecisionEveryReasonAndOther(t *testing.T) {
	s := newStatsStore()
	for _, reason := range decisionReasons {
		s.recordDecision(statsNow, "glm-5.3", "mid", reason, 0, "")
	}
	s.recordDecision(statsNow, "glm-5.3", "mid", "vision-swap", 0, "")
	s.recordDecision(statsNow, "kimi-k3", "top", "new", 420, "h:aaaa0001")
	s.recordDecision(statsNow, "kimi-k3", "top", "keep", 0, "h:aaaa0001")

	d := s.days["2026-11-25"]
	for _, reason := range decisionReasons {
		want := int64(1)
		if reason == "new" || reason == "keep" {
			want = 2
		}
		if d.Reasons[reason] != want {
			t.Errorf("reason %s = %d, want %d", reason, d.Reasons[reason], want)
		}
	}
	if d.Reasons[reasonOther] != 1 || d.Reasons["vision-swap"] != 0 {
		t.Errorf("unknown reason not bucketed as other: %v", d.Reasons)
	}
	if d.Decisions["kimi-k3"] != 2 || d.Decisions["glm-5.3"] != 11 || d.Tiers["top"] != 2 {
		t.Errorf("decisions %v tiers %v", d.Decisions, d.Tiers)
	}
	if len(d.Sessions) != 1 {
		t.Errorf("sessions = %v, want one hash", d.Sessions)
	}
	snap := s.snapshot(7, statsNow, pluginConfig{})
	if snap.Router.Decisions != 13 || snap.Router.SessionsSum != 1 || snap.Router.JevP50Today == nil {
		t.Errorf("router snapshot %+v", snap.Router)
	}
}

func TestDayKeyUsesBRT(t *testing.T) {
	if got := dayKey(time.Date(2026, 11, 26, 2, 59, 0, 0, time.UTC)); got != "2026-11-25" {
		t.Errorf("02:59 UTC = %s, want 2026-11-25", got)
	}
	if got := dayKey(time.Date(2026, 11, 26, 3, 0, 0, 0, time.UTC)); got != "2026-11-26" {
		t.Errorf("03:00 UTC = %s, want 2026-11-26", got)
	}
}

func TestHistPercentileWithinHalfBucket(t *testing.T) {
	var h hist
	for ms := 1; ms <= 1000; ms++ {
		h.add(time.Duration(ms) * time.Millisecond)
	}
	for p, want := range map[float64]float64{0.5: 500, 0.95: 950} {
		got, ok := h.percentile(p)
		if !ok || math.Abs(got-want)/want > 0.12 {
			t.Errorf("p%.0f = %.1f, want %.0f within 12%%", p*100, got, want)
		}
	}
	var empty hist
	if _, ok := empty.percentile(0.5); ok {
		t.Error("empty histogram reported a percentile")
	}
}

func usage(trace, model, resp string, failed bool, status int, in, cache, out int64) pluginapi.UsageRecord {
	return pluginapi.UsageRecord{
		TraceID: trace, RequestID: trace + "-" + model, Model: model, Alias: model, ResponseModel: resp,
		Provider: "openai-compatible-x", RequestedAt: statsNow, Latency: 800 * time.Millisecond,
		Failed: failed, Failure: pluginapi.UsageFailure{StatusCode: status},
		Detail: pluginapi.UsageDetail{InputTokens: in, CacheReadTokens: cache, OutputTokens: out},
	}
}

func TestUsageKeptOnlyForTaggedTraces(t *testing.T) {
	s := newStatsStore()
	s.tagTrace(map[string]any{"trace_id": "t-router"}, traceTag{router: true})
	s.recordUsage(usage("t-router", "orcarouter/auto(low)", "deepseek-v4.1-flash", false, 0, 100, 0, 10), usagePrice{})
	s.recordUsage(usage("t-direct", "orcarouter/auto", "", false, 0, 100, 0, 10), usagePrice{})
	s.recordUsage(usage("", "orcarouter/auto", "", false, 0, 100, 0, 10), usagePrice{})

	m := s.days["2026-11-25"].Models["orcarouter/auto"]
	if m == nil || m.Calls != 1 || m.RouterCalls != 1 || m.Response["deepseek-v4.1-flash"] != 1 {
		t.Fatalf("model stats = %+v, want exactly the tagged router record", m)
	}
}

// The primary and fallback records of one guarded call share TraceID (host
// 8.0.79, verified on TEST); the pairing must attribute both to the alias.
func TestGuardedPairingByTraceID(t *testing.T) {
	s := newStatsStore()
	cfg := guardedCfg()
	alias := cfg.RefusalFallback[0]
	tag := traceTag{alias: alias.Alias, primary: "claude-opus-5-5"}

	// Call 1: primary refuses as a 400, canopus answers.
	s.tagTrace(map[string]any{"trace_id": "t1"}, tag)
	s.recordSwitch(statsNow, alias.Alias, alias.Primary, "asteras/canopus", refusalErrorReason)
	s.recordUsage(usage("t1", "claude-opus-5-5", "", true, 400, 10, 0, 0), usagePrice{})
	s.recordUsage(usage("t1", "asteras/canopus", "", false, 0, 10, 0, 5), usagePrice{})
	// Call 2: primary answers.
	s.tagTrace(map[string]any{"trace_id": "t2"}, tag)
	s.recordUsage(usage("t2", "claude-opus-5-5", "", false, 0, 10, 0, 5), usagePrice{})
	// Call 3: an ordinary 400 on the primary, no switch.
	s.tagTrace(map[string]any{"trace_id": "t3"}, tag)
	s.recordUsage(usage("t3", "claude-opus-5-5", "", true, 400, 10, 0, 0), usagePrice{})
	// Call 4: empty answer from canopus, auto-cyber fails with 402.
	s.tagTrace(map[string]any{"trace_id": "t4"}, tag)
	s.recordSwitch(statsNow, alias.Alias, alias.Primary, "asteras/canopus", refusalFallbackReason)
	s.recordSwitch(statsNow, alias.Alias, "asteras/canopus", "orcarouter/auto-cyber", emptyAnswerReason)
	s.recordUsage(usage("t4", "claude-opus-5-5", "", false, 0, 10, 0, 1), usagePrice{})
	s.recordUsage(usage("t4", "asteras/canopus", "", false, 0, 10, 0, 0), usagePrice{})
	s.recordUsage(usage("t4", "orcarouter/auto-cyber", "orca/orcacyber-flash-1.0", true, 402, 0, 0, 0), usagePrice{})
	// Not a switch: Jev could not confirm a prose refusal.
	s.recordSwitch(statsNow, alias.Alias, alias.Primary, "asteras/canopus", proseRefusalUnconfirmedLog)

	snap := s.snapshot(7, statsNow, cfg)
	if len(snap.Guarded) != 1 {
		t.Fatalf("guarded = %+v", snap.Guarded)
	}
	g := snap.Guarded[0]
	if strings.Join(g.Chain, ",") != "claude-opus-5-5,asteras/canopus,orcarouter/auto-cyber" {
		t.Errorf("chain = %v", g.Chain)
	}
	if g.Calls[0] != 4 || g.Calls[1] != 2 || g.Calls[2] != 1 {
		t.Errorf("calls = %v, want [4 2 1]", g.Calls)
	}
	if g.Edges[0] != 2 || g.Edges[1] != 1 {
		t.Errorf("edges = %v, want [2 1]", g.Edges)
	}
	if g.Primary400 != 2 || g.Refusal400 != 1 || g.Other400 != 1 {
		t.Errorf("400 split = %d/%d/%d, want 2/1/1", g.Primary400, g.Refusal400, g.Other400)
	}
	if g.Rescue[0] != (rescueStat{Model: "asteras/canopus", OK: 2}) || g.Rescue[1] != (rescueStat{Model: "orcarouter/auto-cyber", Fail: 1}) {
		t.Errorf("rescue = %+v", g.Rescue)
	}
	if g.EmptyAnswers != 1 {
		t.Errorf("empty answers = %d", g.EmptyAnswers)
	}
	last := len(g.RefusalRate) - 1
	if g.RefusalRate[last] == nil || *g.RefusalRate[last] != 50 {
		t.Errorf("refusal rate today = %v, want 50%% (2 switches / 4 primary calls)", g.RefusalRate[last])
	}
	for _, r := range g.Reasons {
		if r.Name == proseRefusalUnconfirmedLog {
			t.Errorf("unconfirmed prose counted as a switch")
		}
	}
}

// openai-compatibility providers report the bare name in Model ("canopus")
// and the provider-prefixed config name in Alias ("asteras/canopus"); the
// guarded chain is keyed by the config name.
func TestGuardedChainKeyUsesAlias(t *testing.T) {
	s := newStatsStore()
	cfg := guardedCfg()
	alias := cfg.RefusalFallback[0]
	tag := traceTag{alias: alias.Alias, primary: "claude-opus-5-5"}

	s.tagTrace(map[string]any{"trace_id": "t1"}, tag)
	s.recordSwitch(statsNow, alias.Alias, alias.Primary, "asteras/canopus", refusalErrorReason)
	s.recordUsage(usage("t1", "claude-opus-5-5", "", true, 400, 10, 0, 0), usagePrice{})
	fallback := usage("t1", "canopus", "", false, 0, 10, 0, 5)
	fallback.Alias = "asteras/canopus"
	s.recordUsage(fallback, usagePrice{})

	snap := s.snapshot(7, statsNow, cfg)
	g := snap.Guarded[0]
	if strings.Join(g.Chain, ",") != "claude-opus-5-5,asteras/canopus,orcarouter/auto-cyber" {
		t.Fatalf("chain = %v", g.Chain)
	}
	if g.Calls[0] != 1 || g.Calls[1] != 1 || g.Calls[2] != 0 {
		t.Errorf("calls = %v, want [1 1 0] (fallback keyed by its alias)", g.Calls)
	}
	if len(g.Rescue) != 2 || g.Rescue[0] != (rescueStat{Model: "asteras/canopus", OK: 1}) {
		t.Errorf("rescue = %+v, want asteras/canopus OK=1", g.Rescue)
	}
	if s.days["2026-11-25"].Models["canopus"] == nil {
		t.Error("per-model line lost the host's bare model name")
	}
}

// Daily files written by 0.4.0 keyed the guarded chain by the bare model
// name ("canopus"); load moves those counts onto the configured chain name.
func TestLoadMigratesGuardedChainKeys(t *testing.T) {
	dir := t.TempDir()
	raw := `{"day":"2026-11-25","guarded":{"opus-guarded":{"calls":{"canopus":5,"claude-opus-5-5":2,"other/model":3},"rescued":{"canopus":5},"unrescued":{"canopus":1}}}}`
	if err := os.WriteFile(filepath.Join(dir, "2026-11-25.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newStatsStore()
	s.dir = dir
	s.load(statsNow, guardedCfg())

	g := s.days["2026-11-25"].Guarded["opus-guarded"]
	if g == nil {
		t.Fatal("guarded day not loaded")
	}
	if g.Calls["asteras/canopus"] != 5 || g.Calls["canopus"] != 0 || g.Calls["claude-opus-5-5"] != 2 || g.Calls["other/model"] != 3 {
		t.Errorf("calls = %v, want canopus moved to asteras/canopus", g.Calls)
	}
	if g.Rescued["asteras/canopus"] != 5 || g.Rescued["canopus"] != 0 {
		t.Errorf("rescued = %v", g.Rescued)
	}
	if g.Unrescued["asteras/canopus"] != 1 || g.Unrescued["canopus"] != 0 {
		t.Errorf("unrescued = %v", g.Unrescued)
	}
	if !s.dirty["2026-11-25"] {
		t.Error("migrated day not marked dirty")
	}
	// A bare name shared by two chain models is ambiguous and stays put.
	g2 := &guardedDay{Calls: map[string]int64{"canopus": 5, "b/canopus": 7}}
	if migrateGuardedKeys(g2, []string{"a/canopus", "b/canopus"}) || g2.Calls["canopus"] != 5 || g2.Calls["a/canopus"] != 0 {
		t.Errorf("ambiguous bare name moved: %v", g2.Calls)
	}
}

func TestPriceLookup(t *testing.T) {
	tb := &table.Table{Models: map[string]table.Model{}}
	m := table.Model{}
	m.Cost.Input, m.Cost.Output = 1, 4
	tb.Models["glm-5.3"] = m
	tb.Models["claude-opus-5-5"] = m
	tb.Models["kimi"] = m
	cache := 0.1
	prices := map[string]modelPrice{
		"glm-5.3":                  {Input: 2, Output: 8, CacheRead: &cache},
		"orca/orcacyber-flash-1.0": {Input: 0.5, Output: 2},
		"claude-opus-5-5":          {Subscription: true},
	}
	cases := []struct {
		name  string
		ids   []string
		want  modelPrice
		found bool
	}{
		{"config wins over table", []string{"", "glm-5.3"}, prices["glm-5.3"], true},
		{"table only", []string{"", "kimi"}, modelPrice{Input: 1, Output: 4}, true},
		{"upstream router priced by response model", []string{"orca/orcacyber-flash-1.0", "orcarouter/auto-cyber"}, prices["orca/orcacyber-flash-1.0"], true},
		{"subscription", []string{"", "claude-opus-5-5"}, modelPrice{Subscription: true}, true},
		{"missing", []string{"x", "y"}, modelPrice{}, false},
	}
	for _, c := range cases {
		got := priceFor(prices, tb, c.ids...)
		if got.found != c.found || got.price.Input != c.want.Input || got.price.Output != c.want.Output || got.price.Subscription != c.want.Subscription {
			t.Errorf("%s: got %+v, want %+v found=%v", c.name, got, c.want, c.found)
		}
	}
}

func TestCostAndNotes(t *testing.T) {
	s := newStatsStore()
	s.tagTrace(map[string]any{"trace_id": "t"}, traceTag{router: true})
	cache := 0.1
	// 1M input of which 400k cached, 100k output: 0.6*2 + 0.4*0.1 + 0.1*8 = 2.04.
	s.recordUsage(usage("t", "glm-5.3", "", false, 0, 1_000_000, 400_000, 100_000), usagePrice{price: modelPrice{Input: 2, Output: 8, CacheRead: &cache}, found: true})
	// No cache-read price: cache billed at input (0.6+0.4)*1 + 0.1*4 = 1.4.
	s.recordUsage(usage("t", "kimi-k3", "", false, 0, 1_000_000, 400_000, 100_000), usagePrice{price: modelPrice{Input: 1, Output: 4}, found: true})
	s.recordUsage(usage("t", "claude-opus-5-5", "", false, 0, 10, 0, 1), usagePrice{price: modelPrice{Subscription: true}, found: true})
	s.recordUsage(usage("t", "z-ai/glm-6", "", false, 0, 10, 0, 1), usagePrice{})
	claude := usage("t", "claude-sonnet-5", "", false, 0, 100, 900, 1)
	claude.Provider = "claude"
	s.recordUsage(claude, usagePrice{})

	got := map[string]modelSnapshot{}
	for _, m := range s.snapshot(7, statsNow, pluginConfig{}).Models {
		got[m.Model] = m
	}
	if c := got["glm-5.3"].CostUSD; c == nil || math.Abs(*c-2.04) > 1e-9 || got["glm-5.3"].CostNote != "" {
		t.Errorf("glm-5.3 cost %v note %q, want 2.04", c, got["glm-5.3"].CostNote)
	}
	if c := got["kimi-k3"].CostUSD; c == nil || math.Abs(*c-1.4) > 1e-9 || got["kimi-k3"].CostNote != "cache ao preço de entrada" {
		t.Errorf("kimi-k3 cost %v note %q, want 1.4 at input price", c, got["kimi-k3"].CostNote)
	}
	if got["claude-opus-5-5"].CostUSD != nil || got["claude-opus-5-5"].CostNote != "assinatura" {
		t.Errorf("subscription = %+v", got["claude-opus-5-5"])
	}
	if got["z-ai/glm-6"].CostUSD != nil || got["z-ai/glm-6"].CostNote != "sem preço" {
		t.Errorf("unpriced = %+v", got["z-ai/glm-6"])
	}
	// Claude reports input without cache reads: 900 / (100 + 900) = 90%.
	if p := got["claude-sonnet-5"].CacheHitPct; p == nil || *p != 90 || got["claude-sonnet-5"].Tokens["input"] != 100 {
		t.Errorf("claude cache hit %v tokens %v", p, got["claude-sonnet-5"].Tokens)
	}
	// OpenAI-compatible input includes cache reads: 400k / 1M = 40%.
	if p := got["glm-5.3"].CacheHitPct; p == nil || *p != 40 || got["glm-5.3"].Tokens["input"] != 600_000 {
		t.Errorf("openai cache hit %v tokens %v", p, got["glm-5.3"].Tokens)
	}
}

func TestPersistenceRoundTripRetentionAndCorruptFile(t *testing.T) {
	dir := t.TempDir()
	s := newStatsStore()
	s.dir = dir
	s.recordDecision(statsNow, "glm-5.3", "mid", "new", 300, "h:00000001")
	s.tagTrace(map[string]any{"trace_id": "t"}, traceTag{router: true})
	s.recordUsage(usage("t", "glm-5.3", "", false, 0, 10, 0, 3), usagePrice{})
	old := statsNow.AddDate(0, 0, -statsRetentionDays)
	s.recordDecision(old, "glm-5.3", "mid", "keep", 0, "")
	s.recordDecision(statsNow.AddDate(0, 0, -(statsRetentionDays-1)), "glm-5.3", "mid", "keep", 0, "")
	// The day older than retention is dropped from memory before the write.
	s.flush(statsNow)
	if err := os.WriteFile(filepath.Join(dir, "2026-11-20.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, dayKey(old)+".json"), []byte(`{"day":"`+dayKey(old)+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(filepath.Join(dir, "2026-11-25.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}

	loaded := newStatsStore()
	loaded.dir = dir
	loaded.load(statsNow, pluginConfig{})
	if _, ok := loaded.days["2026-11-20"]; ok {
		t.Error("corrupt file loaded")
	}
	if _, ok := loaded.days[dayKey(old)]; ok {
		t.Error("day past retention loaded")
	}
	if _, ok := loaded.days[dayKey(statsNow.AddDate(0, 0, -(statsRetentionDays-1)))]; !ok {
		t.Error("oldest retained day not loaded")
	}
	want, _ := json.Marshal(s.snapshot(30, statsNow, pluginConfig{}))
	got, _ := json.Marshal(loaded.snapshot(30, statsNow, pluginConfig{}))
	if string(want) != string(got) {
		t.Errorf("round trip differs:\n got %s\nwant %s", got, want)
	}
	loaded.flush(statsNow)
	if _, err := os.Stat(filepath.Join(dir, dayKey(old)+".json")); !os.IsNotExist(err) {
		t.Errorf("file past retention not deleted: %v", err)
	}
}

func TestSnapshotWeeklyBuckets(t *testing.T) {
	s := newStatsStore()
	s.recordDecision(statsNow, "glm-5.3", "mid", "new", 0, "")
	s.recordDecision(statsNow.AddDate(0, 0, -89), "glm-5.3", "mid", "new", 0, "")
	snap := s.snapshot(90, statsNow, pluginConfig{})
	if snap.Bucket != "week" || len(snap.Labels) != 13 || len(snap.Router.ByModel) != 1 {
		t.Fatalf("bucket %s labels %d by_model %+v", snap.Bucket, len(snap.Labels), snap.Router.ByModel)
	}
	v := snap.Router.ByModel[0].Values
	if v[0] != 1 || v[12] != 1 {
		t.Errorf("weekly values = %v, want first and last week 1", v)
	}
}

func managementCall(t *testing.T, path, query string) pluginapi.ManagementResponse {
	t.Helper()
	req := pluginapi.ManagementRequest{Method: http.MethodGet, Path: path, Query: map[string][]string{}}
	if query != "" {
		req.Query.Set("days", query)
	}
	raw, _ := json.Marshal(req)
	out, err := handleManagement(raw)
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		OK     bool
		Result pluginapi.ManagementResponse
	}
	if err := json.Unmarshal(out, &env); err != nil || !env.OK {
		t.Fatalf("envelope %s: %v", out, err)
	}
	return env.Result
}

// The resource route is not management-authenticated, so it must carry no
// numbers, only the shell, under a CSP that admits exactly its own script.
func TestDashboardResourceHasNoDataAndCSP(t *testing.T) {
	stats.recordDecision(time.Now(), "secret-model-name", "top", "new", 0, "h:deadbeef")
	resp := managementCall(t, dashboardPath, "")
	body := string(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "/v0/management/auto-router/stats") {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if strings.Contains(body, "secret-model-name") || strings.Contains(body, "deadbeef") {
		t.Error("resource response leaks recorded data")
	}
	csp := http.Header(resp.Headers).Get("Content-Security-Policy")
	script := body[strings.Index(body, "<script>")+len("<script>") : strings.LastIndex(body, "</script>")]
	sum := sha256.Sum256([]byte(script))
	for _, want := range []string{"default-src 'none'", "script-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'", "connect-src 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
	if strings.Count(body, "<script") != 1 {
		t.Error("page must hold exactly one script block for the CSP hash")
	}
	if http.Header(resp.Headers).Get("X-Content-Type-Options") != "nosniff" {
		t.Error("missing nosniff")
	}
}

func TestStatsRouteDaysAndUnknownPath(t *testing.T) {
	if r := managementCall(t, statsRoutePath, "14"); r.StatusCode != http.StatusBadRequest {
		t.Errorf("days=14 status %d, want 400", r.StatusCode)
	}
	r := managementCall(t, statsRoutePath, "30")
	var snap statsSnapshot
	if r.StatusCode != http.StatusOK || json.Unmarshal(r.Body, &snap) != nil || snap.Days != 30 || len(snap.Labels) != 30 {
		t.Errorf("days=30 status %d days %d labels %d", r.StatusCode, snap.Days, len(snap.Labels))
	}
	if r := managementCall(t, "/v0/management/auto-router/other", ""); r.StatusCode != http.StatusNotFound {
		t.Errorf("unknown path status %d", r.StatusCode)
	}
	reg := managementRegistration()
	if len(reg.Routes) != 1 || reg.Routes[0].Path != statsRoutePath || len(reg.Resources) != 1 || reg.Resources[0].Menu != "Auto Router" {
		t.Errorf("registration = %+v", reg)
	}
}
