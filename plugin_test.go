package main

import (
	"encoding/json"
	"os"
	"sync"
	"testing"

	"github.com/chloeassistant/cpa-plugin-auto-router/internal/decide"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

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
	if _, ok := pending.Load("route-session"); !ok {
		t.Fatal("pending decision missing")
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
	if !ok || value.(decide.Decision).State.Difficulty != decide.Routine {
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
	if !ok || value.(decide.Decision).Model != "eyes" {
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
func TestPendingDecisionIsConsumed(t *testing.T) {
	configureTest(t)
	pending = sync.Map{}
	pending.Store("pending-id", decide.Decision{Choice: decide.Choice{Model: "gpt-5.6-luna", Tier: "mid", Thinking: "high"}})
	req := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{
		Model:           "auto-router",
		SourceFormat:    "chat-completions",
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
		Metadata:        map[string]any{"request_id": "pending-id"},
	}}
	if _, err := decisionForExecutor(req); err != nil {
		t.Fatal(err)
	}
	if _, ok := pending.Load("pending-id"); ok {
		t.Fatal("pending decision was not consumed")
	}
}

func TestSessionLogUsesShortHash(t *testing.T) {
	const raw = "session-secret-value"
	got := hashSession(raw)
	if got == raw || len(got) != len("h:00000000") || got[:2] != "h:" {
		t.Fatalf("hashed session = %q", got)
	}
}
