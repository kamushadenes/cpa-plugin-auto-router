package main

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	statsRoutePath     = "/v0/management/auto-router/stats"
	dashboardPath      = "/v0/resource/plugins/auto-router/dashboard"
	dashboardMenuLabel = "Auto Router"
)

//go:embed dashboard.html
var dashboardHTML string

// dashboardCSP allows only the page's own inline script (by hash), inline
// styles, and requests to this origin.
var dashboardCSP = func() string {
	start := strings.Index(dashboardHTML, "<script>") + len("<script>")
	end := strings.LastIndex(dashboardHTML, "</script>")
	sum := sha256.Sum256([]byte(dashboardHTML[start:end]))
	return "default-src 'none'; script-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'; style-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'none'"
}()

// managementRoute is the wire shape of pluginapi.ManagementRoute and
// ResourceRoute without the Handler, which the host fills in.
type managementRoute struct {
	Method      string `json:"Method,omitempty"`
	Path        string `json:"Path"`
	Menu        string `json:"Menu,omitempty"`
	Description string `json:"Description,omitempty"`
}

type managementRegistrationResponse struct {
	Routes    []managementRoute `json:"routes"`
	Resources []managementRoute `json:"resources"`
}

func managementRegistration() managementRegistrationResponse {
	return managementRegistrationResponse{
		Routes:    []managementRoute{{Method: http.MethodGet, Path: statsRoutePath}},
		Resources: []managementRoute{{Path: dashboardPath, Menu: dashboardMenuLabel, Description: "Auto Router decisions, guarded fallbacks, cost and latency."}},
	}
}

// handleManagement serves both routes. The host sends a request here only
// for a registered path: the stats path only after the management key check,
// the dashboard path without any check, so the dashboard response carries no
// data.
func handleManagement(raw []byte) ([]byte, error) {
	var req pluginapi.ManagementRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	switch req.Path {
	case dashboardPath:
		return okEnvelope(pluginapi.ManagementResponse{
			StatusCode: http.StatusOK,
			Headers: http.Header{
				"Content-Type":            {"text/html; charset=utf-8"},
				"Content-Security-Policy": {dashboardCSP},
				"X-Content-Type-Options":  {"nosniff"},
				"Cache-Control":           {"no-store"},
			},
			Body: []byte(dashboardHTML),
		})
	case statsRoutePath:
		days := 7
		if raw := req.Query.Get("days"); raw != "" {
			days, _ = strconv.Atoi(raw)
		}
		if days != 7 && days != 30 && days != 90 {
			return okEnvelope(jsonResponse(http.StatusBadRequest, map[string]string{"error": "days must be 7, 30 or 90"}))
		}
		return okEnvelope(jsonResponse(http.StatusOK, stats.snapshot(days, time.Now(), loadedConfig())))
	default:
		return okEnvelope(jsonResponse(http.StatusNotFound, map[string]string{"error": "not found"}))
	}
}

func jsonResponse(status int, v any) pluginapi.ManagementResponse {
	body, _ := json.Marshal(v)
	return pluginapi.ManagementResponse{
		StatusCode: status,
		Headers: http.Header{
			"Content-Type":           {"application/json"},
			"X-Content-Type-Options": {"nosniff"},
			"Cache-Control":          {"no-store"},
		},
		Body: body,
	}
}

// Snapshot shapes: everything the page draws, already grouped. Series hold
// one value per bucket (a day, or a week for 90 days); nil means no data.

type namedCount struct {
	Name string `json:"name"`
	N    int64  `json:"n"`
}

type namedSeries struct {
	Name   string  `json:"name"`
	Values []int64 `json:"values"`
}

type statsSnapshot struct {
	GeneratedAt string          `json:"generated_at"`
	Days        int             `json:"days"`
	Bucket      string          `json:"bucket"`
	Labels      []string        `json:"labels"`
	HourLabels  []string        `json:"hour_labels"`
	Router      routerSnapshot  `json:"router"`
	Guarded     []guardedAlias  `json:"guarded"`
	Models      []modelSnapshot `json:"models"`
}

type routerSnapshot struct {
	Decisions   int64         `json:"decisions"`
	SessionsSum int64         `json:"sessions_daily_sum"`
	JevP50Today *float64      `json:"jev_p50_today"`
	ByModel     []namedSeries `json:"by_model"`
	Tiers       []namedCount  `json:"tiers"`
	Reasons     []namedCount  `json:"reasons"`
	JevP50      []*float64    `json:"jev_p50"`
	JevP95      []*float64    `json:"jev_p95"`
	Sessions    []int64       `json:"sessions"`
}

type guardedAlias struct {
	Alias          string        `json:"alias"`
	Chain          []string      `json:"chain"`
	Calls          []int64       `json:"calls"`
	Edges          []int64       `json:"edges"`
	ByDestination  []namedSeries `json:"switches_by_destination"`
	Reasons        []namedCount  `json:"reasons"`
	RefusalRate    []*float64    `json:"refusal_rate"`
	Primary400     int64         `json:"primary_400"`
	Refusal400     int64         `json:"primary_400_refusal"`
	Other400       int64         `json:"primary_400_other"`
	Rescue         []rescueStat  `json:"rescue"`
	FallbackErrors int64         `json:"fallback_errors"`
	EmptyAnswers   int64         `json:"empty_answers"`
	ToolEscalation int64         `json:"tool_escalations"`
	AfterOutput    int64         `json:"refusal_after_output"`
}

type rescueStat struct {
	Model string `json:"model"`
	OK    int64  `json:"ok"`
	Fail  int64  `json:"fail"`
}

type modelSnapshot struct {
	Model       string           `json:"model"`
	Router      bool             `json:"router"`
	Guarded     bool             `json:"guarded"`
	Calls       int64            `json:"calls"`
	CostUSD     *float64         `json:"cost_usd"`
	CostPer1K   *float64         `json:"cost_per_1k"`
	CostNote    string           `json:"cost_note"`
	LatencyP50  *float64         `json:"latency_p50"`
	LatencyP95  *float64         `json:"latency_p95"`
	TTFTP50     *float64         `json:"ttft_p50"`
	OutPerCall  float64          `json:"out_per_call"`
	CacheHitPct *float64         `json:"cache_hit_pct"`
	Tokens      map[string]int64 `json:"tokens"`
	ResponseMix []namedCount     `json:"response_mix"`
	P95Series   []*float64       `json:"p95_series"`
	Status      []namedCount     `json:"status"`
	Non200Pct   float64          `json:"non200_pct"`
	Hours       []int64          `json:"hours"`
}

func pct(part, whole int64) *float64 {
	if whole <= 0 {
		return nil
	}
	v := 100 * float64(part) / float64(whole)
	return &v
}

func pctl(h *hist, p float64) *float64 {
	if v, ok := h.percentile(p); ok {
		return &v
	}
	return nil
}

func sortedCounts(m map[string]int64) []namedCount {
	out := make([]namedCount, 0, len(m))
	for k, n := range m {
		if n > 0 {
			out = append(out, namedCount{Name: k, N: n})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].N != out[j].N {
			return out[i].N > out[j].N
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func seriesFrom(m map[string][]int64) []namedSeries {
	out := make([]namedSeries, 0, len(m))
	for k, v := range m {
		out = append(out, namedSeries{Name: k, Values: v})
	}
	total := func(v []int64) (n int64) {
		for _, x := range v {
			n += x
		}
		return n
	}
	sort.Slice(out, func(i, j int) bool {
		ti, tj := total(out[i].Values), total(out[j].Values)
		if ti != tj {
			return ti > tj
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func addTo(m map[string][]int64, key string, buckets, b int, n int64) {
	if m[key] == nil {
		m[key] = make([]int64, buckets)
	}
	m[key][b] += n
}

// snapshot builds the page data for the last `days` BRT days ending today.
func (s *statsStore) snapshot(days int, now time.Time, cfg pluginConfig) statsSnapshot {
	step := 1
	bucket := "day"
	if days == 90 {
		step, bucket = 7, "week"
	}
	buckets := (days + step - 1) / step
	keys := make([]string, days)
	for i := range days {
		keys[i] = dayKey(now.AddDate(0, 0, i-days+1))
	}
	labels := make([]string, buckets)
	for b := range buckets {
		labels[b] = keys[b*step][8:10] + "/" + keys[b*step][5:7]
	}
	nowBRT := now.In(brt)
	hourLabels := make([]string, 24)
	for i := range 24 {
		hourLabels[i] = nowBRT.Add(time.Duration(i-23) * time.Hour).Format("15")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	snap := statsSnapshot{GeneratedAt: now.In(brt).Format(time.RFC3339), Days: days, Bucket: bucket, Labels: labels, HourLabels: hourLabels}

	// Router.
	byModel := map[string][]int64{}
	tiers, reasons := map[string]int64{}, map[string]int64{}
	jevBuckets := make([]hist, buckets)
	sessionBuckets := make([]hashSet, buckets)
	snap.Router.Sessions = make([]int64, buckets)
	for i, key := range keys {
		d := s.days[key]
		if d == nil {
			continue
		}
		b := i / step
		for m, n := range d.Decisions {
			addTo(byModel, m, buckets, b, n)
			snap.Router.Decisions += n
		}
		for k, n := range d.Tiers {
			tiers[k] += n
		}
		for k, n := range d.Reasons {
			reasons[k] += n
		}
		jevBuckets[b].merge(&d.Jev)
		snap.Router.SessionsSum += int64(len(d.Sessions))
		if sessionBuckets[b] == nil {
			sessionBuckets[b] = hashSet{}
		}
		for h := range d.Sessions {
			sessionBuckets[b][h] = struct{}{}
		}
	}
	snap.Router.ByModel = seriesFrom(byModel)
	snap.Router.Tiers = sortedCounts(tiers)
	snap.Router.Reasons = sortedCounts(reasons)
	for b := range buckets {
		snap.Router.JevP50 = append(snap.Router.JevP50, pctl(&jevBuckets[b], 0.5))
		snap.Router.JevP95 = append(snap.Router.JevP95, pctl(&jevBuckets[b], 0.95))
		snap.Router.Sessions[b] = int64(len(sessionBuckets[b]))
	}
	if d := s.days[dayKey(now)]; d != nil {
		snap.Router.JevP50Today = pctl(&d.Jev, 0.5)
	}

	snap.Guarded = s.guardedSnapshot(keys, step, buckets, cfg)
	snap.Models = s.modelSnapshots(keys, step, buckets, now)
	return snap
}

// guardedSnapshot covers every configured alias plus any alias still present
// in the window after it was removed from config.
func (s *statsStore) guardedSnapshot(keys []string, step, buckets int, cfg pluginConfig) []guardedAlias {
	chains := map[string][]string{}
	order := []string{}
	for _, a := range cfg.RefusalFallback {
		chain := []string{stripThinkingSuffix(a.Primary)}
		for _, f := range a.Fallback {
			chain = append(chain, stripThinkingSuffix(f))
		}
		chains[a.Alias] = chain
		order = append(order, a.Alias)
	}
	var removed []string
	for _, key := range keys {
		if d := s.days[key]; d != nil {
			for alias := range d.Guarded {
				if _, ok := chains[alias]; !ok && !slices.Contains(removed, alias) {
					removed = append(removed, alias)
				}
			}
		}
	}
	sort.Strings(removed)
	out := make([]guardedAlias, 0, len(order)+len(removed))
	for _, alias := range append(order, removed...) {
		calls, reasons := map[string]int64{}, map[string]int64{}
		edges := map[[2]string]int64{}
		byDest := map[string][]int64{}
		rescued, unrescued := map[string]int64{}, map[string]int64{}
		primary := ""
		if chain := chains[alias]; len(chain) > 0 {
			primary = chain[0]
		}
		primaryCalls := make([]int64, buckets)
		primarySwitches := make([]int64, buckets)
		g := guardedAlias{Alias: alias}
		for i, key := range keys {
			d := s.days[key]
			if d == nil || d.Guarded[alias] == nil {
				continue
			}
			b := i / step
			gd := d.Guarded[alias]
			for m, n := range gd.Calls {
				calls[m] += n
				if m == primary {
					primaryCalls[b] += n
				}
			}
			for k, n := range gd.Switches {
				parts := strings.SplitN(k, "|", 3)
				if len(parts) != 3 {
					continue
				}
				from, to, reason := parts[0], parts[1], parts[2]
				reasons[reason] += n
				switch reason {
				case fallbackErrorReason:
					g.FallbackErrors += n
				case emptyAnswerReason:
					g.EmptyAnswers += n
				case toolErrorEscalationReason:
					g.ToolEscalation += n
				case refusalAfterOutputLog:
					g.AfterOutput += n
				}
				if reason == refusalAfterOutputLog {
					continue // reported, never switched
				}
				edges[[2]string{from, to}] += n
				addTo(byDest, to, buckets, b, n)
				if from == primary && reason != toolErrorEscalationReason {
					primarySwitches[b] += n
					if reason == refusalErrorReason {
						g.Refusal400 += n
					}
				}
			}
			for m, n := range gd.Rescued {
				rescued[m] += n
			}
			for m, n := range gd.Unrescued {
				unrescued[m] += n
			}
			g.Primary400 += gd.Primary400
		}
		chain := chains[alias]
		if chain == nil {
			for m := range calls {
				chain = append(chain, m)
			}
			sort.Strings(chain)
		}
		g.Chain = chain
		for i, m := range chain {
			g.Calls = append(g.Calls, calls[m])
			if i > 0 {
				g.Edges = append(g.Edges, edges[[2]string{chain[i-1], m}])
				g.Rescue = append(g.Rescue, rescueStat{Model: m, OK: rescued[m], Fail: unrescued[m]})
			}
		}
		g.ByDestination = seriesFrom(byDest)
		g.Reasons = sortedCounts(reasons)
		for b := range buckets {
			g.RefusalRate = append(g.RefusalRate, pct(primarySwitches[b], primaryCalls[b]))
		}
		g.Other400 = max(0, g.Primary400-g.Refusal400)
		out = append(out, g)
	}
	return out
}

func (s *statsStore) modelSnapshots(keys []string, step, buckets int, now time.Time) []modelSnapshot {
	type acc struct {
		m       modelDay
		p95     []hist
		hours   []int64
		touched bool
	}
	all := map[string]*acc{}
	today, yesterday := dayKey(now), dayKey(now.AddDate(0, 0, -1))
	hour := now.In(brt).Hour()
	for i, key := range keys {
		d := s.days[key]
		if d == nil {
			continue
		}
		b := i / step
		for id, md := range d.Models {
			a := all[id]
			if a == nil {
				a = &acc{p95: make([]hist, buckets), hours: make([]int64, 24)}
				all[id] = a
			}
			a.m.Calls += md.Calls
			a.m.RouterCalls += md.RouterCalls
			a.m.GuardedCalls += md.GuardedCalls
			for k, n := range md.Status {
				inc(&a.m.Status, k, n)
			}
			a.m.Uncached += md.Uncached
			a.m.CacheRead += md.CacheRead
			a.m.CacheCreate += md.CacheCreate
			a.m.Output += md.Output
			a.m.CostUSD += md.CostUSD
			a.m.Priced += md.Priced
			a.m.Subscription += md.Subscription
			a.m.CacheAtInput += md.CacheAtInput
			a.m.Latency.merge(&md.Latency)
			a.m.TTFT.merge(&md.TTFT)
			for k, n := range md.Response {
				inc(&a.m.Response, k, n)
			}
			a.p95[b].merge(&md.Latency)
			// Last 24 hours: today's hours up to now, yesterday's after now.
			for h := range 24 {
				switch {
				case key == today && h <= hour:
					a.hours[h-hour+23] += md.Hours[h]
				case key == yesterday && h > hour:
					a.hours[h-hour-1] += md.Hours[h]
				}
			}
		}
	}
	out := make([]modelSnapshot, 0, len(all))
	for id, a := range all {
		m := a.m
		ms := modelSnapshot{
			Model:       id,
			Router:      m.RouterCalls > 0,
			Guarded:     m.GuardedCalls > 0,
			Calls:       m.Calls,
			LatencyP50:  pctl(&m.Latency, 0.5),
			LatencyP95:  pctl(&m.Latency, 0.95),
			TTFTP50:     pctl(&m.TTFT, 0.5),
			CacheHitPct: pct(m.CacheRead, m.Uncached+m.CacheRead+m.CacheCreate),
			Tokens:      map[string]int64{"input": m.Uncached, "cache": m.CacheRead, "output": m.Output},
			ResponseMix: sortedCounts(m.Response),
			Hours:       a.hours,
		}
		if m.Calls > 0 {
			ms.OutPerCall = float64(m.Output) / float64(m.Calls)
		}
		switch {
		case m.Subscription > 0 && m.Priced == 0:
			ms.CostNote = "assinatura"
		case m.Priced == 0:
			ms.CostNote = "sem preço"
		default:
			cost := m.CostUSD
			per1k := 1000 * cost / float64(m.Priced)
			ms.CostUSD, ms.CostPer1K = &cost, &per1k
			if m.CacheAtInput > 0 {
				ms.CostNote = "cache ao preço de entrada"
			}
		}
		var non200 int64
		status := map[string]int64{}
		for k, n := range m.Status {
			if k != "200" {
				status[k] = n
				non200 += n
			}
		}
		ms.Status = sortedCounts(status)
		if p := pct(non200, m.Calls); p != nil {
			ms.Non200Pct = *p
		}
		for b := range a.p95 {
			ms.P95Series = append(ms.P95Series, pctl(&a.p95[b], 0.95))
		}
		out = append(out, ms)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Calls != out[j].Calls {
			return out[i].Calls > out[j].Calls
		}
		return out[i].Model < out[j].Model
	})
	return out
}
