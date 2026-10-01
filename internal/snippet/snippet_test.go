package snippet

import "testing"

func TestExtractChatRequestAndSignals(t *testing.T) {
	body := []byte(`{"tools":[{"type":"function"},{"type":"function"}],"messages":[{"role":"system","content":"do not send"},{"role":"user","content":"old"},{"role":"assistant","content":"answer"},{"role":"user","content":"hello 世界"}]}`)
	state, sig := Extract(nil, body, 100)
	if state.Request != "hello 世界" {
		t.Fatalf("request = %q", state.Request)
	}
	if sig.Images != 0 || sig.EstTokens != (len(body)+3)/4 || !sig.HasNewUserMessage {
		t.Fatalf("signals = %+v", sig)
	}
}

func TestExtractResponsesImagesAndToolOnly(t *testing.T) {
	body := []byte(`{"tools":[{"type":"function"}],"input":[{"role":"user","content":[{"type":"input_text","text":"describe"},{"type":"input_image","image_url":"ignored"}]}]}`)
	state, sig := Extract(nil, body, 100)
	if state.Request != "describe" {
		t.Fatalf("request = %q", state.Request)
	}
	if sig.Images != 1 || !sig.HasNewUserMessage {
		t.Fatalf("signals = %+v", sig)
	}

	toolOnly := []byte(`{"messages":[{"role":"assistant","content":"answer"},{"role":"tool","content":"private tool result"}]}`)
	state, sig = Extract(nil, toolOnly, 100)
	if state.Request != "" || sig.HasNewUserMessage {
		t.Fatalf("tool-only = request %q signals %+v", state.Request, sig)
	}

	responsesToolOnly := []byte(`{"input":[{"type":"function_call_output","output":"secret tool result"}]}`)
	state, sig = Extract(nil, responsesToolOnly, 100)
	if state.Request != "" || sig.HasNewUserMessage {
		t.Fatalf("responses tool-only = request %q signals %+v", state.Request, sig)
	}
}

func TestExtractCountsExplicitToolErrorStreakChatCompletions(t *testing.T) {
	body := []byte(`{"messages":[` +
		`{"role":"user","content":"fix the build"},` +
		`{"role":"assistant","tool_calls":[{"id":"c1","function":{"name":"run","arguments":"{}"}}]},` +
		`{"role":"tool","tool_call_id":"c1","content":"exit 1","is_error":true},` +
		`{"role":"assistant","tool_calls":[{"id":"c2","function":{"name":"run","arguments":"{}"}}]},` +
		`{"role":"tool","tool_call_id":"c2","content":"exit 1","is_error":true},` +
		`{"role":"assistant","tool_calls":[{"id":"c3","function":{"name":"run","arguments":"{}"}}]},` +
		`{"role":"tool","tool_call_id":"c3","content":"exit 1","status":"failed"}]}`)
	_, sig := Extract(nil, body, 100)
	if sig.ToolErrorStreak != 3 {
		t.Fatalf("ToolErrorStreak = %d, want 3", sig.ToolErrorStreak)
	}
}

func TestExtractCountsExplicitToolErrorStreakResponses(t *testing.T) {
	body := []byte(`{"input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the build"}]},` +
		`{"type":"function_call","call_id":"c1","name":"run","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"c1","output":"exit 1","status":"failed"},` +
		`{"type":"function_call","call_id":"c2","name":"run","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"c2","output":"exit 1","is_error":true}]}`)
	_, sig := Extract(nil, body, 100)
	if sig.ToolErrorStreak != 2 {
		t.Fatalf("ToolErrorStreak = %d, want 2", sig.ToolErrorStreak)
	}
}

func TestExtractIgnoresUnmarkedAndNonCapabilityToolFailures(t *testing.T) {
	cases := map[string][]byte{
		"unmarked error text": []byte(`{"messages":[{"role":"tool","tool_call_id":"c1","content":"{\"error\":\"exit 1\"}"},{"role":"tool","tool_call_id":"c2","content":"error: exit 1"}]}`),
		"successful output":   []byte(`{"messages":[{"role":"tool","tool_call_id":"c1","content":"ok"},{"role":"tool","tool_call_id":"c2","content":"ok"}]}`),
		"user text":           []byte(`{"messages":[{"role":"user","content":"the tool keeps failing with is_error"}]}`),
		"authentication":      []byte(`{"messages":[{"role":"tool","tool_call_id":"c1","content":"401 authentication required","is_error":true},{"role":"tool","tool_call_id":"c2","content":"401 authentication required","is_error":true}]}`),
		"permission":          []byte(`{"messages":[{"role":"tool","tool_call_id":"c1","content":"permission denied","is_error":true},{"role":"tool","tool_call_id":"c2","content":"permission denied","is_error":true}]}`),
		"explicitly false":    []byte(`{"messages":[{"role":"tool","tool_call_id":"c1","content":"exit 1","is_error":false}]}`),
	}
	for name, body := range cases {
		if _, sig := Extract(nil, body, 100); sig.ToolErrorStreak != 0 {
			t.Fatalf("%s: ToolErrorStreak = %d, want 0", name, sig.ToolErrorStreak)
		}
	}
}

func TestExtractIgnoresUncorrelatedToolErrors(t *testing.T) {
	body := []byte(`{"messages":[{"role":"tool","tool_call_id":"ghost","content":"exit 1","is_error":true}]}`)
	if _, sig := Extract(nil, body, 100); sig.ToolErrorStreak != 0 {
		t.Fatalf("ToolErrorStreak = %d, want 0 for a result with no matching call", sig.ToolErrorStreak)
	}
}

// Envelopes below were captured on hermes-lab from a real Hermes turn against a
// disposable recorder: the terminal tool returns a JSON string in content.
func TestExtractCountsCapturedHermesTerminalFailures(t *testing.T) {
	body := []byte(`{"messages":[` +
		`{"role":"assistant","tool_calls":[{"id":"c1"},{"id":"c2"},{"id":"c3"}]},` +
		`{"role":"tool","tool_call_id":"c1","content":"{\"output\": \"\", \"exit_code\": 7, \"error\": null}"},` +
		`{"role":"tool","tool_call_id":"c2","content":"{\"output\": \"\", \"exit_code\": 7, \"error\": null}"},` +
		`{"role":"tool","tool_call_id":"c3","content":"{\"output\": \"\", \"exit_code\": 7, \"error\": null}"}]}`)
	_, sig := Extract(nil, body, 100)
	if sig.ToolErrorStreak != 3 {
		t.Fatalf("captured failure envelope not detected: streak = %d", sig.ToolErrorStreak)
	}
}

func TestExtractIgnoresCapturedHermesTerminalSuccess(t *testing.T) {
	body := []byte(`{"messages":[` +
		`{"role":"assistant","tool_calls":[{"id":"c1"},{"id":"c2"},{"id":"c3"}]},` +
		`{"role":"tool","tool_call_id":"c1","content":"{\"output\": \"\", \"exit_code\": 0, \"error\": null}"},` +
		`{"role":"tool","tool_call_id":"c2","content":"{\"output\": \"\", \"exit_code\": 0, \"error\": null}"},` +
		`{"role":"tool","tool_call_id":"c3","content":"{\"output\": \"\", \"exit_code\": 0, \"error\": null}"}]}`)
	_, sig := Extract(nil, body, 100)
	if sig.ToolErrorStreak != 0 {
		t.Fatalf("captured success envelope counted as failure: streak = %d", sig.ToolErrorStreak)
	}
}

func TestExtractCountsStructuredFailureInResponsesOutput(t *testing.T) {
	body := []byte(`{"input":[` +
		`{"type":"function_call","call_id":"c1","name":"terminal","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"c1","output":"{\"output\": \"\", \"exit_code\": 7, \"error\": null}"}]}`)
	_, sig := Extract(nil, body, 100)
	if sig.ToolErrorStreak != 1 {
		t.Fatalf("responses structured failure not detected: streak = %d", sig.ToolErrorStreak)
	}
}

func TestExtractIgnoresNonFailureMarkersAndUserDocuments(t *testing.T) {
	cases := map[string]string{
		"error false":        `{"role":"tool","tool_call_id":"c1","content":"x","error":false}`,
		"error zero":         `{"role":"tool","tool_call_id":"c1","content":"x","error":0}`,
		"error empty string": `{"role":"tool","tool_call_id":"c1","content":"x","error":""}`,
		"error empty object": `{"role":"tool","tool_call_id":"c1","content":"x","error":{}}`,
		"error empty array":  `{"role":"tool","tool_call_id":"c1","content":"x","error":[]}`,
		"user document":      `{"role":"tool","tool_call_id":"c1","content":"{\"error\": \"field in the user's own document\"}"}`,
		"exit code zero":     `{"role":"tool","tool_call_id":"c1","content":"{\"output\": \"\", \"exit_code\": 0, \"error\": null}"}`,
		"no exit code":       `{"role":"tool","tool_call_id":"c1","content":"{\"output\": \"text\"}"}`,
	}
	for name, result := range cases {
		body := []byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"c1"}]},` + result + `]}`)
		if _, sig := Extract(nil, body, 100); sig.ToolErrorStreak != 0 {
			t.Fatalf("%s: counted as a failure", name)
		}
	}
}

func TestExtractResetsToolErrorStreakAfterProgress(t *testing.T) {
	body := []byte(`{"messages":[` +
		`{"role":"assistant","tool_calls":[{"id":"c1"},{"id":"c2"},{"id":"c3"},{"id":"c4"}]},` +
		`{"role":"tool","tool_call_id":"c1","content":"exit 1","is_error":true},` +
		`{"role":"tool","tool_call_id":"c2","content":"exit 1","is_error":true},` +
		`{"role":"tool","tool_call_id":"c3","content":"ok"},` +
		`{"role":"tool","tool_call_id":"c4","content":"exit 1","is_error":true}]}`)
	_, sig := Extract(nil, body, 100)
	if sig.ToolErrorStreak != 1 {
		t.Fatalf("ToolErrorStreak = %d, want 1", sig.ToolErrorStreak)
	}
}

func TestExtractResponsesStringInput(t *testing.T) {
	state, sig := Extract(nil, []byte(`{"input":"你好，世界"}`), 100)
	if state.Request != "你好，世界" || state.Session.Depth != "new session" || !sig.HasNewUserMessage {
		t.Fatalf("state = %+v, signals = %+v", state, sig)
	}
}
