package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chloeassistant/cpa-plugin-auto-router/internal/decide"
	"github.com/chloeassistant/cpa-plugin-auto-router/internal/session"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type fakeStreamRead struct {
	response pluginapi.HostModelStreamReadResponse
	err      error
}

type fakeHostCalls struct {
	mu sync.Mutex

	executeResponses map[string]pluginapi.HostModelExecutionResponse
	executeErrors    map[string]error
	streamResponses  map[string]pluginapi.HostModelStreamResponse
	streamReads      map[string][]fakeStreamRead

	executeModels []string
	streamModels  []string
	hostCloses    []string
	emits         []string
	pluginCloses  chan rpcStreamCloseRequest
	logs          []map[string]any
}

func newFakeHostCalls() *fakeHostCalls {
	return &fakeHostCalls{
		executeResponses: map[string]pluginapi.HostModelExecutionResponse{},
		executeErrors:    map[string]error{},
		streamResponses:  map[string]pluginapi.HostModelStreamResponse{},
		streamReads:      map[string][]fakeStreamRead{},
		pluginCloses:     make(chan rpcStreamCloseRequest, 8),
	}
}

func (f *fakeHostCalls) call(method string, payload any) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	switch method {
	case pluginabi.MethodHostModelExecute:
		req, ok := payload.(hostModelExecutionRequest)
		if !ok {
			return nil, fmt.Errorf("execute payload type %T", payload)
		}
		model := stripThinkingSuffix(req.Model)
		f.executeModels = append(f.executeModels, req.Model)
		if err := f.executeErrors[model]; err != nil {
			return nil, err
		}
		response, ok := f.executeResponses[model]
		if !ok {
			return nil, fmt.Errorf("no execute response for %s", model)
		}
		return marshalFakeHost(response)

	case pluginabi.MethodHostModelExecuteStream:
		req, ok := payload.(hostModelExecutionRequest)
		if !ok {
			return nil, fmt.Errorf("stream execute payload type %T", payload)
		}
		model := stripThinkingSuffix(req.Model)
		f.streamModels = append(f.streamModels, req.Model)
		response, ok := f.streamResponses[model]
		if !ok {
			return nil, fmt.Errorf("no stream response for %s", model)
		}
		return marshalFakeHost(response)

	case pluginabi.MethodHostModelStreamRead:
		req, ok := payload.(pluginapi.HostModelStreamReadRequest)
		if !ok {
			return nil, fmt.Errorf("stream read payload type %T", payload)
		}
		reads := f.streamReads[req.StreamID]
		if len(reads) == 0 {
			return nil, fmt.Errorf("no stream read response for %s", req.StreamID)
		}
		step := reads[0]
		f.streamReads[req.StreamID] = reads[1:]
		if step.err != nil {
			return nil, step.err
		}
		return marshalFakeHost(step.response)

	case pluginabi.MethodHostModelStreamClose:
		req, ok := payload.(pluginapi.HostModelStreamCloseRequest)
		if !ok {
			return nil, fmt.Errorf("model close payload type %T", payload)
		}
		f.hostCloses = append(f.hostCloses, req.StreamID)
		return json.RawMessage(`{}`), nil

	case pluginabi.MethodHostStreamEmit:
		req, ok := payload.(rpcStreamEmitRequest)
		if !ok {
			return nil, fmt.Errorf("emit payload type %T", payload)
		}
		f.emits = append(f.emits, string(req.Payload))
		return json.RawMessage(`{}`), nil

	case pluginabi.MethodHostStreamClose:
		req, ok := payload.(rpcStreamCloseRequest)
		if !ok {
			return nil, fmt.Errorf("plugin close payload type %T", payload)
		}
		f.pluginCloses <- req
		return json.RawMessage(`{}`), nil

	case pluginabi.MethodHostLog:
		fields, ok := payload.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("log payload type %T", payload)
		}
		f.logs = append(f.logs, fields)
		return json.RawMessage(`{}`), nil
	default:
		return nil, fmt.Errorf("unexpected host callback %s", method)
	}
}

func marshalFakeHost(value any) (json.RawMessage, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func installFakeHost(t *testing.T, fake *fakeHostCalls) {
	t.Helper()
	previous := hostCall
	hostCall = fake.call
	t.Cleanup(func() { hostCall = previous })
}

func configureFailoverHostTest(t *testing.T) {
	t.Helper()
	t.Setenv("TEST_JEV_KEY", "")
	path := t.TempDir() + "/models.yaml"
	raw := []byte("benchmarks:\n  arena-overall: {source: test, unit: elo}\nmodels:\n  first:\n    tier: mid\n    vision: true\n    cost: {input: 1, output: 1}\n    scores: {}\n  second:\n    tier: mid\n    vision: true\n    cost: {input: 2, output: 2}\n    scores: {}\n  third:\n    tier: mid\n    vision: true\n    cost: {input: 3, output: 3}\n    scores: {}\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(lifecycleRequest{ConfigYAML: []byte("enabled: true\njev_api_key_env: TEST_JEV_KEY\njev_base_url: http://127.0.0.1:1\njev_timeout_ms: 20\ntable_path: " + path + "\n")})
	if err != nil {
		t.Fatal(err)
	}
	if err := configure(config); err != nil {
		t.Fatal(err)
	}
	store = session.New(time.Hour, 65536)
	pending = sync.Map{}
}

func seedHostDecision(t *testing.T, sessionID, model string, streamID ...string) []byte {
	t.Helper()
	decision := decide.Decision{
		Choice: decide.Choice{Model: model, Tier: "mid", Thinking: "high"},
		Reason: "new",
		State:  decide.State{Difficulty: decide.Routine, Model: model, Thinking: "high", Tier: "mid"},
	}
	_, _, generation := store.Begin(sessionID)
	req := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{
		Model:           pluginIdentifier,
		SourceFormat:    "chat-completions",
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"retry me"}]}`),
		Headers:         http.Header{"X-Session-ID": []string{sessionID}},
	}}
	key := requestKey(req.SourceFormat, req.Headers, req.OriginalRequest)
	pending.Store(key, pendingRoute{decision: decision, context: routeContext{
		category:             "backend",
		factors:              decide.Factors{"touches_code": 1},
		effortP:              decide.EffortDistribution{"0": 0, "1": 1, "2": 0, "3": 0, "4": 0},
		effortMean:           1,
		categoryConfidence:   1,
		difficultyConfidence: 1,
		difficulty:           decision.State.Difficulty,
	}, generation: generation})
	store.Put(sessionID, generation, decision.State)
	if len(streamID) > 0 {
		req.StreamID = streamID[0]
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func decodeHostEnvelope(t *testing.T, raw []byte) (envelope, pluginapi.ExecutorResponse) {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if !env.OK {
		return env, pluginapi.ExecutorResponse{}
	}
	var response pluginapi.ExecutorResponse
	if err := json.Unmarshal(env.Result, &response); err != nil {
		t.Fatal(err)
	}
	return env, response
}

func assertFailoverHeader(t *testing.T, header, model string, failed []string) {
	t.Helper()
	prefix := model + "(high);failover;failed_from="
	if !strings.HasPrefix(header, prefix) {
		t.Fatalf("X-Auto-Router = %q, want prefix %q", header, prefix)
	}
	var got []string
	if err := json.Unmarshal([]byte(strings.TrimPrefix(header, prefix)), &got); err != nil {
		t.Fatalf("X-Auto-Router failed_from = %q: %v", header, err)
	}
	if len(got) != len(failed) {
		t.Fatalf("failed_from = %#v, want %#v", got, failed)
	}
	for i := range failed {
		if got[i] != failed[i] {
			t.Fatalf("failed_from = %#v, want %#v", got, failed)
		}
	}
}

func assertFailoverLog(t *testing.T, fake *fakeHostCalls, model string, failed []string) {
	t.Helper()
	for _, logEntry := range fake.logs {
		fields := decisionLogFields(t, logEntry)
		if fields["model"] != model || fields["reason"] != "failover" {
			continue
		}
		got, ok := fields["failed_from"].([]any)
		if !ok || len(got) != len(failed) {
			continue
		}
		matches := true
		for i, want := range failed {
			if got[i] != want {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		factors, factorsOK := fields["factors"].(map[string]any)
		effortP, effortOK := fields["effort_p"].(map[string]any)
		if !factorsOK || factors["touches_code"] != float64(1) || !effortOK || effortP["1"] != float64(1) || fields["effort_mean"] != float64(1) || fields["category_confidence"] != float64(1) || fields["difficulty_confidence"] != float64(1) {
			continue
		}
		if _, ok := fields["category_p"]; ok {
			t.Fatalf("obsolete category_p in failover log: %#v", fields)
		}
		if _, ok := fields["difficulty_p"]; ok {
			t.Fatalf("obsolete difficulty_p in failover log: %#v", fields)
		}
		return
	}
	t.Fatalf("failover log missing model=%q failed_from=%#v: %#v", model, failed, fake.logs)
}

func TestFailoverLogUsesComposedLabelsBelowConfidenceThreshold(t *testing.T) {
	fake := newFakeHostCalls()
	installFakeHost(t, fake)
	factors := decide.Factors{"touches_code": 0.9, "frontend": 0.1, "fix_existing": 0.1, "judges_existing": 0.1, "design_only": 0.1, "many_steps": 0.1, "transform_only": 0.1, "exact_answer": 0.1, "writes_tests": 0.1}
	effort := decide.EffortDistribution{"0": 0.30, "1": 0, "2": 0.10, "3": 0.35, "4": 0.25}
	decision := decide.Decision{Choice: decide.Choice{Model: "model", Tier: "mid", Thinking: "high"}, Reason: "failover", State: decide.State{Difficulty: decide.Routine}}
	ctx := routeContext{factors: factors, effortP: effort, effortMean: decide.EffortMean(effort), category: "", difficulty: decide.Routine, categoryConfidence: 0.5, difficultyConfidence: 0.45, confidence: 0.45}
	logFailover(rpcExecutorRequest{}, decision, ctx, []string{"first"})
	fields := decisionLogFields(t, fake.logs[0])
	if fields["category"] != "backend" || fields["difficulty"] != decide.Hard || fields["model"] != "model" || fields["reason"] != "failover" {
		t.Fatalf("logged failover = %#v", fields)
	}
}

func waitPluginClose(t *testing.T, fake *fakeHostCalls) rpcStreamCloseRequest {
	t.Helper()
	select {
	case closeRequest := <-fake.pluginCloses:
		return closeRequest
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for plugin stream close")
		return rpcStreamCloseRequest{}
	}
}

func TestStaleEffectiveSessionDoesNotOverwriteVisionSwap(t *testing.T) {
	configureTest(t)
	store = session.New(time.Hour, 65536)
	fake := newFakeHostCalls()
	installFakeHost(t, fake)
	_, _, generation := store.Begin("same-tier-session")
	store.Put("same-tier-session", generation, decide.State{Difficulty: decide.Hard, Model: "blind", Thinking: "xhigh", Tier: "top"})

	textRequest := pluginapi.ModelRouteRequest{
		RequestedModel: pluginIdentifier,
		SourceFormat:   "chat-completions",
		Headers:        http.Header{"X-Session-ID": []string{"same-tier-session"}},
		Body:           []byte(`{"messages":[{"role":"user","content":"keep the current model"}]}`),
	}
	if _, err := routeModel(marshalRoute(t, textRequest)); err != nil {
		t.Fatal(err)
	}
	key := requestKey(textRequest.SourceFormat, textRequest.Headers, textRequest.Body)
	pendingValue, ok := pending.Load(key)
	if !ok {
		t.Fatal("older route decision missing")
	}
	olderRoute := pendingValue.(pendingRoute)
	older := olderRoute.decision
	if older.Model != "blind" {
		t.Fatalf("older route model = %q, want blind", older.Model)
	}

	imageRequest := textRequest
	imageRequest.Body = []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"look at this"},{"type":"image_url","image_url":{"url":"https://example.invalid/image.png"}}]}]}`)
	if _, err := routeModel(marshalRoute(t, imageRequest)); err != nil {
		t.Fatal(err)
	}
	state, ok := store.Get("same-tier-session")
	if !ok || state.Model != "eyes" || state.Tier != "top" || state.Thinking != "xhigh" {
		t.Fatalf("vision swap state = %#v, present=%v", state, ok)
	}

	persistEffectiveSession(rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{
		SourceFormat:    textRequest.SourceFormat,
		OriginalRequest: textRequest.Body,
		Headers:         textRequest.Headers,
	}}, older, olderRoute.generation)
	state, ok = store.Get("same-tier-session")
	if !ok || state.Model != "eyes" || state.Tier != "top" || state.Thinking != "xhigh" {
		t.Fatalf("stale completion replaced newer model = %#v, present=%v", state, ok)
	}
}

func TestExecuteRetriesBuffered429WithDifferentModelAndPersistsSession(t *testing.T) {
	configureFailoverHostTest(t)
	fake := newFakeHostCalls()
	installFakeHost(t, fake)
	fake.executeResponses["first"] = pluginapi.HostModelExecutionResponse{StatusCode: http.StatusTooManyRequests, Body: []byte(`{"error":"rate limit"}`)}
	fake.executeResponses["second"] = pluginapi.HostModelExecutionResponse{StatusCode: http.StatusOK, Body: []byte(`{"ok":true}`)}

	raw, err := execute(seedHostDecision(t, "buffered-session", "first"))
	if err != nil {
		t.Fatal(err)
	}
	env, response := decodeHostEnvelope(t, raw)
	if !env.OK || string(response.Payload) != `{"ok":true}` {
		t.Fatalf("execute envelope = %#v, response = %#v", env, response)
	}
	assertFailoverHeader(t, response.Headers.Get("X-Auto-Router"), "second", []string{"first"})
	if len(fake.executeModels) != 2 {
		t.Fatalf("host execute models = %#v", fake.executeModels)
	}
	state, ok := store.Get("buffered-session")
	if !ok || state.Model != "second" {
		t.Fatalf("effective session state = %#v, present=%v", state, ok)
	}
	assertFailoverLog(t, fake, "second", []string{"first"})
}

func TestExecuteRetriesTransportMarkerError(t *testing.T) {
	configureFailoverHostTest(t)
	fake := newFakeHostCalls()
	installFakeHost(t, fake)
	fake.executeErrors["first"] = errors.New("AUTH_UNAVAILABLE")
	fake.executeResponses["second"] = pluginapi.HostModelExecutionResponse{StatusCode: http.StatusOK, Body: []byte(`{"ok":true}`)}

	raw, err := execute(seedHostDecision(t, "transport-session", "first"))
	if err != nil {
		t.Fatal(err)
	}
	env, response := decodeHostEnvelope(t, raw)
	if !env.OK || string(response.Payload) != `{"ok":true}` {
		t.Fatalf("execute response = %#v, envelope = %#v", response, env)
	}
	assertFailoverHeader(t, response.Headers.Get("X-Auto-Router"), "second", []string{"first"})
	if len(fake.executeModels) != 2 {
		t.Fatalf("host execute models = %#v", fake.executeModels)
	}
}

func TestExecuteStreamRetries429BeforeFirstChunk(t *testing.T) {
	configureFailoverHostTest(t)
	fake := newFakeHostCalls()
	installFakeHost(t, fake)
	fake.streamResponses["first"] = pluginapi.HostModelStreamResponse{StatusCode: http.StatusTooManyRequests, StreamID: "stream-first"}
	fake.streamResponses["second"] = pluginapi.HostModelStreamResponse{StatusCode: http.StatusOK, StreamID: "stream-second"}

	fake.streamReads["stream-second"] = []fakeStreamRead{{response: pluginapi.HostModelStreamReadResponse{Payload: []byte("data: ok\n\n"), Done: true}}}

	raw, err := executeStream(seedHostDecision(t, "stream-429-session", "first", "plugin-stream-429"))
	if err != nil {
		t.Fatal(err)
	}
	env, _ := decodeHostEnvelope(t, raw)
	if !env.OK {
		t.Fatalf("executeStream envelope = %#v", env)
	}
	var result struct {
		Headers http.Header `json:"headers"`
	}
	if err := json.Unmarshal(env.Result, &result); err != nil {
		t.Fatal(err)
	}
	assertFailoverHeader(t, result.Headers.Get("X-Auto-Router"), "second", []string{"first"})
	closeRequest := waitPluginClose(t, fake)
	if closeRequest.Error != "" {
		t.Fatalf("plugin stream close error = %q", closeRequest.Error)
	}
	if len(fake.emits) != 1 || fake.emits[0] != "data: ok\n\n" {
		t.Fatalf("emitted chunks = %#v", fake.emits)
	}
	if len(fake.streamModels) != 2 || fake.streamModels[0] != "first(high)" || fake.streamModels[1] != "second(high)" {
		t.Fatalf("host stream models = %#v", fake.streamModels)
	}
	if len(fake.hostCloses) != 2 {
		t.Fatalf("host stream closes = %#v", fake.hostCloses)
	}
}

func TestExecuteStreamRetriesReadErrorBeforeFirstChunk(t *testing.T) {
	configureFailoverHostTest(t)
	fake := newFakeHostCalls()
	installFakeHost(t, fake)
	fake.streamResponses["first"] = pluginapi.HostModelStreamResponse{StatusCode: http.StatusOK, StreamID: "stream-first"}
	fake.streamResponses["second"] = pluginapi.HostModelStreamResponse{StatusCode: http.StatusOK, StreamID: "stream-second"}
	fake.streamReads["stream-first"] = []fakeStreamRead{{response: pluginapi.HostModelStreamReadResponse{Error: "rate_limit"}}}
	fake.streamReads["stream-second"] = []fakeStreamRead{{response: pluginapi.HostModelStreamReadResponse{Payload: []byte("data: recovered\n\n"), Done: true}}}

	raw, err := executeStream(seedHostDecision(t, "stream-read-session", "first", "plugin-stream-read"))
	if err != nil {
		t.Fatal(err)
	}
	env, _ := decodeHostEnvelope(t, raw)
	if !env.OK {
		t.Fatalf("executeStream envelope = %#v", env)
	}
	var result struct {
		Headers http.Header `json:"headers"`
	}
	if err := json.Unmarshal(env.Result, &result); err != nil {
		t.Fatal(err)
	}
	assertFailoverHeader(t, result.Headers.Get("X-Auto-Router"), "second", []string{"first"})
	closeRequest := waitPluginClose(t, fake)
	if closeRequest.Error != "" {
		t.Fatalf("plugin stream close error = %q", closeRequest.Error)
	}
	if len(fake.emits) != 1 || fake.emits[0] != "data: recovered\n\n" {
		t.Fatalf("emitted chunks = %#v", fake.emits)
	}
	if len(fake.streamModels) != 2 {
		t.Fatalf("host stream models = %#v", fake.streamModels)
	}
}

func TestExecuteStreamStopsAfterThreeRetryableFailures(t *testing.T) {
	configureFailoverHostTest(t)
	fake := newFakeHostCalls()
	installFakeHost(t, fake)
	for _, model := range []string{"first", "second", "third"} {
		fake.streamResponses[model] = pluginapi.HostModelStreamResponse{StatusCode: http.StatusTooManyRequests, StreamID: "stream-" + model}
	}

	raw, err := executeStream(seedHostDecision(t, "stream-cap-session", "first", "plugin-stream-cap"))
	if err != nil {
		t.Fatal(err)
	}
	env, _ := decodeHostEnvelope(t, raw)
	if env.OK {
		t.Fatalf("executeStream unexpectedly succeeded: %#v", env)
	}
	if len(fake.streamModels) != 3 {
		t.Fatalf("host stream calls = %d (%#v), want exactly 3", len(fake.streamModels), fake.streamModels)
	}
}

func TestExecuteStreamDoesNotRetryAfterFirstChunk(t *testing.T) {
	configureFailoverHostTest(t)
	fake := newFakeHostCalls()
	installFakeHost(t, fake)
	fake.streamResponses["first"] = pluginapi.HostModelStreamResponse{StatusCode: http.StatusOK, StreamID: "stream-first"}
	fake.streamReads["stream-first"] = []fakeStreamRead{
		{response: pluginapi.HostModelStreamReadResponse{Payload: []byte("data: partial\n\n")}},
		{response: pluginapi.HostModelStreamReadResponse{Error: "rate_limit"}},
	}

	raw, err := executeStream(seedHostDecision(t, "stream-postchunk-session", "first", "plugin-stream-postchunk"))
	if err != nil {
		t.Fatal(err)
	}
	env, _ := decodeHostEnvelope(t, raw)
	if !env.OK {
		t.Fatalf("executeStream envelope = %#v", env)
	}
	closeRequest := waitPluginClose(t, fake)
	if !strings.Contains(strings.ToLower(closeRequest.Error), "rate_limit") {
		t.Fatalf("plugin stream close error = %q", closeRequest.Error)
	}
	if len(fake.streamModels) != 1 || fake.streamModels[0] != "first(high)" {
		t.Fatalf("host stream models = %#v", fake.streamModels)
	}
	if len(fake.emits) != 1 || fake.emits[0] != "data: partial\n\n" {
		t.Fatalf("emitted chunks = %#v", fake.emits)
	}
}

func TestExecuteStopsAfterThreeRetryableFailures(t *testing.T) {
	configureFailoverHostTest(t)
	fake := newFakeHostCalls()
	installFakeHost(t, fake)
	for _, model := range []string{"first", "second", "third"} {
		fake.executeResponses[model] = pluginapi.HostModelExecutionResponse{StatusCode: http.StatusTooManyRequests, Body: []byte(`{"error":"rate limit"}`)}
	}

	raw, err := execute(seedHostDecision(t, "three-failures-session", "first"))
	if err != nil {
		t.Fatal(err)
	}
	env, _ := decodeHostEnvelope(t, raw)
	if env.OK {
		t.Fatalf("execute unexpectedly succeeded: %#v", env)
	}
	if len(fake.executeModels) != 3 {
		t.Fatalf("host execute calls = %d (%#v), want exactly 3", len(fake.executeModels), fake.executeModels)
	}
	for i, want := range []string{"first(high)", "second(high)", "third(high)"} {
		if fake.executeModels[i] != want {
			t.Fatalf("host execute model[%d] = %q, want %q", i, fake.executeModels[i], want)
		}
	}
}

func TestExecuteDoesNotRetryExplicitNonretryableStatuses(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound, http.StatusRequestEntityTooLarge} {
		t.Run(fmt.Sprintf("status-%d", status), func(t *testing.T) {
			configureFailoverHostTest(t)
			fake := newFakeHostCalls()
			installFakeHost(t, fake)
			fake.executeResponses["first"] = pluginapi.HostModelExecutionResponse{StatusCode: status, Body: []byte(`{"error":"RATE_LIMIT"}`)}

			raw, err := execute(seedHostDecision(t, fmt.Sprintf("status-session-%d", status), "first"))
			if err != nil {
				t.Fatal(err)
			}
			env, response := decodeHostEnvelope(t, raw)
			if env.OK && strings.Contains(response.Headers.Get("X-Auto-Router"), ";failover") {
				t.Fatalf("status %d unexpectedly failed over: %#v", status, response.Headers)
			}
			if len(fake.executeModels) != 1 {
				t.Fatalf("status %d host execute calls = %#v", status, fake.executeModels)
			}
		})
	}
}

func TestExecuteSuccessfulResponseKeepsSessionModel(t *testing.T) {
	configureFailoverHostTest(t)
	fake := newFakeHostCalls()
	installFakeHost(t, fake)
	fake.executeResponses["first"] = pluginapi.HostModelExecutionResponse{StatusCode: http.StatusOK, Body: []byte(`{"ok":true}`)}

	raw, err := execute(seedHostDecision(t, "successful-session", "first"))
	if err != nil {
		t.Fatal(err)
	}
	env, response := decodeHostEnvelope(t, raw)
	if !env.OK || string(response.Payload) != `{"ok":true}` {
		t.Fatalf("execute response = %#v, envelope = %#v", response, env)
	}
	if len(fake.executeModels) != 1 || fake.executeModels[0] != "first(high)" {
		t.Fatalf("host execute models = %#v", fake.executeModels)
	}
	state, ok := store.Get("successful-session")
	if !ok || state.Model != "first" {
		t.Fatalf("session state = %#v, present=%v", state, ok)
	}
}

func TestRetryableHostFailureAllowlist(t *testing.T) {
	tests := []struct {
		name   string
		status int
		err    string
		want   bool
	}{
		{name: "rate limit", err: `{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your account's rate limit. Please try again later."}}`, want: true},
		{name: "overloaded", err: "upstream overloaded", want: true},
		{name: "cooling down", err: "provider cooling down", want: true},
		{name: "auth unavailable", err: "AUTH_UNAVAILABLE", want: true},
		{name: "api error", err: `{"type":"error","error":{"type":"api_error","message":"temporary upstream failure"}}`, want: true},
		{name: "spaced api error", err: `{"error":{"type": "api_error", "message":"temporary failure"}}`, want: true},
		{name: "stream closed before first event", err: "upstream stream closed before first event", want: true},
		{name: "text status 503", err: "host_call_failed: host model status 503", want: true},
		{name: "status 429", status: http.StatusTooManyRequests, want: true},
		{name: "status 502", status: http.StatusBadGateway, want: true},
		{name: "status 503", status: http.StatusServiceUnavailable, want: true},
		{name: "status 529", status: 529, want: true},
		{name: "stream closed before done", err: "upstream stream closed before [DONE]", want: true},
		{name: "authentication error", err: `{"type":"error","error":{"type":"authentication_error","message":"OAuth access token has been revoked."}}`, want: false},
		{name: "invalid request error", err: `{"type":"error","error":{"type":"invalid_request_error","message":"bad input"}}`, want: false},
		{name: "unknown error", err: "upstream disconnected", want: false},
		{name: "status 400 rate limit body", status: http.StatusBadRequest, err: `host model status 400: {"error":"RATE_LIMIT"}`, want: false},
		{name: "status 401 rate limit body", status: http.StatusUnauthorized, err: `host model status 401: {"error":"RATE_LIMIT"}`, want: false},
		{name: "status 404 rate limit body", status: http.StatusNotFound, err: `host model status 404: {"error":"RATE_LIMIT"}`, want: false},
		{name: "status 413 rate limit body", status: http.StatusRequestEntityTooLarge, err: `host model status 413: {"error":"RATE_LIMIT"}`, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			if tt.err != "" {
				err = errors.New(tt.err)
			}
			if got := retryableHostFailure(tt.status, err); got != tt.want {
				t.Fatalf("retryableHostFailure(%d, %v) = %v, want %v", tt.status, err, got, tt.want)
			}
		})
	}
}
