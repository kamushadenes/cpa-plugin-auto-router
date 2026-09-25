package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chloeassistant/cpa-plugin-auto-router/internal/decide"
	"github.com/chloeassistant/cpa-plugin-auto-router/internal/session"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestOverlappingRoutesKeepHigherDifficulty(t *testing.T) {
	routineResponse := calibratedJevResponse(0.9, 0.5)
	hardResponse := strings.Replace(routineResponse, `"0":0,"1":1,"2":0,"3":0,"4":0`, `"0":0,"1":0,"2":0,"3":1,"4":0`, 1)
	routineStarted := make(chan struct{})
	releaseRoutine := make(chan struct{})
	var startOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			State struct {
				Item string `json:"item"`
			} `json:"state"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if strings.Contains(request.State.Item, "routine") {
			startOnce.Do(func() { close(routineStarted) })
			<-releaseRoutine
			_, _ = w.Write([]byte(routineResponse))
			return
		}
		_, _ = w.Write([]byte(hardResponse))
	}))
	defer server.Close()
	configureJevTest(t, server.URL)
	store = session.New(time.Hour, 65536)
	fake := newFakeHostCalls()
	installFakeHost(t, fake)

	routine := pluginapi.ModelRouteRequest{
		RequestedModel: pluginIdentifier,
		SourceFormat:   "chat-completions",
		Headers:        http.Header{"X-Session-ID": []string{"overlap-session"}},
		Body:           []byte(`{"messages":[{"role":"user","content":"routine request"}]}`),
	}
	hard := routine
	hard.Body = []byte(`{"messages":[{"role":"user","content":"hard request"}]}`)

	routineDone := make(chan error, 1)
	go func() {
		_, err := routeModel(marshalRoute(t, routine))
		routineDone <- err
	}()
	select {
	case <-routineStarted:
	case <-time.After(time.Second):
		t.Fatal("routine classification did not start")
	}

	hardDone := make(chan error, 1)
	go func() {
		_, err := routeModel(marshalRoute(t, hard))
		hardDone <- err
	}()
	select {
	case err := <-hardDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("hard classification did not complete")
	}

	state, ok := store.Get("overlap-session")
	if !ok || state.Difficulty != decide.Hard || state.Tier != "top" || state.Thinking != "xhigh" {
		t.Fatalf("hard completion state = %#v, present=%v", state, ok)
	}

	close(releaseRoutine)
	select {
	case err := <-routineDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("routine classification did not complete")
	}
	state, ok = store.Get("overlap-session")
	if !ok || state.Difficulty != decide.Hard || state.Tier != "top" || state.Thinking != "xhigh" {
		t.Fatalf("stale routine completion downgraded state = %#v, present=%v", state, ok)
	}
}

func calibratedJevResponse(touchesCode, frontend float64) string {
	return `{"answers":{"touches_code":{"noul":` + formatFloat(touchesCode) + `},"frontend":{"noul":` + formatFloat(frontend) + `},"fix_existing":{"noul":0.1},"judges_existing":{"noul":0.1},"design_only":{"noul":0.1},"many_steps":{"noul":0.1},"transform_only":{"noul":0.1},"exact_answer":{"noul":0.1},"writes_tests":{"noul":0.1},"effort":{"probabilities":{"0":0,"1":1,"2":0,"3":0,"4":0}}}}`
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func lowConfidenceJevResponse(touchesCode, frontend float64) string {
	return `{"answers":{"touches_code":{"noul":` + formatFloat(touchesCode) + `},"frontend":{"noul":` + formatFloat(frontend) + `},"fix_existing":{"noul":0.1},"judges_existing":{"noul":0.1},"design_only":{"noul":0.1},"many_steps":{"noul":0.1},"transform_only":{"noul":0.1},"exact_answer":{"noul":0.1},"writes_tests":{"noul":0.1},"effort":{"probabilities":{"0":0.4,"1":0.3,"2":0.2,"3":0.05,"4":0.05}}}}`
}

func jevResponseWithEffort(effort string) string {
	return strings.Replace(
		calibratedJevResponse(0.9, 0.1),
		`{"0":0,"1":1,"2":0,"3":0,"4":0}`,
		effort,
		1,
	)
}

func TestDecideWithContextEscalatesJournalExtreme(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(jevResponseWithEffort(`{"0":0,"1":0,"2":0,"3":0.61,"4":0.39}`)))
	}))
	defer server.Close()
	configureJevTest(t, server.URL)
	request := pluginapi.ModelRouteRequest{RequestedModel: pluginIdentifier, SourceFormat: "chat-completions", Body: []byte(`{"messages":[{"role":"user","content":"design the RLS migration"}]}`)}
	previous := decide.State{Difficulty: decide.Trivial, Model: "gpt-5.6-luna", Tier: "flash", Thinking: "low"}
	decision, meta, _, err := decideForWithContext(request, previous, true)
	if err != nil {
		t.Fatal(err)
	}
	if meta.effortMean != 3.39 || meta.difficultyConfidence != 1 {
		t.Fatalf("journal metadata = %#v", meta)
	}
	if decision.State.Difficulty != decide.Extreme || decision.State.Tier != "top" || decision.State.Thinking != "max" || decision.Reason != "escalate-tier" {
		t.Fatalf("journal decision = %#v", decision)
	}
}

func TestDecideWithContextUsesHardFloorForLowConfidenceExtreme(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(jevResponseWithEffort(`{"0":0.45,"1":0,"2":0,"3":0.15,"4":0.40}`)))
	}))
	defer server.Close()
	configureJevTest(t, server.URL)
	request := pluginapi.ModelRouteRequest{RequestedModel: pluginIdentifier, SourceFormat: "chat-completions", Body: []byte(`{"messages":[{"role":"user","content":"investigate this failure"}]}`)}
	previous := decide.State{Difficulty: decide.Trivial, Model: "gpt-5.6-luna", Tier: "flash", Thinking: "low"}
	decision, meta, _, err := decideForWithContext(request, previous, true)
	if err != nil {
		t.Fatal(err)
	}
	if meta.effortMean != 2.05 || meta.difficultyConfidence != .55 {
		t.Fatalf("low-confidence extreme metadata = %#v", meta)
	}
	if decision.State.Difficulty != decide.Hard || decision.State.Tier != "top" || decision.State.Thinking != "xhigh" || decision.Reason != "escalate-tier" {
		t.Fatalf("low-confidence extreme decision = %#v", decision)
	}
	if decision.State.Difficulty == decide.Trivial {
		t.Fatal("low-confidence extreme must not retain trivial difficulty")
	}
}

func TestDecideWithContextFloorsLowConfidenceHardToRoutine(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(jevResponseWithEffort(`{"0":0.41,"1":0,"2":0,"3":0.29,"4":0.30}`)))
	}))
	defer server.Close()
	configureJevTest(t, server.URL)
	request := pluginapi.ModelRouteRequest{RequestedModel: pluginIdentifier, SourceFormat: "chat-completions", Body: []byte(`{"messages":[{"role":"user","content":"fix this issue"}]}`)}
	previous := decide.State{Difficulty: decide.Trivial, Model: "gpt-5.6-luna", Tier: "flash", Thinking: "low"}
	decision, meta, _, err := decideForWithContext(request, previous, true)
	if err != nil {
		t.Fatal(err)
	}
	if meta.effortMean != 2.07 || meta.difficultyConfidence != .29 {
		t.Fatalf("low-confidence hard metadata = %#v", meta)
	}
	if decision.State.Difficulty != decide.Routine || decision.State.Tier != "mid" || decision.State.Thinking != "high" || decision.Reason != "escalate-tier" {
		t.Fatalf("low-confidence hard decision = %#v", decision)
	}
}

func TestDecideWithContextPreservesHigherPreviousDifficulty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(jevResponseWithEffort(`{"0":0.45,"1":0.55,"2":0,"3":0,"4":0}`)))
	}))
	defer server.Close()
	configureJevTest(t, server.URL)
	request := pluginapi.ModelRouteRequest{RequestedModel: pluginIdentifier, SourceFormat: "chat-completions", Body: []byte(`{"messages":[{"role":"user","content":"fix this issue again"}]}`)}
	previous := decide.State{Difficulty: decide.Hard, Model: "blind", Tier: "top", Thinking: "xhigh"}
	decision, _, _, err := decideForWithContext(request, previous, true)
	if err != nil {
		t.Fatal(err)
	}
	if decision.State.Difficulty != decide.Hard || decision.State.Tier != "top" || decision.State.Thinking != "xhigh" {
		t.Fatalf("higher previous difficulty was downgraded = %#v", decision)
	}
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
	key := requestKey(request.SourceFormat, request.Headers, request.Body)
	if value, ok := pending.Load(key); !ok || value.(pendingRoute).decision.State.Model == "" {
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
	if decision.State.Difficulty != decide.Trivial {
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
	key := requestKey(request.SourceFormat, request.Headers, request.Body)
	value, ok := pending.Load(key)
	if !ok || value.(pendingRoute).decision.State.Difficulty != decide.Routine {
		t.Fatalf("default decision = %#v", value)
	}
}

func TestExistingExtremeToolOnlyStillVisionSwaps(t *testing.T) {
	configureTest(t)
	_, _, generation := store.Begin("vision-session")
	store.Put("vision-session", generation, decide.State{Difficulty: decide.Extreme, Model: "blind", Thinking: "max", Tier: "top"})
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
	if !response.Handled || response.Reason != "model-gone" {
		t.Fatalf("response = %+v", response)
	}
	key := requestKey(request.SourceFormat, request.Headers, request.Body)
	value, ok := pending.Load(key)
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

func TestPendingRoutesMatchBodyBeforeExecution(t *testing.T) {
	var mu sync.Mutex
	jevCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		jevCalls++
		mu.Unlock()
		_, _ = w.Write([]byte(calibratedJevResponse(0.9, 0.5)))
	}))
	defer server.Close()
	configureJevTest(t, server.URL)
	store = session.New(time.Hour, 65536)
	fake := newFakeHostCalls()
	installFakeHost(t, fake)

	const sessionID = "pending-body-session"
	imageBody := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"https://example.invalid/image.png"}}]}]}`)
	textBody := []byte(`{"messages":[{"role":"user","content":"describe"}]}`)
	imageRoute := pluginapi.ModelRouteRequest{RequestedModel: pluginIdentifier, SourceFormat: "chat-completions", Headers: http.Header{"X-Session-ID": []string{sessionID}}, Body: imageBody}
	textRoute := imageRoute
	textRoute.Body = textBody
	if _, err := routeModel(marshalRoute(t, imageRoute)); err != nil {
		t.Fatal(err)
	}
	if _, err := routeModel(marshalRoute(t, textRoute)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	gotCalls := jevCalls
	mu.Unlock()
	if gotCalls != 2 {
		t.Fatalf("Jev calls after routing = %d, want 2", gotCalls)
	}

	imageExec := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: pluginIdentifier, SourceFormat: "chat-completions", OriginalRequest: imageBody, Headers: http.Header{"X-Session-ID": []string{sessionID}}}}
	_, imageContext, _, err := decisionForExecutorWithContext(imageExec)
	if err != nil {
		t.Fatal(err)
	}
	if !imageContext.hasImage {
		t.Fatalf("image execution consumed non-image context: %+v", imageContext)
	}

	textExec := imageExec
	textExec.OriginalRequest = textBody
	_, textContext, _, err := decisionForExecutorWithContext(textExec)
	if err != nil {
		t.Fatal(err)
	}
	if textContext.hasImage {
		t.Fatalf("text execution consumed image context: %+v", textContext)
	}
	mu.Lock()
	gotCalls = jevCalls
	mu.Unlock()
	if gotCalls != 2 {
		t.Fatalf("Jev calls after execution = %d, want exactly 2", gotCalls)
	}
}

func TestPendingDuplicateRouteKeepsFirstDecision(t *testing.T) {
	routineResponse := calibratedJevResponse(0.9, 0.5)
	hardResponse := strings.Replace(routineResponse, `"0":0,"1":1,"2":0,"3":0,"4":0`, `"0":0,"1":0,"2":0,"3":1,"4":0`, 1)
	var mu sync.Mutex
	responses := []string{routineResponse, hardResponse}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		response := responses[0]
		responses = responses[1:]
		mu.Unlock()
		_, _ = w.Write([]byte(response))
	}))
	defer server.Close()
	configureJevTest(t, server.URL)
	store = session.New(time.Hour, 65536)
	fake := newFakeHostCalls()
	installFakeHost(t, fake)
	body := []byte(`{"messages":[{"role":"user","content":"same request"}]}`)
	request := pluginapi.ModelRouteRequest{RequestedModel: pluginIdentifier, SourceFormat: "chat-completions", Headers: http.Header{"X-Session-ID": []string{"duplicate-session"}}, Body: body}
	if _, err := routeModel(marshalRoute(t, request)); err != nil {
		t.Fatal(err)
	}
	if _, err := routeModel(marshalRoute(t, request)); err != nil {
		t.Fatal(err)
	}
	executor := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: pluginIdentifier, SourceFormat: "chat-completions", OriginalRequest: body, Headers: request.Headers}}
	decision, _, _, err := decisionForExecutorWithContext(executor)
	if err != nil {
		t.Fatal(err)
	}
	if decision.State.Difficulty != decide.Routine {
		t.Fatalf("duplicate consumed newer decision: %#v", decision.State)
	}
}

func TestPendingRoutesSeparateSourceFormats(t *testing.T) {
	var mu sync.Mutex
	jevCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		jevCalls++
		mu.Unlock()
		_, _ = w.Write([]byte(calibratedJevResponse(0.9, 0.5)))
	}))
	defer server.Close()
	configureJevTest(t, server.URL)
	store = session.New(time.Hour, 65536)
	fake := newFakeHostCalls()
	installFakeHost(t, fake)
	chatBody := []byte(`{"messages":[{"role":"user","content":"describe"}]}`)
	responseBody := []byte(`{"input":[{"role":"user","content":[{"type":"input_text","text":"describe"},{"type":"input_image","image_url":"https://example.invalid/image.png"}]}]}`)
	chat := pluginapi.ModelRouteRequest{RequestedModel: pluginIdentifier, SourceFormat: "chat-completions", Headers: http.Header{"X-Session-ID": []string{"format-session"}}, Body: chatBody}
	responses := pluginapi.ModelRouteRequest{RequestedModel: pluginIdentifier, SourceFormat: "responses", Headers: chat.Headers, Body: responseBody}
	if _, err := routeModel(marshalRoute(t, chat)); err != nil {
		t.Fatal(err)
	}
	if _, err := routeModel(marshalRoute(t, responses)); err != nil {
		t.Fatal(err)
	}
	chatExec := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: pluginIdentifier, SourceFormat: chat.SourceFormat, OriginalRequest: chatBody, Headers: chat.Headers}}
	_, chatContext, _, err := decisionForExecutorWithContext(chatExec)
	if err != nil {
		t.Fatal(err)
	}
	if chatContext.hasImage {
		t.Fatalf("chat execution consumed responses context: %+v", chatContext)
	}
	responseExec := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: pluginIdentifier, SourceFormat: responses.SourceFormat, OriginalRequest: responseBody, Headers: responses.Headers}}
	_, responseContext, _, err := decisionForExecutorWithContext(responseExec)
	if err != nil {
		t.Fatal(err)
	}
	if !responseContext.hasImage {
		t.Fatalf("responses execution lost image context: %+v", responseContext)
	}
	mu.Lock()
	gotCalls := jevCalls
	mu.Unlock()
	if gotCalls != 2 {
		t.Fatalf("Jev calls after format-separated execution = %d, want exactly 2", gotCalls)
	}
}

func TestMissingPendingRouteReclassifiesAndLogs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(calibratedJevResponse(0.9, 0.5)))
	}))
	defer server.Close()
	configureJevTest(t, server.URL)
	store = session.New(time.Hour, 65536)
	fake := newFakeHostCalls()
	installFakeHost(t, fake)
	req := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{
		Model:           pluginIdentifier,
		SourceFormat:    "chat-completions",
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"reclassify me"}]}`),
		Headers:         http.Header{"X-Session-ID": []string{"missing-pending-session"}},
	}}
	decision, _, _, err := decisionForExecutorWithContext(req)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Reason != "reclassified" {
		t.Fatalf("decision reason = %q, want reclassified", decision.Reason)
	}
	if len(fake.logs) != 1 {
		t.Fatalf("decision logs = %#v, want one reclassification log", fake.logs)
	}
	fields := decisionLogFields(t, fake.logs[0])
	if fields["reason"] != "reclassified" {
		t.Fatalf("reclassification log = %#v", fields)
	}
}

func TestPendingRouteDecisionAndContextAreConsumedTogether(t *testing.T) {
	configureTest(t)
	pending = sync.Map{}
	req := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{
		Model:           "auto-router",
		SourceFormat:    "chat-completions",
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
		Metadata:        map[string]any{"request_id": "pending-id"},
	}}
	key := requestKey(req.SourceFormat, req.Headers, req.OriginalRequest)
	pending.Store(key, pendingRoute{
		decision: decide.Decision{Choice: decide.Choice{Model: "gpt-5.6-luna", Tier: "mid", Thinking: "high"}},
		context:  routeContext{category: "backend", hasImage: true},
	})
	decision, routeCtx, _, err := decisionForExecutorWithContext(req)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Model != "gpt-5.6-luna" || routeCtx.category != "backend" || !routeCtx.hasImage {
		t.Fatalf("pending route = decision=%+v context=%+v", decision, routeCtx)
	}
	if _, ok := pending.Load(key); ok {
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
