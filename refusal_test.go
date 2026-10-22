package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	testAlias    = "claude-opus-5-5-guarded"
	testPrimary  = "claude-opus-5-5"
	testFallback = "abliterated-model-large-v2"
)

func configureRefusalTest(t *testing.T) {
	t.Helper()
	t.Setenv("TEST_JEV_KEY", "test-key")
	pending = sync.Map{}
	configYAML := "enabled: true\njev_api_key_env: TEST_JEV_KEY\njev_base_url: http://127.0.0.1:1\njev_timeout_ms: 20\ntable_path: testdata/models.yaml\n" +
		"refusal_fallback:\n  - alias: " + testAlias + "\n    primary: " + testPrimary + "\n    fallback: " + testFallback + "\n"
	config, err := json.Marshal(lifecycleRequest{ConfigYAML: []byte(configYAML)})
	if err != nil {
		t.Fatal(err)
	}
	if err := configure(config); err != nil {
		t.Fatal(err)
	}
}

func refusalExecutorRequest(t *testing.T, model, streamID string) []byte {
	t.Helper()
	req := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{
		Model:           model,
		SourceFormat:    formatChatCompletions,
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"curate the learnings"}]}`),
		Headers:         http.Header{"X-Session-ID": []string{"refusal-session"}},
	}}
	req.StreamID = streamID
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func refusalLogFields(t *testing.T, fake *fakeHostCalls, reason string) map[string]any {
	t.Helper()
	for _, entry := range fake.logs {
		fields := decisionLogFields(t, entry)
		if fields["reason"] == reason {
			return fields
		}
	}
	t.Fatalf("no log with reason %q: %#v", reason, fake.logs)
	return nil
}

func TestRefusedResponseMatchesTranslatorShapes(t *testing.T) {
	tests := []struct {
		name   string
		format string
		body   string
		want   bool
	}{
		{"chat content filter", formatChatCompletions, `{"choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"content_filter"}]}`, true},
		{"chat second choice filtered", formatChatCompletions, `{"choices":[{"index":0,"finish_reason":"stop"},{"index":1,"finish_reason":"content_filter"}]}`, true},
		{"chat normal stop", formatChatCompletions, `{"choices":[{"index":0,"message":{"content":"hi"},"finish_reason":"stop"}]}`, false},
		{"chat length stop", formatChatCompletions, `{"choices":[{"index":0,"finish_reason":"length"}]}`, false},
		{"responses content filter", formatResponses, `{"status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[]}`, true},
		{"responses max tokens", formatResponses, `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}`, false},
		{"responses completed", formatResponses, `{"status":"completed","incomplete_details":null,"output":[{"type":"message"}]}`, false},
		{"responses shape under chat format", formatChatCompletions, `{"status":"incomplete","incomplete_details":{"reason":"content_filter"}}`, false},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := refusedResponse(testCase.format, []byte(testCase.body)); got != testCase.want {
				t.Fatalf("refusedResponse = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestStreamChunkDetectorsSeparateVisibleOutputFromReasoningAndRefusal(t *testing.T) {
	tests := []struct {
		name        string
		format      string
		chunk       string
		wantVisible bool
		wantRefusal bool
	}{
		{"chat role only", formatChatCompletions, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"},\"finish_reason\":null}]}\n\n", false, false},
		{"chat text delta", formatChatCompletions, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":null}]}\n\n", true, false},
		{"chat reasoning delta is not visible output", formatChatCompletions, "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"thinking\"},\"finish_reason\":null}]}\n\n", false, false},
		{"chat reasoning field is not visible output", formatChatCompletions, "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning\":\"thinking\"},\"finish_reason\":null}]}\n\n", false, false},
		{"chat tool call delta", formatChatCompletions, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\"}]},\"finish_reason\":null}]}\n\n", true, false},
		{"chat refusal terminal", formatChatCompletions, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"content_filter\"}]}\n\n", false, true},
		{"chat done sentinel", formatChatCompletions, "data: [DONE]\n\n", false, false},
		{"chat double framed text delta", formatChatCompletions, "data: data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":null}]}\n\n", true, false},
		{"chat double framed refusal", formatChatCompletions, "data: data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"content_filter\"}]}\n\n", false, true},
		{"responses text delta", formatResponses, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello\"}\n\n", true, false},
		{"responses function call arguments delta", formatResponses, "event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"{\\\"path\\\"\"}\n\n", true, false},
		{"responses empty delta", formatResponses, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"\"}\n\n", false, false},
		{"responses reasoning delta is not visible output", formatResponses, "event: response.reasoning_summary_text.delta\ndata: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"thinking\"}\n\n", false, false},
		{"responses refusal delta is not visible output", formatResponses, "event: response.refusal.delta\ndata: {\"type\":\"response.refusal.delta\",\"delta\":\"I cannot\"}\n\n", false, false},
		{"responses incomplete content filter", formatResponses, "event: response.incomplete\ndata: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"content_filter\"}}}\n\n", false, true},
		{"responses incomplete max tokens", formatResponses, "event: response.incomplete\ndata: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n", false, false},
		{"responses created", formatResponses, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"status\":\"in_progress\"}}\n\n", false, false},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := streamChunkHasVisibleOutput(testCase.format, []byte(testCase.chunk)); got != testCase.wantVisible {
				t.Fatalf("streamChunkHasVisibleOutput = %v, want %v", got, testCase.wantVisible)
			}
			if got := streamChunkRefused(testCase.format, []byte(testCase.chunk)); got != testCase.wantRefusal {
				t.Fatalf("streamChunkRefused = %v, want %v", got, testCase.wantRefusal)
			}
		})
	}
}

func TestRefusalFallbackNonStreamSwitchesOnRefusal(t *testing.T) {
	configureRefusalTest(t)
	fake := newFakeHostCalls()
	installFakeHost(t, fake)
	fake.executeResponses[testPrimary] = pluginapi.HostModelExecutionResponse{
		StatusCode: http.StatusOK,
		Body:       []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"content_filter"}]}`),
	}
	fake.executeResponses[testFallback] = pluginapi.HostModelExecutionResponse{
		StatusCode: http.StatusOK,
		Body:       []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"here are the learnings"},"finish_reason":"stop"}]}`),
	}

	raw, err := execute(refusalExecutorRequest(t, testAlias, ""))
	if err != nil {
		t.Fatal(err)
	}
	env, response := decodeHostEnvelope(t, raw)
	if !env.OK {
		t.Fatalf("envelope = %#v", env.Error)
	}
	if got := response.Headers.Get("X-Auto-Router"); got != testFallback+`;refusal-fallback;failed_from=["`+testPrimary+`"]` {
		t.Fatalf("X-Auto-Router = %q", got)
	}
	if !strings.Contains(string(response.Payload), "here are the learnings") {
		t.Fatalf("payload = %s", response.Payload)
	}
	if len(fake.executeModels) != 2 || fake.executeModels[0] != testPrimary || fake.executeModels[1] != testFallback {
		t.Fatalf("executed models = %#v", fake.executeModels)
	}
	if string(fake.executeBodies[1]) != string(fake.executeBodies[0]) {
		t.Fatalf("fallback body changed: %s", fake.executeBodies[1])
	}
	fields := refusalLogFields(t, fake, refusalFallbackReason)
	if fields["alias"] != testAlias || fields["primary"] != testPrimary || fields["fallback"] != testFallback || fields["stream"] != false {
		t.Fatalf("log fields = %#v", fields)
	}
}

func TestRefusalFallbackNonStreamKeepsPrimaryAnswer(t *testing.T) {
	configureRefusalTest(t)
	fake := newFakeHostCalls()
	installFakeHost(t, fake)
	fake.executeResponses[testPrimary] = pluginapi.HostModelExecutionResponse{
		StatusCode: http.StatusOK,
		Body:       []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"all good"},"finish_reason":"stop"}]}`),
	}

	raw, err := execute(refusalExecutorRequest(t, testAlias, ""))
	if err != nil {
		t.Fatal(err)
	}
	env, response := decodeHostEnvelope(t, raw)
	if !env.OK {
		t.Fatalf("envelope = %#v", env.Error)
	}
	if got := response.Headers.Get("X-Auto-Router"); got != testPrimary+";refusal-guard" {
		t.Fatalf("X-Auto-Router = %q", got)
	}
	if len(fake.executeModels) != 1 {
		t.Fatalf("executed models = %#v", fake.executeModels)
	}
	if len(fake.logs) != 0 {
		t.Fatalf("unexpected logs = %#v", fake.logs)
	}
}

func TestRefusalFallbackNonStreamErrorDoesNotSwitch(t *testing.T) {
	configureRefusalTest(t)
	fake := newFakeHostCalls()
	installFakeHost(t, fake)
	fake.executeResponses[testPrimary] = pluginapi.HostModelExecutionResponse{
		StatusCode: http.StatusTooManyRequests,
		Body:       []byte(`{"error":{"type":"rate_limit_error"}}`),
	}

	raw, err := execute(refusalExecutorRequest(t, testAlias, ""))
	if err != nil {
		t.Fatal(err)
	}
	env, _ := decodeHostEnvelope(t, raw)
	if env.OK {
		t.Fatal("rate limited primary produced an ok envelope")
	}
	if len(fake.executeModels) != 1 || fake.executeModels[0] != testPrimary {
		t.Fatalf("executed models = %#v", fake.executeModels)
	}
}

func TestRefusalFallbackNonStreamTransportErrorDoesNotSwitch(t *testing.T) {
	configureRefusalTest(t)
	fake := newFakeHostCalls()
	installFakeHost(t, fake)
	fake.executeErrors[testPrimary] = errors.New("auth_unavailable")

	raw, err := execute(refusalExecutorRequest(t, testAlias, ""))
	if err != nil {
		t.Fatal(err)
	}
	env, _ := decodeHostEnvelope(t, raw)
	if env.OK {
		t.Fatal("transport error produced an ok envelope")
	}
	if len(fake.executeModels) != 1 {
		t.Fatalf("executed models = %#v", fake.executeModels)
	}
}

func TestRefusalFallbackStreamSwitchesBeforeFirstOutput(t *testing.T) {
	configureRefusalTest(t)
	fake := newFakeHostCalls()
	installFakeHost(t, fake)
	fake.streamResponses[testPrimary] = pluginapi.HostModelStreamResponse{StatusCode: http.StatusOK, StreamID: "stream-primary"}
	fake.streamResponses[testFallback] = pluginapi.HostModelStreamResponse{StatusCode: http.StatusOK, StreamID: "stream-fallback"}
	fake.streamReads["stream-primary"] = []fakeStreamRead{
		{response: pluginapi.HostModelStreamReadResponse{Payload: []byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"},\"finish_reason\":null}]}\n\n")}},
		{response: pluginapi.HostModelStreamReadResponse{Payload: []byte("data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"content_filter\"}]}\n\n"), Done: true}},
	}
	fake.streamReads["stream-fallback"] = []fakeStreamRead{
		{response: pluginapi.HostModelStreamReadResponse{Payload: []byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"uncensored answer\"},\"finish_reason\":null}]}\n\n")}},
		{response: pluginapi.HostModelStreamReadResponse{Payload: []byte("data: [DONE]\n\n"), Done: true}},
	}

	raw, err := executeStream(refusalExecutorRequest(t, testAlias, "plugin-stream-refusal"))
	if err != nil {
		t.Fatal(err)
	}
	if got := streamHeaderValue(t, raw, "X-Auto-Router"); got != testFallback+`;refusal-fallback;failed_from=["`+testPrimary+`"]` {
		t.Fatalf("X-Auto-Router = %q", got)
	}
	if closeRequest := waitPluginClose(t, fake); closeRequest.Error != "" {
		t.Fatalf("stream closed with %q", closeRequest.Error)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.emits) != 2 || !strings.Contains(fake.emits[0], "uncensored answer") {
		t.Fatalf("emitted chunks = %#v", fake.emits)
	}
	for _, emitted := range fake.emits {
		if strings.Contains(emitted, "content_filter") {
			t.Fatalf("refused primary chunk reached the client: %#v", fake.emits)
		}
	}
	if len(fake.streamModels) != 2 || fake.streamModels[0] != testPrimary || fake.streamModels[1] != testFallback {
		t.Fatalf("stream models = %#v", fake.streamModels)
	}
	if string(fake.streamBodies[1]) != string(fake.streamBodies[0]) {
		t.Fatalf("fallback stream body changed: %s", fake.streamBodies[1])
	}
}

func TestRefusalFallbackStreamSwitchesAfterLongReasoningPrelude(t *testing.T) {
	configureRefusalTest(t)
	fake := newFakeHostCalls()
	installFakeHost(t, fake)
	fake.streamResponses[testPrimary] = pluginapi.HostModelStreamResponse{StatusCode: http.StatusOK, StreamID: "stream-primary"}
	fake.streamResponses[testFallback] = pluginapi.HostModelStreamResponse{StatusCode: http.StatusOK, StreamID: "stream-fallback"}
	// 200 reasoning chunks stand in for a high reasoning effort prelude: well
	// past the old 64-chunk ceiling, and no visible token before the refusal.
	reads := make([]fakeStreamRead, 0, 201)
	for i := range 200 {
		payload := fmt.Sprintf("data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"step %d\"},\"finish_reason\":null}]}\n\n", i)
		reads = append(reads, fakeStreamRead{response: pluginapi.HostModelStreamReadResponse{Payload: []byte(payload)}})
	}
	reads = append(reads, fakeStreamRead{response: pluginapi.HostModelStreamReadResponse{
		Payload: []byte("data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"content_filter\"}]}\n\n"),
		Done:    true,
	}})
	fake.streamReads["stream-primary"] = reads
	fake.streamReads["stream-fallback"] = []fakeStreamRead{
		{response: pluginapi.HostModelStreamReadResponse{Payload: []byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"uncensored answer\"},\"finish_reason\":null}]}\n\n"), Done: true}},
	}

	raw, err := executeStream(refusalExecutorRequest(t, testAlias, "plugin-stream-reasoning-refusal"))
	if err != nil {
		t.Fatal(err)
	}
	if got := streamHeaderValue(t, raw, "X-Auto-Router"); got != testFallback+`;refusal-fallback;failed_from=["`+testPrimary+`"]` {
		t.Fatalf("X-Auto-Router = %q", got)
	}
	if closeRequest := waitPluginClose(t, fake); closeRequest.Error != "" {
		t.Fatalf("stream closed with %q", closeRequest.Error)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.emits) != 1 || !strings.Contains(fake.emits[0], "uncensored answer") {
		t.Fatalf("emitted chunks = %#v", fake.emits)
	}
	if len(fake.streamModels) != 2 || fake.streamModels[1] != testFallback {
		t.Fatalf("stream models = %#v", fake.streamModels)
	}
}

func TestRefusalFallbackStreamKeepsPrimaryWhenReasoningPrecededContent(t *testing.T) {
	configureRefusalTest(t)
	fake := newFakeHostCalls()
	installFakeHost(t, fake)
	fake.streamResponses[testPrimary] = pluginapi.HostModelStreamResponse{StatusCode: http.StatusOK, StreamID: "stream-primary"}
	fake.streamReads["stream-primary"] = []fakeStreamRead{
		{response: pluginapi.HostModelStreamReadResponse{Payload: []byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"weighing it\"},\"finish_reason\":null}]}\n\n")}},
		{response: pluginapi.HostModelStreamReadResponse{Payload: []byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"here it is\"},\"finish_reason\":null}]}\n\n")}},
		{response: pluginapi.HostModelStreamReadResponse{Payload: []byte("data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"), Done: true}},
	}

	raw, err := executeStream(refusalExecutorRequest(t, testAlias, "plugin-stream-reasoning-then-content"))
	if err != nil {
		t.Fatal(err)
	}
	if got := streamHeaderValue(t, raw, "X-Auto-Router"); got != testPrimary+";refusal-guard" {
		t.Fatalf("X-Auto-Router = %q", got)
	}
	if closeRequest := waitPluginClose(t, fake); closeRequest.Error != "" {
		t.Fatalf("stream closed with %q", closeRequest.Error)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	// The buffered reasoning chunk is flushed with the rest: nothing is lost
	// when the guard stands down.
	if len(fake.emits) != 3 || !strings.Contains(fake.emits[0], "weighing it") {
		t.Fatalf("emitted chunks = %#v", fake.emits)
	}
	if len(fake.streamModels) != 1 {
		t.Fatalf("stream models = %#v", fake.streamModels)
	}
	if len(fake.logs) != 0 {
		t.Fatalf("unexpected logs = %#v", fake.logs)
	}
}

func TestRefusalFallbackStreamKeepsPrimaryWhenOutputCameFirst(t *testing.T) {
	configureRefusalTest(t)
	fake := newFakeHostCalls()
	installFakeHost(t, fake)
	fake.streamResponses[testPrimary] = pluginapi.HostModelStreamResponse{StatusCode: http.StatusOK, StreamID: "stream-primary"}
	fake.streamReads["stream-primary"] = []fakeStreamRead{
		{response: pluginapi.HostModelStreamReadResponse{Payload: []byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"},\"finish_reason\":null}]}\n\n")}},
		{response: pluginapi.HostModelStreamReadResponse{Payload: []byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial answer\"},\"finish_reason\":null}]}\n\n")}},
		{response: pluginapi.HostModelStreamReadResponse{Payload: []byte("data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"content_filter\"}]}\n\n"), Done: true}},
	}

	raw, err := executeStream(refusalExecutorRequest(t, testAlias, "plugin-stream-late-refusal"))
	if err != nil {
		t.Fatal(err)
	}
	if got := streamHeaderValue(t, raw, "X-Auto-Router"); got != testPrimary+";refusal-guard" {
		t.Fatalf("X-Auto-Router = %q", got)
	}
	if closeRequest := waitPluginClose(t, fake); closeRequest.Error != "" {
		t.Fatalf("stream closed with %q", closeRequest.Error)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.emits) != 3 {
		t.Fatalf("emitted chunks = %#v", fake.emits)
	}
	if len(fake.streamModels) != 1 || fake.streamModels[0] != testPrimary {
		t.Fatalf("stream models = %#v", fake.streamModels)
	}
	for _, entry := range fake.logs {
		fields := decisionLogFields(t, entry)
		if fields["reason"] == refusalAfterOutputLog && fields["stream"] == true {
			return
		}
	}
	t.Fatalf("missing refusal_after_output log: %#v", fake.logs)
}

func TestRefusalAliasRoutesWithoutJevAndUnknownAliasIsNotClaimed(t *testing.T) {
	configureRefusalTest(t)
	fake := newFakeHostCalls()
	installFakeHost(t, fake)
	request := pluginapi.ModelRouteRequest{
		RequestedModel: testAlias,
		SourceFormat:   formatChatCompletions,
		Headers:        http.Header{"X-Session-ID": []string{"alias-session"}},
		Body:           []byte(`{"messages":[{"role":"user","content":"curate the learnings"}]}`),
	}
	raw, err := routeModel(marshalRoute(t, request))
	if err != nil {
		t.Fatal(err)
	}
	response := decodeRouteResponse(t, raw)
	if !response.Handled || response.TargetKind != pluginapi.ModelRouteTargetSelf || response.Reason != refusalGuardReason {
		t.Fatalf("alias route = %+v", response)
	}
	if _, ok := pending.Load(requestKey(request.SourceFormat, request.Headers, request.Body)); ok {
		t.Fatal("alias route wrote a pending Jev decision")
	}
	if len(fake.logs) != 0 {
		t.Fatalf("alias route logged a decision: %#v", fake.logs)
	}

	request.RequestedModel = "claude-opus-5-5-unguarded"
	raw, err = routeModel(marshalRoute(t, request))
	if err != nil {
		t.Fatal(err)
	}
	if decodeRouteResponse(t, raw).Handled {
		t.Fatal("unconfigured alias was claimed")
	}
}

func TestRefusalAliasAbsentFromConfigIsNotClaimed(t *testing.T) {
	configureTest(t)
	request := pluginapi.ModelRouteRequest{
		RequestedModel: testAlias,
		SourceFormat:   formatChatCompletions,
		Body:           []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
	}
	raw, err := routeModel(marshalRoute(t, request))
	if err != nil {
		t.Fatal(err)
	}
	if decodeRouteResponse(t, raw).Handled {
		t.Fatal("alias claimed without refusal_fallback config")
	}
}

func TestModelRegistrationIncludesConfiguredAliases(t *testing.T) {
	configureTest(t)
	if models := modelRegistration().Models; len(models) != 1 || models[0].ID != pluginIdentifier {
		t.Fatalf("models without refusal_fallback = %#v", models)
	}
	configureRefusalTest(t)
	models := modelRegistration().Models
	if len(models) != 2 || models[1].ID != testAlias || models[1].OwnedBy != pluginIdentifier {
		t.Fatalf("models with refusal_fallback = %#v", models)
	}
}

func TestNormalizeRefusalFallbackDropsIncompleteEntries(t *testing.T) {
	entries := normalizeRefusalFallback([]refusalFallbackAlias{
		{Alias: "  spaced  ", Primary: " p ", Fallback: " f "},
		{Alias: "no-primary", Fallback: "f"},
		{Alias: "no-fallback", Primary: "p"},
		{Alias: pluginIdentifier, Primary: "p", Fallback: "f"},
	})
	if len(entries) != 1 || entries[0] != (refusalFallbackAlias{Alias: "spaced", Primary: "p", Fallback: "f"}) {
		t.Fatalf("normalized = %#v", entries)
	}
}

func streamHeaderValue(t *testing.T, raw []byte, key string) string {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if !env.OK {
		t.Fatalf("stream envelope = %s", raw)
	}
	var result struct {
		Headers http.Header `json:"headers"`
	}
	if err := json.Unmarshal(env.Result, &result); err != nil {
		t.Fatal(err)
	}
	return result.Headers.Get(key)
}
