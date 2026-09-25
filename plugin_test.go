package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"testing"

	"github.com/chloeassistant/cpa-plugin-auto-router/internal/decide"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func calibratedJevResponse(touchesCode, frontend float64) string {
	return `{"answers":{"touches_code":{"noul":` + formatFloat(touchesCode) + `},"frontend":{"noul":` + formatFloat(frontend) + `},"fix_existing":{"noul":0.1},"judges_existing":{"noul":0.1},"design_only":{"noul":0.1},"many_steps":{"noul":0.1},"transform_only":{"noul":0.1},"exact_answer":{"noul":0.1},"writes_tests":{"noul":0.1},"effort":{"probabilities":{"0":0,"1":1,"2":0,"3":0,"4":0}}}}`
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func lowConfidenceJevResponse(touchesCode, frontend float64) string {
	return `{"answers":{"touches_code":{"noul":` + formatFloat(touchesCode) + `},"frontend":{"noul":` + formatFloat(frontend) + `},"fix_existing":{"noul":0.1},"judges_existing":{"noul":0.1},"design_only":{"noul":0.1},"many_steps":{"noul":0.1},"transform_only":{"noul":0.1},"exact_answer":{"noul":0.1},"writes_tests":{"noul":0.1},"effort":{"probabilities":{"0":0.4,"1":0.3,"2":0.2,"3":0.05,"4":0.05}}}}`
}

func TestRouteIgnoresOtherModels(t *testing.T) {
	raw, err := json.Marshal(rpcModelRouteRequest{ModelRouteRequest: pluginapi.ModelRouteRequest{RequestedModel: "gpt-6-astra"}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := routeModel(raw)
	if err != nil {
		t.Fatal(err)
	}
	var envelope envelope
	if err := json.Unmarshal(result, &envelope); err != nil {
		t.Fatal(err)
	}
	var response pluginapi.ModelRouteResponse
	if err := json.Unmarshal(envelope.Result, &response); err != nil {
		t.Fatal(err)
	}
	if response.Handled {
		t.Fatalf("response = %+v", response)
	}
}

func TestRouteDecidesWithoutJev(t *testing.T) {
	configureTest(t)
	request := pluginapi.ModelRouteRequest{
		RequestedModel: "auto-router",
		SourceFormat:   "chat-completions",
		Headers:        map[string][]string{"X-Session-ID": {"route-session"}},
		Body:           []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
	}
	raw, err := json.Marshal(rpcModelRouteRequest{ModelRouteRequest: request})
	if err != nil {
		t.Fatal(err)
	}
	result, err := routeModel(raw)
	if err != nil {
		t.Fatal(err)
	}
	response := decodeRouteResponse(t, result)
	if !response.Handled || response.TargetKind != pluginapi.ModelRouteTargetSelf || response.Reason != "jev-unavailable" {
		t.Fatalf("response = %+v", response)
	}
	if value, ok := pending.Load("route-session"); !ok || value.(pendingRoute).decision.State.Model == "" {
		t.Fatal("pending decision missing")
	}
}

func TestRouteComposesCalibratedFactorsAndEffort(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(calibratedJevResponse(0.9, 0.5)))
	}))
	defer server.Close()
	configureJevTest(t, server.URL)
	request := pluginapi.ModelRouteRequest{RequestedModel: pluginIdentifier, SourceFormat: "chat-completions", Body: []byte(`{"messages":[{"role":"user","content":"fix the API"}]}`)}
	decision, meta, routeCtx, err := decideForWithContext(request, decide.State{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if meta.category != "backend" || meta.difficulty != decide.Routine || meta.factors["touches_code"] != 0.9 || meta.effortP["1"] != 1 || meta.effortMean != 1 {
		t.Fatalf("meta = %#v", meta)
	}
	if meta.categoryConfidence != 0.9 || meta.difficultyConfidence != 1 {
		t.Fatalf("confidence = category=%v difficulty=%v", meta.categoryConfidence, meta.difficultyConfidence)
	}
	if routeCtx.category != meta.category || routeCtx.factors["frontend"] != 0.5 || routeCtx.effortP["1"] != 1 {
		t.Fatalf("route context = %#v", routeCtx)
	}
	if decision.State.Difficulty != decide.Routine {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestRouteLogsCalibratedMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(calibratedJevResponse(0.9, 0.5)))
	}))
	defer server.Close()
	configureJevTest(t, server.URL)
	fake := newFakeHostCalls()
	installFakeHost(t, fake)
	request := rpcModelRouteRequest{ModelRouteRequest: pluginapi.ModelRouteRequest{RequestedModel: pluginIdentifier, SourceFormat: "chat-completions", Headers: http.Header{"X-Session-ID": []string{"metadata-session"}}, Body: []byte(`{"messages":[{"role":"user","content":"fix the API"}]}`)}}
	if _, err := routeModel(marshalRoute(t, request.ModelRouteRequest)); err != nil {
		t.Fatal(err)
	}
	if len(fake.logs) != 1 {
		t.Fatalf("logs = %#v", fake.logs)
	}
	fields := decisionLogFields(t, fake.logs[0])
	if fields["model"] == "" || fields["reason"] != "new" {
		t.Fatalf("decision identity = model=%#v reason=%#v", fields["model"], fields["reason"])
	}
	for _, key := range []string{"factors", "effort_p", "effort_mean", "category_confidence", "difficulty_confidence"} {
		if _, ok := fields[key]; !ok {
			t.Errorf("missing log field %q: %#v", key, fields)
		}
	}
	for _, key := range []string{"category_p", "difficulty_p"} {
		if _, ok := fields[key]; ok {
			t.Errorf("obsolete log field %q = %#v", key, fields[key])
		}
	}
}

func TestRouteLogsComposedLabelsBelowConfidenceThreshold(t *testing.T) {
	fake := newFakeHostCalls()
	installFakeHost(t, fake)
	factors := decide.Factors{"touches_code": 0.9, "frontend": 0.1, "fix_existing": 0.1, "judges_existing": 0.1, "design_only": 0.1, "many_steps": 0.1, "transform_only": 0.1, "exact_answer": 0.1, "writes_tests": 0.1}
	effort := decide.EffortDistribution{"0": 0.30, "1": 0, "2": 0.10, "3": 0.35, "4": 0.25}
	decision := decide.Decision{Choice: decide.Choice{Model: "model", Tier: "mid", Thinking: "high"}, Reason: "new", State: decide.State{Difficulty: decide.Routine}}
	meta := routeMeta{factors: factors, effortP: effort, effortMean: decide.EffortMean(effort), category: "", difficulty: decide.Routine, categoryConfidence: 0.5, difficultyConfidence: 0.45, confidence: 0.45}
	if _, err := routeResponse("", "", decision, meta); err != nil {
		t.Fatal(err)
	}
	fields := decisionLogFields(t, fake.logs[0])
	if fields["category"] != "backend" || fields["difficulty"] != decide.Hard {
		t.Fatalf("logged labels = category=%#v difficulty=%#v", fields["category"], fields["difficulty"])
	}
	logFailover(rpcExecutorRequest{}, decision, routeContext{factors: factors, effortP: effort, category: "", difficulty: decide.Routine}, []string{"failed-model"})
	fields = decisionLogFields(t, fake.logs[1])
	if fields["category"] != "backend" || fields["difficulty"] != decide.Hard || fields["model"] != "model" || fields["reason"] != "failover" {
		t.Fatalf("failover log = %#v", fields)
	}
	failedFrom, ok := fields["failed_from"].([]any)
	if !ok || len(failedFrom) != 1 || failedFrom[0] != "failed-model" {
		t.Fatalf("failed_from = %#v", fields["failed_from"])
	}
}

func decisionLogFields(t *testing.T, entry map[string]any) map[string]any {
	t.Helper()
	message, ok := entry["message"].(string)
	if !ok {
		t.Fatalf("log message = %#v", entry["message"])
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(message), &fields); err != nil {
		t.Fatalf("decision log message = %q: %v", message, err)
	}
	if structured, ok := entry["fields"]; ok {
		fields, mapOK := structured.(map[string]any)
		if !mapOK || len(fields) > 0 {
			t.Fatalf("decision log has structured fields that append a host suffix: %#v", structured)
		}
	}
	return fields
}

func TestRouteKeepsConfidenceFallbacksForNewAndExistingSessions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(lowConfidenceJevResponse(0.5, 0.5)))
	}))
	defer server.Close()
	configureJevTest(t, server.URL)
	request := pluginapi.ModelRouteRequest{RequestedModel: pluginIdentifier, SourceFormat: "chat-completions", Body: []byte(`{"messages":[{"role":"user","content":"fix the API"}]}`)}
	decision, meta, _, err := decideForWithContext(request, decide.State{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if meta.category != "" || meta.categoryConfidence != 0.5 {
		t.Fatalf("new low-confidence category = %#v", meta)
	}
	if decision.State.Difficulty != decide.Routine {
		t.Fatalf("new low-confidence difficulty = %#v", decision.State)
	}
	prev := decide.State{Difficulty: decide.Hard, Model: "prior", Tier: "top", Thinking: "xhigh"}
	_, meta, _, err = decideForWithContext(request, prev, true)
	if err != nil {
		t.Fatal(err)
	}
	if meta.category != "" || meta.difficulty != decide.Hard {
		t.Fatalf("existing low-confidence fallback = %#v", meta)
	}
}

func TestNewToolOnlyRequestUsesDefaultDecision(t *testing.T) {
	configureTest(t)
	request := pluginapi.ModelRouteRequest{
		RequestedModel: "auto-router",
		SourceFormat:   "chat-completions",
		Headers:        map[string][]string{"X-Session-ID": {"tool-session"}},
		Body:           []byte(`{"messages":[{"role":"tool","content":"tool result"}]}`),
	}
	result, err := routeModel(marshalRoute(t, request))
	if err != nil {
		t.Fatal(err)
	}
	response := decodeRouteResponse(t, result)
	if !response.Handled || response.Reason != "jev-unavailable" {
		t.Fatalf("response = %+v", response)
	}
	value, ok := pending.Load("tool-session")
	if !ok || value.(pendingRoute).decision.State.Difficulty != decide.Routine {
		t.Fatalf("default decision = %#v", value)
	}
}

func TestExistingExtremeToolOnlyStillVisionSwaps(t *testing.T) {
	configureTest(t)
	store.Put("vision-session", decide.State{Difficulty: decide.Extreme, Model: "blind", Thinking: "max", Tier: "top"})
	request := pluginapi.ModelRouteRequest{
		RequestedModel: "auto-router",
		SourceFormat:   "chat-completions",
		Headers:        map[string][]string{"X-Session-ID": {"vision-session"}},
		Body:           []byte(`{"messages":[{"role":"tool","content":[{"type":"image","url":"x"}]}]}`),
	}
	result, err := routeModel(marshalRoute(t, request))
	if err != nil {
		t.Fatal(err)
	}
	response := decodeRouteResponse(t, result)
	if !response.Handled || response.Reason != "vision-swap" {
		t.Fatalf("response = %+v", response)
	}
	value, ok := pending.Load("vision-session")
	if !ok || value.(pendingRoute).decision.Model != "eyes" {
		t.Fatalf("vision decision = %#v", value)
	}
}

func configureTest(t *testing.T) {
	t.Helper()
	t.Setenv("TEST_JEV_KEY", "test-key")
	pending = sync.Map{}
	config, err := json.Marshal(lifecycleRequest{ConfigYAML: []byte("enabled: true\njev_api_key_env: TEST_JEV_KEY\njev_base_url: http://127.0.0.1:1\njev_timeout_ms: 20\ntable_path: testdata/models.yaml\n")})
	if err != nil {
		t.Fatal(err)
	}
	if err := configure(config); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("testdata/models.yaml"); err != nil {
		t.Fatal(err)
	}
}

func configureJevTest(t *testing.T, baseURL string) {
	t.Helper()
	t.Setenv("TEST_JEV_KEY", "test-key")
	pending = sync.Map{}
	config, err := json.Marshal(lifecycleRequest{ConfigYAML: []byte("enabled: true\njev_api_key_env: TEST_JEV_KEY\njev_base_url: " + baseURL + "\njev_timeout_ms: 1000\nconfidence_threshold: 0.6\ntable_path: testdata/models.yaml\n")})
	if err != nil {
		t.Fatal(err)
	}
	if err := configure(config); err != nil {
		t.Fatal(err)
	}
}

func marshalRoute(t *testing.T, request pluginapi.ModelRouteRequest) []byte {
	t.Helper()
	raw, err := json.Marshal(rpcModelRouteRequest{ModelRouteRequest: request})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func decodeRouteResponse(t *testing.T, raw []byte) pluginapi.ModelRouteResponse {
	t.Helper()
	var response pluginapi.ModelRouteResponse
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(env.Result, &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestPendingRouteDecisionAndContextAreConsumedTogether(t *testing.T) {
	configureTest(t)
	pending = sync.Map{}
	pending.Store("pending-id", pendingRoute{
		decision: decide.Decision{Choice: decide.Choice{Model: "gpt-5.6-luna", Tier: "mid", Thinking: "high"}},
		context:  routeContext{category: "backend", hasImage: true},
	})
	req := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{
		Model:           "auto-router",
		SourceFormat:    "chat-completions",
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
		Metadata:        map[string]any{"request_id": "pending-id"},
	}}
	decision, routeCtx, err := decisionForExecutorWithContext(req)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Model != "gpt-5.6-luna" || routeCtx.category != "backend" || !routeCtx.hasImage {
		t.Fatalf("pending route = decision=%+v context=%+v", decision, routeCtx)
	}
	if _, ok := pending.Load("pending-id"); ok {
		t.Fatal("pending route was not consumed")
	}
}

func TestSessionLogUsesShortHash(t *testing.T) {
	const raw = "session-secret-value"
	got := hashSession(raw)
	if got == raw || len(got) != len("h:00000000") || got[:2] != "h:" {
		t.Fatalf("hashed session = %q", got)
	}
}
