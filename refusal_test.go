package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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

const (
	proseChatRequest      = `{"messages":[{"role":"user","content":"curate the learnings"}]}`
	proseResponsesRequest = `{"input":[{"role":"user","content":[{"type":"input_text","text":"curate the learnings"}]}]}`
)

// proseJev is a fake Jev endpoint that records the response text it was asked about.
type proseJev struct {
	url  string
	mu   sync.Mutex
	seen []string
}

func (j *proseJev) requests() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]string(nil), j.seen...)
}

func startProseJev(t *testing.T, answer http.HandlerFunc) *proseJev {
	t.Helper()
	fake := &proseJev{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			State struct {
				Response string `json:"response"`
			} `json:"state"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		fake.mu.Lock()
		fake.seen = append(fake.seen, payload.State.Response)
		fake.mu.Unlock()
		answer(w, r)
	}))
	t.Cleanup(server.Close)
	fake.url = server.URL
	return fake
}

func jevBody(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }
}

func jevNoul(noul string) http.HandlerFunc {
	return jevBody(`{"answers":{"refuses_task":{"noul":` + noul + `}}}`)
}

func jevHang(w http.ResponseWriter, r *http.Request) {
	select {
	case <-r.Context().Done():
	case <-time.After(2 * time.Second):
	}
}

func configureProseRefusalTest(t *testing.T, jevURL string, prose bool) {
	t.Helper()
	t.Setenv("TEST_JEV_KEY", "test-key")
	pending = sync.Map{}
	configYAML := fmt.Sprintf("enabled: true\njev_api_key_env: TEST_JEV_KEY\njev_base_url: %s\njev_timeout_ms: 100\ntable_path: testdata/models.yaml\n"+
		"refusal_fallback:\n  - alias: %s\n    primary: %s\n    fallback: %s\n    prose_refusal: %t\n", jevURL, testAlias, testPrimary, testFallback, prose)
	config, err := json.Marshal(lifecycleRequest{ConfigYAML: []byte(configYAML)})
	if err != nil {
		t.Fatal(err)
	}
	if err := configure(config); err != nil {
		t.Fatal(err)
	}
}

// proseExecutorRequest builds an alias request whose SourceFormat is exactly
// what the host would send (wireFormat), for the response shape named by format.
func proseExecutorRequest(t *testing.T, wireFormat, format, streamID string) []byte {
	t.Helper()
	body := proseChatRequest
	if format == formatResponses {
		body = proseResponsesRequest
	}
	req := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{
		Model:           testAlias,
		SourceFormat:    wireFormat,
		OriginalRequest: []byte(body),
	}}
	req.StreamID = streamID
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// wireFormats are the SourceFormat values the plugin must understand: the host's
// own translator identifiers first, then the ABI aliases the plugin declares.
var wireFormats = map[string]string{
	"openai":           formatChatCompletions,
	"chat-completions": formatChatCompletions,
	"openai-response":  formatResponses,
	"responses":        formatResponses,
}

// The fixtures follow the CLIProxyAPI non-streaming translators: chat-completions
// carries message.content, reasoning_content and tool_calls; responses carries
// output items of type reasoning, message (output_text parts) and function_call.
var proseFixtures = map[string]map[string]string{
	formatChatCompletions: {
		"refusal":      `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"I'm sorry, but I can't help with that request.","reasoning_content":"weighing the request"},"finish_reason":"stop"}]}`,
		"answer":       `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"Here are the curated learnings."},"finish_reason":"stop"}]}`,
		"quoted":       `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"The ticket quotes the old bot: \"I can't help with that.\" The new bot answers it."},"finish_reason":"stop"}]}`,
		"tool_call":    `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"I can't help with that until I read the file.","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"note\":\"I cannot assist with this\"}"}}]},"finish_reason":"tool_calls"}]}`,
		"reasoning":    `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"Here are the curated learnings.","reasoning_content":"I can't help with that, or can I?"},"finish_reason":"stop"}]}`,
		"fallback":     `{"id":"c2","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"fallback answer"},"finish_reason":"stop"}]}`,
		"refusal_ptbr": `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"Desculpe, não posso ajudar com esse pedido.","reasoning_content":"weighing the request"},"finish_reason":"stop"}]}`,
	},
	formatResponses: {
		"refusal":      `{"id":"r1","object":"response","status":"completed","incomplete_details":null,"output":[{"id":"rs_1","type":"reasoning","status":"completed","summary":[{"type":"summary_text","text":"weighing the request"}]},{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","annotations":[],"text":"I can’t assist with this request."}]}]}`,
		"answer":       `{"id":"r1","object":"response","status":"completed","incomplete_details":null,"output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","annotations":[],"text":"Here are the curated learnings."}]}]}`,
		"quoted":       `{"id":"r1","object":"response","status":"completed","incomplete_details":null,"output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","annotations":[],"text":"The ticket quotes the old bot: \"I can't assist with this.\" The new bot answers it."}]}]}`,
		"tool_call":    `{"id":"r1","object":"response","status":"completed","incomplete_details":null,"output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","annotations":[],"text":"I can't assist until I read the file."}]},{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"read_file","arguments":"{\"note\":\"I cannot assist with this\"}"}]}`,
		"reasoning":    `{"id":"r1","object":"response","status":"completed","incomplete_details":null,"output":[{"id":"rs_1","type":"reasoning","status":"completed","summary":[{"type":"summary_text","text":"I cannot help with this, or can I?"}]},{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","annotations":[],"text":"Here are the curated learnings."}]}]}`,
		"fallback":     `{"id":"r2","object":"response","status":"completed","incomplete_details":null,"output":[{"id":"msg_2","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","annotations":[],"text":"fallback answer"}]}]}`,
		"refusal_ptbr": `{"id":"r1","object":"response","status":"completed","incomplete_details":null,"output":[{"id":"rs_1","type":"reasoning","status":"completed","summary":[{"type":"summary_text","text":"weighing the request"}]},{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","annotations":[],"text":"Desculpe, não posso ajudar com esse pedido."}]}]}`,
	},
}

// proseVisibleText is the answer text of each fixture: what Jev must receive,
// without the reasoning the same fixture carries.
var proseVisibleText = map[string]map[string]string{
	formatChatCompletions: {
		"refusal":      "I'm sorry, but I can't help with that request.",
		"refusal_ptbr": "Desculpe, não posso ajudar com esse pedido.",
		"quoted":       `The ticket quotes the old bot: "I can't help with that." The new bot answers it.`,
	},
	formatResponses: {
		"refusal":      "I can’t assist with this request.",
		"refusal_ptbr": "Desculpe, não posso ajudar com esse pedido.",
		"quoted":       `The ticket quotes the old bot: "I can't assist with this." The new bot answers it.`,
	},
}

func TestProseRefusalCueSeparatesRefusalsFromOtherDeclines(t *testing.T) {
	refusals := []string{
		"I'm sorry, but I can't help with that.",
		"I cannot assist with this request.",
		"I am unable to provide that information.",
		"I’m not able to write that.",
		"I must decline this request.",
		"That goes against my guidelines.",
		"This would violate the usage policies.",
		"I'm sorry, but I cannot continue with this request.",
		"Unfortunately, I cannot complete this request.",
		"I won't be creating that content.",
		"I'm not comfortable producing that.",
		"Sorry, this is something I have to refuse.",
		"I do not provide instructions of that kind.",
		"Desculpe, não posso ajudar com esse pedido.",
		"Nao posso fornecer esse conteudo.",
		"Não consigo criar esse texto.",
		"Não vou gerar esse conteúdo.",
		"Não posso atender a esse pedido por motivos de segurança.",
		"Me recuso a produzir isso.",
		"Isso vai contra minhas diretrizes.",
		"Isso viola as minhas diretrizes.",
		"O pedido contradiz a política de conteúdo do provedor.",
	}
	others := []string{
		"Here are the curated learnings.",
		"I can't access the file at that path; the tool returned 403.",
		"I cannot find any evidence for that claim in the logs.",
		"Iran's policy changed in 2024.",
		"Aqui estão os aprendizados curados.",
		"Não posso confirmar isso sem os logs do deploy.",
		"Não encontrei evidência disso no repositório.",
		"O cliente recusa a proposta de preço.",
	}
	for _, text := range refusals {
		if !proseRefusalCue.MatchString(strings.ReplaceAll(text, "’", "'")) {
			t.Errorf("refusal not matched: %q", text)
		}
	}
	for _, text := range others {
		if proseRefusalCue.MatchString(strings.ReplaceAll(text, "’", "'")) {
			t.Errorf("non-refusal matched: %q", text)
		}
	}
}

func TestProseRefusalNonStream(t *testing.T) {
	tests := []struct {
		name         string
		prose        bool
		primary      string
		jev          http.HandlerFunc
		wantJevCalls int
		wantFallback bool
		wantLog      string
	}{
		{"confirmed refusal switches", true, "refusal", jevNoul("0.97"), 1, true, refusalProseReason},
		{"confirmed portuguese refusal switches", true, "refusal_ptbr", jevNoul("0.97"), 1, true, refusalProseReason},
		{"uncertain jev keeps primary", true, "refusal", jevNoul("0.5"), 1, false, ""},
		{"no refusal phrase skips jev", true, "answer", jevNoul("0.97"), 0, false, ""},
		{"quoted refusal rejected by jev", true, "quoted", jevNoul("0.04"), 1, false, ""},
		{"jev timeout keeps primary", true, "refusal", jevHang, 1, false, proseRefusalUnconfirmedLog},
		{"jev malformed keeps primary", true, "refusal", jevBody(`{"answers":{"refuses_task":{"noul":"high"}}}`), 1, false, proseRefusalUnconfirmedLog},
		{"jev missing answer keeps primary", true, "refusal", jevBody(`{"answers":{}}`), 1, false, proseRefusalUnconfirmedLog},
		{"tool call is never a refusal", true, "tool_call", jevNoul("0.97"), 0, false, ""},
		{"reasoning is not refusal text", true, "reasoning", jevNoul("0.97"), 0, false, ""},
		{"portuguese refusal without opt-in skips jev", false, "refusal_ptbr", jevNoul("0.97"), 0, false, ""},
		{"alias without opt-in skips jev", false, "refusal", jevNoul("0.97"), 0, false, ""},
	}
	for wireFormat, format := range wireFormats {
		for _, testCase := range tests {
			t.Run(wireFormat+"/"+testCase.name, func(t *testing.T) {
				jev := startProseJev(t, testCase.jev)
				configureProseRefusalTest(t, jev.url, testCase.prose)
				fake := newFakeHostCalls()
				installFakeHost(t, fake)
				primaryBody := proseFixtures[format][testCase.primary]
				fallbackBody := proseFixtures[format]["fallback"]
				fake.executeResponses[testPrimary] = pluginapi.HostModelExecutionResponse{StatusCode: http.StatusOK, Body: []byte(primaryBody)}
				fake.executeResponses[testFallback] = pluginapi.HostModelExecutionResponse{StatusCode: http.StatusOK, Body: []byte(fallbackBody)}

				raw, err := execute(proseExecutorRequest(t, wireFormat, format, ""))
				if err != nil {
					t.Fatal(err)
				}
				env, response := decodeHostEnvelope(t, raw)
				if !env.OK {
					t.Fatalf("envelope = %#v", env.Error)
				}
				seen := jev.requests()
				if len(seen) != testCase.wantJevCalls {
					t.Fatalf("jev calls = %d, want %d", len(seen), testCase.wantJevCalls)
				}
				// Jev sees the visible answer only, never the reasoning.
				if len(seen) > 0 && seen[0] != proseVisibleText[format][testCase.primary] {
					t.Fatalf("jev response state = %q", seen[0])
				}
				if testCase.wantFallback {
					if string(response.Payload) != fallbackBody {
						t.Fatalf("payload = %s", response.Payload)
					}
					if got := response.Headers.Get("X-Auto-Router"); got != testFallback+`;refusal-fallback;failed_from=["`+testPrimary+`"]` {
						t.Fatalf("X-Auto-Router = %q", got)
					}
					if len(fake.executeModels) != 2 || fake.executeModels[1] != testFallback {
						t.Fatalf("executed models = %#v", fake.executeModels)
					}
					if string(fake.executeBodies[1]) != string(fake.executeBodies[0]) {
						t.Fatalf("fallback body changed: %s", fake.executeBodies[1])
					}
				} else {
					if string(response.Payload) != primaryBody {
						t.Fatalf("primary payload changed: %s", response.Payload)
					}
					if got := response.Headers.Get("X-Auto-Router"); got != testPrimary+";refusal-guard" {
						t.Fatalf("X-Auto-Router = %q", got)
					}
					if len(fake.executeModels) != 1 {
						t.Fatalf("executed models = %#v", fake.executeModels)
					}
				}
				if testCase.wantLog != "" {
					refusalLogFields(t, fake, testCase.wantLog)
				} else if !testCase.wantFallback && len(fake.logs) != 0 {
					t.Fatalf("unexpected logs = %#v", fake.logs)
				}
				for _, entry := range fake.logs {
					logged, _ := json.Marshal(entry)
					if strings.Contains(string(logged), "with this") || strings.Contains(string(logged), "with that") {
						t.Fatalf("log carries response text: %s", logged)
					}
				}
			})
		}
	}
}

func TestProseRefusalLeavesStreamingUnchanged(t *testing.T) {
	jev := startProseJev(t, jevNoul("0.97"))
	configureProseRefusalTest(t, jev.url, true)
	fake := newFakeHostCalls()
	installFakeHost(t, fake)
	fake.streamResponses[testPrimary] = pluginapi.HostModelStreamResponse{StatusCode: http.StatusOK, StreamID: "stream-primary"}
	fake.streamReads["stream-primary"] = []fakeStreamRead{
		{response: pluginapi.HostModelStreamReadResponse{Payload: []byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"I can't help with that request.\"},\"finish_reason\":null}]}\n\n")}},
		{response: pluginapi.HostModelStreamReadResponse{Payload: []byte("data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"), Done: true}},
	}

	raw, err := executeStream(proseExecutorRequest(t, "openai", formatChatCompletions, "plugin-stream-prose"))
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
	if len(fake.emits) != 2 || !strings.Contains(fake.emits[0], "help with that request") {
		t.Fatalf("emitted chunks = %#v", fake.emits)
	}
	if len(fake.streamModels) != 1 || fake.streamModels[0] != testPrimary {
		t.Fatalf("stream models = %#v", fake.streamModels)
	}
	if calls := len(jev.requests()); calls != 0 {
		t.Fatalf("streaming called jev %d times", calls)
	}
}

func TestRequestFormatMapsHostIdentifiersAndAbiAliases(t *testing.T) {
	for raw, want := range map[string]string{
		"openai":           formatChatCompletions,
		"chat-completions": formatChatCompletions,
		"chat_completions": formatChatCompletions,
		" OpenAI ":         formatChatCompletions,
		"openai-response":  formatResponses,
		"responses":        formatResponses,
		"openai-responses": formatResponses,
		"claude":           "",
		"gemini":           "",
		"openai-image":     "",
		"":                 "",
	} {
		if got := requestFormat(raw); got != want {
			t.Errorf("requestFormat(%q) = %q, want %q", raw, got, want)
		}
	}
}

// The structured content_filter guard predates the prose check and reads the
// same SourceFormat, so it is pinned against the host's own identifiers too.
func TestStructuredRefusalSwitchesUnderHostSourceFormats(t *testing.T) {
	structured := map[string]string{
		formatChatCompletions: `{"choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"content_filter"}]}`,
		formatResponses:       `{"status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[]}`,
	}
	for _, wireFormat := range []string{"openai", "openai-response"} {
		t.Run(wireFormat, func(t *testing.T) {
			jev := startProseJev(t, jevNoul("0.97"))
			// prose_refusal is off: the structured guard alone must switch.
			configureProseRefusalTest(t, jev.url, false)
			fake := newFakeHostCalls()
			installFakeHost(t, fake)
			format := wireFormats[wireFormat]
			fake.executeResponses[testPrimary] = pluginapi.HostModelExecutionResponse{StatusCode: http.StatusOK, Body: []byte(structured[format])}
			fake.executeResponses[testFallback] = pluginapi.HostModelExecutionResponse{StatusCode: http.StatusOK, Body: []byte(proseFixtures[format]["fallback"])}

			raw, err := execute(proseExecutorRequest(t, wireFormat, format, ""))
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
			if len(fake.executeModels) != 2 || fake.executeModels[1] != testFallback {
				t.Fatalf("executed models = %#v", fake.executeModels)
			}
			if calls := len(jev.requests()); calls != 0 {
				t.Fatalf("structured refusal called jev %d times", calls)
			}
		})
	}
}

func TestStructuredStreamRefusalSwitchesUnderHostSourceFormat(t *testing.T) {
	configureRefusalTest(t)
	fake := newFakeHostCalls()
	installFakeHost(t, fake)
	fake.streamResponses[testPrimary] = pluginapi.HostModelStreamResponse{StatusCode: http.StatusOK, StreamID: "stream-primary"}
	fake.streamResponses[testFallback] = pluginapi.HostModelStreamResponse{StatusCode: http.StatusOK, StreamID: "stream-fallback"}
	fake.streamReads["stream-primary"] = []fakeStreamRead{
		{response: pluginapi.HostModelStreamReadResponse{Payload: []byte("data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"content_filter\"}]}\n\n"), Done: true}},
	}
	fake.streamReads["stream-fallback"] = []fakeStreamRead{
		{response: pluginapi.HostModelStreamReadResponse{Payload: []byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"fallback answer\"},\"finish_reason\":null}]}\n\n"), Done: true}},
	}

	raw, err := executeStream(proseExecutorRequest(t, "openai", formatChatCompletions, "plugin-stream-host-format"))
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
	if len(fake.emits) != 1 || !strings.Contains(fake.emits[0], "fallback answer") {
		t.Fatalf("emitted chunks = %#v", fake.emits)
	}
	if len(fake.streamModels) != 2 || fake.streamModels[1] != testFallback {
		t.Fatalf("stream models = %#v", fake.streamModels)
	}
}
