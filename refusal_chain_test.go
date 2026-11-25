package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	chainFirst = "asteras/canopus"
	chainLast  = "orcarouter/auto-cyber"

	chainAnswerBody  = `{"choices":[{"index":0,"message":{"role":"assistant","content":"here are the learnings"},"finish_reason":"stop"}]}`
	chainRefusalBody = `{"choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"content_filter"}]}`
	chainEmptyBody   = `{"choices":[{"index":0,"message":{"role":"assistant","content":"  "},"finish_reason":"stop"}]}`
	chainToolBody    = `{"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c9","type":"function","function":{"name":"run","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`

	chainStreamText    = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"uncensored answer\"},\"finish_reason\":null}]}\n\n"
	chainStreamRole    = "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"},\"finish_reason\":null}]}\n\n"
	chainStreamRefusal = "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"content_filter\"}]}\n\n"
	chainStreamDone    = "data: [DONE]\n\n"
)

// toolFailureBody is a chat-completions request whose trailing tool results
// carry `failures` explicit failures.
func toolFailureBody(failures int) []byte {
	var b strings.Builder
	b.WriteString(`{"messages":[{"role":"user","content":"fix the build"}`)
	for i := range failures {
		id := "c" + string(rune('1'+i))
		b.WriteString(`,{"role":"assistant","tool_calls":[{"id":"` + id + `","function":{"name":"run","arguments":"{}"}}]}`)
		b.WriteString(`,{"role":"tool","tool_call_id":"` + id + `","content":"exit 1","is_error":true}`)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

func configureChainTest(t *testing.T) {
	t.Helper()
	t.Setenv("TEST_JEV_KEY", "test-key")
	configYAML := "enabled: true\njev_api_key_env: TEST_JEV_KEY\njev_base_url: http://127.0.0.1:1\njev_timeout_ms: 20\ntable_path: testdata/models.yaml\n" +
		"refusal_fallback:\n  - alias: " + testAlias + "\n    primary: " + testPrimary + "\n    fallback: [" + chainFirst + ", " + chainLast + "]\n"
	config, err := json.Marshal(lifecycleRequest{ConfigYAML: []byte(configYAML)})
	if err != nil {
		t.Fatal(err)
	}
	if err := configure(config); err != nil {
		t.Fatal(err)
	}
}

func chainRequest(t *testing.T, body []byte, streamID string) []byte {
	t.Helper()
	if body == nil {
		body = []byte(`{"messages":[{"role":"user","content":"curate the learnings"}]}`)
	}
	req := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{
		Model:           testAlias,
		SourceFormat:    formatChatCompletions,
		OriginalRequest: body,
	}}
	req.StreamID = streamID
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// chainSwitches returns every logged switch as "reason:from>fallback" and
// checks the fields every switch log carries.
func chainSwitches(t *testing.T, fake *fakeHostCalls, stream bool) []string {
	t.Helper()
	var switches []string
	for _, entry := range fake.logs {
		fields := decisionLogFields(t, entry)
		if fields["alias"] != testAlias || fields["primary"] != testPrimary || fields["stream"] != stream {
			t.Fatalf("log fields = %#v", fields)
		}
		switches = append(switches, fields["reason"].(string)+":"+fields["from"].(string)+">"+fields["fallback"].(string))
	}
	return switches
}

func chainHeader(model string, failed ...string) string {
	return refusalRouterHeader(model, failed, false)
}

func TestRefusalChainNonStream(t *testing.T) {
	ok := func(body string) pluginapi.HostModelExecutionResponse {
		return pluginapi.HostModelExecutionResponse{StatusCode: http.StatusOK, Body: []byte(body)}
	}
	serverError := pluginapi.HostModelExecutionResponse{StatusCode: http.StatusInternalServerError, Body: []byte(`{"error":{"type":"api_error"}}`)}
	tests := []struct {
		name       string
		body       []byte
		responses  map[string]pluginapi.HostModelExecutionResponse
		errs       map[string]error
		wantModels []string
		wantOK     bool
		wantHeader string
		wantLogs   []string
	}{
		{
			name:       "primary answers",
			responses:  map[string]pluginapi.HostModelExecutionResponse{testPrimary: ok(chainAnswerBody)},
			wantModels: []string{testPrimary}, wantOK: true,
			wantHeader: testPrimary + ";refusal-guard",
		},
		{
			name:       "primary refusal moves to the first fallback",
			responses:  map[string]pluginapi.HostModelExecutionResponse{testPrimary: ok(chainRefusalBody), chainFirst: ok(chainAnswerBody)},
			wantModels: []string{testPrimary, chainFirst}, wantOK: true,
			wantHeader: chainFirst + `;refusal-fallback;failed_from=["` + testPrimary + `"]`,
			wantLogs:   []string{"refusal-fallback:" + testPrimary + ">" + chainFirst},
		},
		{
			name:       "first fallback refusal moves to the second",
			responses:  map[string]pluginapi.HostModelExecutionResponse{testPrimary: ok(chainRefusalBody), chainFirst: ok(chainRefusalBody), chainLast: ok(chainAnswerBody)},
			wantModels: []string{testPrimary, chainFirst, chainLast}, wantOK: true,
			wantHeader: chainLast + `;refusal-fallback;failed_from=["` + testPrimary + `","` + chainFirst + `"]`,
			wantLogs:   []string{"refusal-fallback:" + testPrimary + ">" + chainFirst, "refusal-fallback:" + chainFirst + ">" + chainLast},
		},
		{
			name:       "first fallback error status moves to the second",
			responses:  map[string]pluginapi.HostModelExecutionResponse{testPrimary: ok(chainRefusalBody), chainFirst: serverError, chainLast: ok(chainAnswerBody)},
			wantModels: []string{testPrimary, chainFirst, chainLast}, wantOK: true,
			wantHeader: chainHeader(chainLast, testPrimary, chainFirst),
			wantLogs:   []string{"refusal-fallback:" + testPrimary + ">" + chainFirst, "fallback-error:" + chainFirst + ">" + chainLast},
		},
		{
			name:       "first fallback host call failure moves to the second",
			responses:  map[string]pluginapi.HostModelExecutionResponse{testPrimary: ok(chainRefusalBody), chainLast: ok(chainAnswerBody)},
			errs:       map[string]error{chainFirst: errors.New("auth_unavailable")},
			wantModels: []string{testPrimary, chainFirst, chainLast}, wantOK: true,
			wantHeader: chainHeader(chainLast, testPrimary, chainFirst),
			wantLogs:   []string{"refusal-fallback:" + testPrimary + ">" + chainFirst, "fallback-error:" + chainFirst + ">" + chainLast},
		},
		{
			name:       "primary refusal error moves to the first fallback",
			errs:       map[string]error{testPrimary: errors.New(cyberRefusalError)},
			responses:  map[string]pluginapi.HostModelExecutionResponse{chainFirst: ok(chainAnswerBody)},
			wantModels: []string{testPrimary, chainFirst}, wantOK: true,
			wantHeader: chainHeader(chainFirst, testPrimary),
			wantLogs:   []string{"refusal-error:" + testPrimary + ">" + chainFirst},
		},
		{
			name:       "last fallback error is returned",
			responses:  map[string]pluginapi.HostModelExecutionResponse{testPrimary: ok(chainRefusalBody), chainFirst: serverError, chainLast: serverError},
			wantModels: []string{testPrimary, chainFirst, chainLast},
			wantLogs:   []string{"refusal-fallback:" + testPrimary + ">" + chainFirst, "fallback-error:" + chainFirst + ">" + chainLast},
		},
		{
			name:       "last fallback refusal is returned as-is",
			responses:  map[string]pluginapi.HostModelExecutionResponse{testPrimary: ok(chainRefusalBody), chainFirst: ok(chainRefusalBody), chainLast: ok(chainRefusalBody)},
			wantModels: []string{testPrimary, chainFirst, chainLast}, wantOK: true,
			wantHeader: chainHeader(chainLast, testPrimary, chainFirst),
			wantLogs:   []string{"refusal-fallback:" + testPrimary + ">" + chainFirst, "refusal-fallback:" + chainFirst + ">" + chainLast},
		},
		{
			name:       "primary 5xx without a refusal marker is returned without switching",
			responses:  map[string]pluginapi.HostModelExecutionResponse{testPrimary: serverError},
			wantModels: []string{testPrimary},
		},
		{
			name:       "primary empty answer moves to the first fallback",
			responses:  map[string]pluginapi.HostModelExecutionResponse{testPrimary: ok(chainEmptyBody), chainFirst: ok(chainAnswerBody)},
			wantModels: []string{testPrimary, chainFirst}, wantOK: true,
			wantHeader: chainHeader(chainFirst, testPrimary),
			wantLogs:   []string{"empty-answer:" + testPrimary + ">" + chainFirst},
		},
		{
			name:       "first fallback empty answer moves to the second",
			responses:  map[string]pluginapi.HostModelExecutionResponse{testPrimary: ok(chainRefusalBody), chainFirst: ok(`{"choices":[]}`), chainLast: ok(chainAnswerBody)},
			wantModels: []string{testPrimary, chainFirst, chainLast}, wantOK: true,
			wantHeader: chainHeader(chainLast, testPrimary, chainFirst),
			wantLogs:   []string{"refusal-fallback:" + testPrimary + ">" + chainFirst, "empty-answer:" + chainFirst + ">" + chainLast},
		},
		{
			name:       "primary tool call is not an empty answer",
			responses:  map[string]pluginapi.HostModelExecutionResponse{testPrimary: ok(chainToolBody)},
			wantModels: []string{testPrimary}, wantOK: true,
			wantHeader: testPrimary + ";refusal-guard",
		},
		{
			name:       "three trailing tool failures run on the last fallback only",
			body:       toolFailureBody(3),
			responses:  map[string]pluginapi.HostModelExecutionResponse{chainLast: ok(chainAnswerBody)},
			wantModels: []string{chainLast}, wantOK: true,
			wantHeader: chainLast + ";tool-error-escalation",
			wantLogs:   []string{"tool-error-escalation:" + testPrimary + ">" + chainLast},
		},
		{
			name:       "escalated last fallback error is returned",
			body:       toolFailureBody(4),
			responses:  map[string]pluginapi.HostModelExecutionResponse{chainLast: serverError},
			wantModels: []string{chainLast},
			wantLogs:   []string{"tool-error-escalation:" + testPrimary + ">" + chainLast},
		},
		{
			name:       "two trailing tool failures start at the primary",
			body:       toolFailureBody(2),
			responses:  map[string]pluginapi.HostModelExecutionResponse{testPrimary: ok(chainAnswerBody)},
			wantModels: []string{testPrimary}, wantOK: true,
			wantHeader: testPrimary + ";refusal-guard",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			configureChainTest(t)
			fake := newFakeHostCalls()
			installFakeHost(t, fake)
			for model, response := range testCase.responses {
				fake.executeResponses[model] = response
			}
			for model, err := range testCase.errs {
				fake.executeErrors[model] = err
			}

			raw, err := execute(chainRequest(t, testCase.body, ""))
			if err != nil {
				t.Fatal(err)
			}
			env, response := decodeHostEnvelope(t, raw)
			if env.OK != testCase.wantOK {
				t.Fatalf("envelope ok = %v, error = %#v", env.OK, env.Error)
			}
			if testCase.wantOK {
				if got := response.Headers.Get("X-Auto-Router"); got != testCase.wantHeader {
					t.Fatalf("X-Auto-Router = %q, want %q", got, testCase.wantHeader)
				}
			}
			if !slices.Equal(fake.executeModels, testCase.wantModels) {
				t.Fatalf("executed models = %#v, want %#v", fake.executeModels, testCase.wantModels)
			}
			for i, body := range fake.executeBodies {
				if string(body) != string(fake.executeBodies[0]) {
					t.Fatalf("body %d changed: %s", i, body)
				}
			}
			if got := chainSwitches(t, fake, false); !slices.Equal(got, testCase.wantLogs) {
				t.Fatalf("switch logs = %#v, want %#v", got, testCase.wantLogs)
			}
		})
	}
}

func TestRefusalChainStream(t *testing.T) {
	type stream struct {
		openErr error
		chunks  []string
	}
	tests := []struct {
		name       string
		body       []byte
		streams    map[string]stream
		wantModels []string
		wantOK     bool
		wantHeader string
		wantEmits  []string
		wantLogs   []string
	}{
		{
			name: "primary and first fallback refuse before output",
			streams: map[string]stream{
				testPrimary: {chunks: []string{chainStreamRole, chainStreamRefusal}},
				chainFirst:  {chunks: []string{chainStreamRefusal}},
				chainLast:   {chunks: []string{chainStreamText, chainStreamDone}},
			},
			wantModels: []string{testPrimary, chainFirst, chainLast}, wantOK: true,
			wantHeader: chainHeader(chainLast, testPrimary, chainFirst),
			wantEmits:  []string{chainStreamText, chainStreamDone},
			wantLogs:   []string{"refusal-fallback:" + testPrimary + ">" + chainFirst, "refusal-fallback:" + chainFirst + ">" + chainLast},
		},
		{
			name: "first fallback open error moves to the second",
			streams: map[string]stream{
				testPrimary: {chunks: []string{chainStreamRefusal}},
				chainFirst:  {openErr: errors.New("host_call_failed: upstream unavailable")},
				chainLast:   {chunks: []string{chainStreamText, chainStreamDone}},
			},
			wantModels: []string{testPrimary, chainFirst, chainLast}, wantOK: true,
			wantHeader: chainHeader(chainLast, testPrimary, chainFirst),
			wantEmits:  []string{chainStreamText, chainStreamDone},
			wantLogs:   []string{"refusal-fallback:" + testPrimary + ">" + chainFirst, "fallback-error:" + chainFirst + ">" + chainLast},
		},
		{
			name: "primary stream ending with nothing visible moves to the first fallback",
			streams: map[string]stream{
				testPrimary: {chunks: []string{chainStreamRole, chainStreamDone}},
				chainFirst:  {chunks: []string{chainStreamText, chainStreamDone}},
			},
			wantModels: []string{testPrimary, chainFirst}, wantOK: true,
			wantHeader: chainHeader(chainFirst, testPrimary),
			wantEmits:  []string{chainStreamText, chainStreamDone},
			wantLogs:   []string{"empty-answer:" + testPrimary + ">" + chainFirst},
		},
		{
			name: "first fallback refusal after visible output does not switch",
			streams: map[string]stream{
				testPrimary: {chunks: []string{chainStreamRefusal}},
				chainFirst:  {chunks: []string{chainStreamText, chainStreamRefusal}},
			},
			wantModels: []string{testPrimary, chainFirst}, wantOK: true,
			wantHeader: chainHeader(chainFirst, testPrimary),
			wantEmits:  []string{chainStreamText, chainStreamRefusal},
			wantLogs:   []string{"refusal-fallback:" + testPrimary + ">" + chainFirst, refusalAfterOutputLog + ":" + chainFirst + ">" + chainLast},
		},
		{
			name: "last fallback open error is returned",
			streams: map[string]stream{
				testPrimary: {chunks: []string{chainStreamRefusal}},
				chainFirst:  {chunks: []string{chainStreamRefusal}},
				chainLast:   {openErr: errors.New("host_call_failed: upstream unavailable")},
			},
			wantModels: []string{testPrimary, chainFirst, chainLast},
			wantLogs:   []string{"refusal-fallback:" + testPrimary + ">" + chainFirst, "refusal-fallback:" + chainFirst + ">" + chainLast},
		},
		{
			name: "three trailing tool failures stream from the last fallback only",
			body: toolFailureBody(3),
			streams: map[string]stream{
				chainLast: {chunks: []string{chainStreamText, chainStreamDone}},
			},
			wantModels: []string{chainLast}, wantOK: true,
			wantHeader: chainLast + ";tool-error-escalation",
			wantEmits:  []string{chainStreamText, chainStreamDone},
			wantLogs:   []string{"tool-error-escalation:" + testPrimary + ">" + chainLast},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			configureChainTest(t)
			fake := newFakeHostCalls()
			installFakeHost(t, fake)
			for model, s := range testCase.streams {
				if s.openErr != nil {
					fake.streamErrors[model] = s.openErr
					continue
				}
				fake.streamResponses[model] = pluginapi.HostModelStreamResponse{StatusCode: http.StatusOK, StreamID: "stream-" + model}
				reads := make([]fakeStreamRead, len(s.chunks))
				for i, chunk := range s.chunks {
					reads[i] = fakeStreamRead{response: pluginapi.HostModelStreamReadResponse{Payload: []byte(chunk), Done: i == len(s.chunks)-1}}
				}
				fake.streamReads["stream-"+model] = reads
			}

			raw, err := executeStream(chainRequest(t, testCase.body, "plugin-stream-chain"))
			if err != nil {
				t.Fatal(err)
			}
			if testCase.wantOK {
				if got := streamHeaderValue(t, raw, "X-Auto-Router"); got != testCase.wantHeader {
					t.Fatalf("X-Auto-Router = %q, want %q", got, testCase.wantHeader)
				}
				if closeRequest := waitPluginClose(t, fake); closeRequest.Error != "" {
					t.Fatalf("stream closed with %q", closeRequest.Error)
				}
			} else {
				if env, _ := decodeHostEnvelope(t, raw); env.OK {
					t.Fatal("failed chain produced an ok envelope")
				}
				waitPluginClose(t, fake)
			}
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if !slices.Equal(fake.streamModels, testCase.wantModels) {
				t.Fatalf("stream models = %#v, want %#v", fake.streamModels, testCase.wantModels)
			}
			if !slices.Equal(fake.emits, testCase.wantEmits) {
				t.Fatalf("emitted chunks = %#v, want %#v", fake.emits, testCase.wantEmits)
			}
			if got := chainSwitches(t, fake, true); !slices.Equal(got, testCase.wantLogs) {
				t.Fatalf("switch logs = %#v, want %#v", got, testCase.wantLogs)
			}
		})
	}
}
