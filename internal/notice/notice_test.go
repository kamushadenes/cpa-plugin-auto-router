package notice

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestInjectAppendsChatCompletionsUserMessage(t *testing.T) {
	body := []byte(`{"model":"auto-router","messages":[{"role":"user","content":"fix the build"},{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"run","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call-1","content":"exit 1"}],"temperature":0.2}`)
	got := Inject("chat-completions", body, Failover("first", "second", "failover"))

	root := gjson.ParseBytes(got)
	messages := root.Get("messages").Array()
	if len(messages) != 4 {
		t.Fatalf("messages = %d, want 4: %s", len(messages), got)
	}
	if role := messages[3].Get("role").String(); role != "user" {
		t.Fatalf("appended role = %q, want user: %s", role, got)
	}
	text := messages[3].Get("content").String()
	if !contains(text, "first") || !contains(text, "second") || !contains(text, "failover") {
		t.Fatalf("notice text = %q", text)
	}
	if messages[2].Get("tool_call_id").String() != "call-1" {
		t.Fatalf("tool pairing changed: %s", got)
	}
	if root.Get("temperature").String() != "0.2" {
		t.Fatalf("temperature changed: %s", got)
	}
}

func TestInjectAppendsResponsesInputMessage(t *testing.T) {
	body := []byte(`{"input":[{"type":"function_call","call_id":"call-1","name":"run","arguments":"{}"},{"type":"function_call_output","call_id":"call-1","output":"exit 1"}]}`)
	got := Inject("responses", body, Failover("first", "second", "failover"))

	items := gjson.ParseBytes(got).Get("input").Array()
	if len(items) != 3 {
		t.Fatalf("input = %d, want 3: %s", len(items), got)
	}
	last := items[2]
	if last.Get("type").String() != "message" || last.Get("role").String() != "user" {
		t.Fatalf("appended item = %s", last.Raw)
	}
	if last.Get("content.0.type").String() != "input_text" {
		t.Fatalf("appended content = %s", last.Raw)
	}
	if !contains(last.Get("content.0.text").String(), "second") {
		t.Fatalf("notice text = %s", last.Raw)
	}
	if items[1].Get("call_id").String() != "call-1" {
		t.Fatalf("tool pairing changed: %s", got)
	}
}

func TestInjectLeavesUnsupportedBodiesUnchanged(t *testing.T) {
	for name, body := range map[string][]byte{
		"string input":     []byte(`{"input":"one shot"}`),
		"missing messages": []byte(`{"model":"auto-router"}`),
		"invalid json":     []byte(`not json`),
		"unknown format":   []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
	} {
		format := "chat-completions"
		if name == "string input" {
			format = "responses"
		}
		if name == "unknown format" {
			format = "gemini"
		}
		if got := Inject(format, body, Failover("first", "second", "failover")); string(got) != string(body) {
			t.Fatalf("%s: body changed to %s", name, got)
		}
	}
}

func TestInjectPreservesLargeIntegerFields(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"}],"seed":12345678901234567890}`)
	got := Inject("chat-completions", body, Failover("first", "second", "failover"))
	if gjson.ParseBytes(got).Get("seed").String() != "12345678901234567890" {
		t.Fatalf("seed lost precision: %s", got)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
