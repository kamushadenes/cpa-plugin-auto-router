package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kamushadenes/cpa-plugin-auto-router/internal/table"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// The dashboard aggregate: one in-memory record per BRT day, fed by router
// decisions, guarded switches and the host's usage records, flushed to one
// JSON file per day. Nothing here holds a body, a prompt, a key or a raw
// session ID.

const (
	statsRetentionDays = 91
	statsFlushInterval = time.Minute
	histBuckets        = 64
	histRatio          = 1.25
	// A usage record arrives after its call ends; a long stream can run for
	// many minutes, so a trace tag lives one hour.
	traceTagTTL = time.Hour
	// shortcut: past this many live trace tags new calls are not attributed;
	// raise it if a busy proxy shows gaps.
	maxTraceTags = 100000
	reasonOther  = "other"
	// defaultStatsDir is relative to the proxy's working directory.
	defaultStatsDir = "plugins/auto-router/stats"
)

// shortcut: fixed UTC-3 because Brazil has had no DST since 2019; switch to
// time.LoadLocation("America/Sao_Paulo") if DST returns.
var brt = time.FixedZone("BRT", -3*60*60)

// decisionReasons is every routing reason the code emits; anything else
// counts as "other".
var decisionReasons = []string{
	"keep", "new", "escalate-tier", "escalate-thinking", "fallback", "model-gone",
	"jev-unavailable", "reclassified", "failover", "context_overflow_risk",
}

// hist is a log-bucket latency histogram in milliseconds. Bucket 0 holds
// values under 1 ms; bucket i holds [1.25^(i-1), 1.25^i).
type hist [histBuckets]int64

func histIndex(ms float64) int {
	if ms < 1 {
		return 0
	}
	return min(int(math.Log(ms)/math.Log(histRatio))+1, histBuckets-1)
}

func (h *hist) add(d time.Duration) {
	h[histIndex(float64(d)/float64(time.Millisecond))]++
}

func (h *hist) merge(o *hist) {
	for i := range h {
		h[i] += o[i]
	}
}

// percentile returns the geometric middle of the bucket holding the p-th
// value, so the error is at most half a bucket (about 12%).
func (h *hist) percentile(p float64) (float64, bool) {
	var total int64
	for _, n := range h {
		total += n
	}
	if total == 0 {
		return 0, false
	}
	rank := int64(math.Ceil(p * float64(total)))
	var seen int64
	for i, n := range h {
		seen += n
		if seen >= rank {
			if i == 0 {
				return 0, true
			}
			return math.Pow(histRatio, float64(i)-0.5), true
		}
	}
	return 0, false
}

// hashSet stores the 4-byte session hashes hashSession produces, as a sorted
// JSON array.
type hashSet map[string]struct{}

func (s hashSet) MarshalJSON() ([]byte, error) {
	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return json.Marshal(keys)
}

func (s *hashSet) UnmarshalJSON(raw []byte) error {
	var keys []string
	if err := json.Unmarshal(raw, &keys); err != nil {
		return err
	}
	*s = make(hashSet, len(keys))
	for _, k := range keys {
		(*s)[k] = struct{}{}
	}
	return nil
}

type dayStats struct {
	Day       string                 `json:"day"`
	Decisions map[string]int64       `json:"decisions,omitempty"`
	Tiers     map[string]int64       `json:"tiers,omitempty"`
	Reasons   map[string]int64       `json:"reasons,omitempty"`
	Jev       hist                   `json:"jev"`
	Sessions  hashSet                `json:"sessions,omitempty"`
	Guarded   map[string]*guardedDay `json:"guarded,omitempty"`
	Models    map[string]*modelDay   `json:"models,omitempty"`
}

type guardedDay struct {
	// Calls counts usage records per chain model.
	Calls map[string]int64 `json:"calls,omitempty"`
	// Switches counts switch log events keyed "from|to|reason".
	Switches map[string]int64 `json:"switches,omitempty"`
	// Rescued and Unrescued count fallback records that answered or failed.
	Rescued    map[string]int64 `json:"rescued,omitempty"`
	Unrescued  map[string]int64 `json:"unrescued,omitempty"`
	Primary400 int64            `json:"primary_400,omitempty"`
}

type modelDay struct {
	Calls        int64            `json:"calls"`
	RouterCalls  int64            `json:"router_calls,omitempty"`
	GuardedCalls int64            `json:"guarded_calls,omitempty"`
	Status       map[string]int64 `json:"status,omitempty"`
	Uncached     int64            `json:"uncached,omitempty"`
	CacheRead    int64            `json:"cache_read,omitempty"`
	CacheCreate  int64            `json:"cache_create,omitempty"`
	Output       int64            `json:"output,omitempty"`
	CostUSD      float64          `json:"cost_usd,omitempty"`
	Priced       int64            `json:"priced,omitempty"`
	Subscription int64            `json:"subscription,omitempty"`
	CacheAtInput int64            `json:"cache_at_input,omitempty"`
	Latency      hist             `json:"latency"`
	TTFT         hist             `json:"ttft"`
	Response     map[string]int64 `json:"response,omitempty"`
	Hours        [24]int64        `json:"hours"`
}

// traceTag marks one inbound request this plugin executed, so its usage
// records can be attributed. Primary is the alias primary without a thinking
// suffix; Escalated means the chain skipped straight to the last fallback.
type traceTag struct {
	router    bool
	alias     string
	primary   string
	escalated bool
	at        time.Time
}

type statsStore struct {
	mu    sync.Mutex
	dir   string
	days  map[string]*dayStats
	dirty map[string]bool
	tags  map[string]traceTag
	once  sync.Once
	stop  chan struct{}
	done  chan struct{}
}

func newStatsStore() *statsStore {
	return &statsStore{days: map[string]*dayStats{}, dirty: map[string]bool{}, tags: map[string]traceTag{}, stop: make(chan struct{}), done: make(chan struct{})}
}

var stats = newStatsStore()

func dayKey(t time.Time) string {
	return t.In(brt).Format(time.DateOnly)
}

// day returns the bucket for key and marks it dirty. Callers hold s.mu.
func (s *statsStore) day(key string) *dayStats {
	d := s.days[key]
	if d == nil {
		d = &dayStats{Day: key}
		s.days[key] = d
	}
	s.dirty[key] = true
	return d
}

func inc(m *map[string]int64, key string, n int64) {
	if *m == nil {
		*m = map[string]int64{}
	}
	(*m)[key] += n
}

func (d *dayStats) guarded(alias string) *guardedDay {
	if d.Guarded == nil {
		d.Guarded = map[string]*guardedDay{}
	}
	g := d.Guarded[alias]
	if g == nil {
		g = &guardedDay{}
		d.Guarded[alias] = g
	}
	return g
}

func (d *dayStats) model(id string) *modelDay {
	if d.Models == nil {
		d.Models = map[string]*modelDay{}
	}
	m := d.Models[id]
	if m == nil {
		m = &modelDay{}
		d.Models[id] = m
	}
	return m
}

// recordDecision counts one routing decision log line.
func (s *statsStore) recordDecision(now time.Time, model, tier, reason string, jevMillis int64, sessionHash string) {
	if !slices.Contains(decisionReasons, reason) {
		reason = reasonOther
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.day(dayKey(now))
	inc(&d.Decisions, model, 1)
	inc(&d.Tiers, tier, 1)
	inc(&d.Reasons, reason, 1)
	if jevMillis > 0 {
		d.Jev.add(time.Duration(jevMillis) * time.Millisecond)
	}
	if sessionHash != "" {
		if d.Sessions == nil {
			d.Sessions = hashSet{}
		}
		d.Sessions[sessionHash] = struct{}{}
	}
}

// recordSwitch counts one guarded switch log line. The unconfirmed-prose
// line is not a switch and is skipped.
func (s *statsStore) recordSwitch(now time.Time, alias, from, to, reason string) {
	if reason == proseRefusalUnconfirmedLog {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.day(dayKey(now)).guarded(alias)
	inc(&g.Switches, stripThinkingSuffix(from)+"|"+stripThinkingSuffix(to)+"|"+reason, 1)
}

// pairKey is the pairing rule: on host 8.0.79 every usage record of one
// inbound request carries that request's TraceID (verified on TEST: the
// primary and fallback records of a guarded switch share TraceID, their
// RequestIDs differ), and ExecutorRequest.Metadata["trace_id"] carries the
// same value, so a tag set at execute time attributes the later records.
func pairKey(rec pluginapi.UsageRecord) string {
	return rec.TraceID
}

func traceIDFromMetadata(metadata map[string]any) string {
	id, _ := metadata["trace_id"].(string)
	return strings.TrimSpace(id)
}

// tagTrace marks the executing request so its usage records are kept.
func (s *statsStore) tagTrace(metadata map[string]any, tag traceTag) {
	id := traceIDFromMetadata(metadata)
	if id == "" {
		return
	}
	tag.at = time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tags[id]; !ok && len(s.tags) >= maxTraceTags {
		return
	}
	s.tags[id] = tag
}

// usagePrice is the resolved price for one record.
type usagePrice struct {
	price modelPrice
	found bool
}

// recordUsage folds one host usage record into the aggregate when it belongs
// to a request this plugin executed. Records unrelated to this plugin are
// ignored.
func (s *statsStore) recordUsage(rec pluginapi.UsageRecord, price usagePrice) {
	model := stripThinkingSuffix(rec.Model)
	at := rec.RequestedAt
	if at.IsZero() {
		at = time.Now()
	}
	status := "200"
	if rec.Failed {
		status = "error"
		if rec.Failure.StatusCode > 0 {
			status = itoa(rec.Failure.StatusCode)
		}
	}
	detail := rec.Detail
	uncached := detail.InputTokens
	// Claude reports input_tokens without cache reads; the OpenAI-compatible
	// providers include them (verified on TEST, see the design's Cost rule).
	if !strings.EqualFold(rec.Provider, "claude") {
		uncached = max(0, detail.InputTokens-detail.CacheReadTokens)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	tag, ok := s.tags[pairKey(rec)]
	if !ok {
		return
	}
	m := s.day(dayKey(at)).model(model)
	m.Calls++
	inc(&m.Status, status, 1)
	m.Uncached += uncached
	m.CacheRead += detail.CacheReadTokens
	m.CacheCreate += detail.CacheCreationTokens
	m.Output += detail.OutputTokens
	switch {
	case price.found && price.price.Subscription:
		m.Subscription++
	case price.found:
		cacheRate := price.price.Input
		if price.price.CacheRead != nil {
			cacheRate = *price.price.CacheRead
		} else if detail.CacheReadTokens > 0 {
			m.CacheAtInput++
		}
		m.CostUSD += (float64(uncached+detail.CacheCreationTokens)*price.price.Input + float64(detail.CacheReadTokens)*cacheRate + float64(detail.OutputTokens)*price.price.Output) / 1e6
		m.Priced++
	}
	if rec.Latency > 0 {
		m.Latency.add(rec.Latency)
	}
	if rec.TTFT > 0 {
		m.TTFT.add(rec.TTFT)
	}
	if rec.ResponseModel != "" {
		inc(&m.Response, rec.ResponseModel, 1)
	}
	m.Hours[at.In(brt).Hour()]++
	if tag.router {
		m.RouterCalls++
	}
	if tag.alias == "" {
		return
	}
	m.GuardedCalls++
	g := s.day(dayKey(at)).guarded(tag.alias)
	// The chain is keyed by the config name, which openai-compatibility providers report in Alias, not in Model.
	key := model
	if rec.Alias != "" {
		key = stripThinkingSuffix(rec.Alias)
	}
	inc(&g.Calls, key, 1)
	switch {
	case key == tag.primary:
		if status == "400" {
			g.Primary400++
		}
	case !tag.escalated && rec.Failed:
		inc(&g.Unrescued, key, 1)
	case !tag.escalated:
		inc(&g.Rescued, key, 1)
	}
}

func itoa(n int) string {
	raw, _ := json.Marshal(n)
	return string(raw)
}

// modelPrice is one entry of the `prices` config map, in US$ per 1M tokens.
// A nil CacheRead bills cache reads at the input price.
type modelPrice struct {
	Input        float64  `yaml:"input"`
	Output       float64  `yaml:"output"`
	CacheRead    *float64 `yaml:"cache_read"`
	Subscription bool     `yaml:"subscription"`
}

// priceFor returns the price of the first id that has one, config map first,
// then the models.yaml cost. Callers pass the ResponseModel first so an
// upstream router is billed for the model that actually answered.
func priceFor(prices map[string]modelPrice, tb *table.Table, ids ...string) usagePrice {
	for _, id := range ids {
		if id == "" {
			continue
		}
		if p, ok := prices[id]; ok {
			return usagePrice{price: p, found: true}
		}
		if tb != nil {
			if m, ok := tb.Models[id]; ok && (m.Cost.Input > 0 || m.Cost.Output > 0) {
				return usagePrice{price: modelPrice{Input: m.Cost.Input, Output: m.Cost.Output}, found: true}
			}
		}
	}
	return usagePrice{}
}

// handleUsage is the usage.handle entry point. It runs on the host's usage
// dispatch, not on the request path, and does no I/O.
func handleUsage(raw []byte) ([]byte, error) {
	var rec pluginapi.UsageRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, err
	}
	tb, _ := loadedTable()
	stats.recordUsage(rec, priceFor(loadedConfig().Prices, tb, rec.ResponseModel, stripThinkingSuffix(rec.Model)))
	return okEnvelope(struct{}{})
}

// startStats loads the stored days once and starts the minute flush. Later
// reconfigures only change the directory future flushes write to.
func startStats(dir string) {
	stats.mu.Lock()
	stats.dir = dir
	stats.mu.Unlock()
	stats.once.Do(func() {
		stats.load(time.Now(), loadedConfig())
		go stats.loop()
	})
}

// stopStats stops the flush loop and writes every pending day.
func stopStats() {
	select {
	case <-stats.stop:
	default:
		close(stats.stop)
	}
	stats.flush(time.Now())
}

func (s *statsStore) loop() {
	ticker := time.NewTicker(statsFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.flush(time.Now())
		case <-s.stop:
			return
		}
	}
}

func oldestKept(now time.Time) string {
	return dayKey(now.AddDate(0, 0, -(statsRetentionDays - 1)))
}

func statsFileDay(name string) (string, bool) {
	day, ok := strings.CutSuffix(name, ".json")
	if !ok {
		return "", false
	}
	if _, err := time.Parse(time.DateOnly, day); err != nil {
		return "", false
	}
	return day, true
}

// aliasChain is the configured chain in chain-key form: primary first, then fallbacks, thinking suffixes removed.
func aliasChain(a refusalFallbackAlias) []string {
	chain := []string{stripThinkingSuffix(a.Primary)}
	for _, f := range a.Fallback {
		chain = append(chain, stripThinkingSuffix(f))
	}
	return chain
}

// migrateGuardedKeys is compatibility with the daily files written by 0.4.0, which keyed the guarded chain by the bare model name.
func migrateGuardedKeys(g *guardedDay, chain []string) bool {
	moved := migrateKeyMap(&g.Calls, chain)
	if migrateKeyMap(&g.Rescued, chain) {
		moved = true
	}
	return migrateKeyMap(&g.Unrescued, chain) || moved
}

// migrateKeyMap moves a key onto its chain model only when exactly one chain model has that bare name.
func migrateKeyMap(m *map[string]int64, chain []string) bool {
	moved := false
	for k, n := range *m {
		if slices.Contains(chain, k) {
			continue
		}
		target := ""
		for _, c := range chain {
			if k == c[strings.LastIndex(c, "/")+1:] {
				if target != "" {
					target = "" // bare name shared by two chain models: ambiguous, leave the key
					break
				}
				target = c
			}
		}
		if target != "" {
			inc(m, target, n)
			delete(*m, k)
			moved = true
		}
	}
	return moved
}

// load reads the stored days. A corrupt file is logged and skipped.
func (s *statsStore) load(now time.Time, cfg pluginConfig) {
	s.mu.Lock()
	dir := s.dir
	s.mu.Unlock()
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			hostLog("", "warn", "auto-router stats directory unreadable", map[string]any{"error": err.Error()})
		}
		return
	}
	oldest := oldestKept(now)
	chains := map[string][]string{}
	for _, a := range cfg.RefusalFallback {
		chains[a.Alias] = aliasChain(a)
	}
	loaded := map[string]*dayStats{}
	migrated := map[string]bool{}
	for _, entry := range entries {
		day, ok := statsFileDay(entry.Name())
		if !ok || day < oldest {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		var d dayStats
		if err == nil {
			err = json.Unmarshal(raw, &d)
		}
		if err != nil || d.Day != day {
			hostLog("", "warn", "auto-router stats file skipped", map[string]any{"file": entry.Name()})
			continue
		}
		moved := false
		for alias, chain := range chains {
			if g := d.Guarded[alias]; g != nil {
				moved = migrateGuardedKeys(g, chain) || moved
			}
		}
		loaded[day] = &d
		migrated[day] = moved
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for day, d := range loaded {
		if _, ok := s.days[day]; !ok {
			s.days[day] = d
			if migrated[day] {
				s.dirty[day] = true // the flush must replace the 0.4.0 file with the migrated keys
			}
		}
	}
}

// flush writes the dirty days and applies retention. JSON is built under the
// lock; files are written after it is released.
func (s *statsStore) flush(now time.Time) {
	oldest := oldestKept(now)
	s.mu.Lock()
	dir := s.dir
	for day := range s.days {
		if day < oldest {
			delete(s.days, day)
			delete(s.dirty, day)
		}
	}
	for id, tag := range s.tags {
		if now.Sub(tag.at) > traceTagTTL {
			delete(s.tags, id)
		}
	}
	pending := map[string][]byte{}
	if dir != "" {
		for day := range s.dirty {
			raw, err := json.Marshal(s.days[day])
			if err == nil {
				pending[day] = raw
			}
		}
		clear(s.dirty)
	}
	s.mu.Unlock()
	if dir == "" {
		return
	}
	for day, raw := range pending {
		if err := writeFileAtomic(dir, day+".json", raw); err != nil {
			hostLog("", "warn", "auto-router stats write failed", map[string]any{"file": day + ".json", "error": err.Error()})
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if day, ok := statsFileDay(entry.Name()); ok && day < oldest {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}

// writeFileAtomic writes a 0600 file through a temp file and a rename.
func writeFileAtomic(dir, name string, raw []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, name))
}
