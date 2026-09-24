package snippet

import "testing"

func TestExtractChatTailAndSignals(t *testing.T) {
	body := []byte(`{"tools":[{"type":"function"},{"type":"function"}],"messages":[{"role":"system","content":"do not send"},{"role":"user","content":"old"},{"role":"assistant","content":"answer"},{"role":"user","content":"hello 世界"}]}`)
	text, sig := Extract("chat-completions", body, 6)
	if text != "llo 世界" {
		t.Fatalf("tail = %q", text)
	}
	if sig.Tools != 2 || sig.Images != 0 || sig.Messages != 4 || sig.Format != "chat-completions" || !sig.HasNewUserMessage {
		t.Fatalf("signals = %+v", sig)
	}
}

func TestExtractResponsesImagesAndToolOnly(t *testing.T) {
	body := []byte(`{"tools":[{"type":"function"}],"input":[{"role":"user","content":[{"type":"input_text","text":"describe"},{"type":"input_image","image_url":"ignored"}]}]}`)
	text, sig := Extract("responses", body, 100)
	if text != "describe" {
		t.Fatalf("text = %q", text)
	}
	if sig.Tools != 1 || sig.Images != 1 || sig.Messages != 1 || sig.Format != "responses" || !sig.HasNewUserMessage {
		t.Fatalf("signals = %+v", sig)
	}

	toolOnly := []byte(`{"messages":[{"role":"assistant","content":"answer"},{"role":"tool","content":"private tool result"}]}`)
	text, sig = Extract("chat-completions", toolOnly, 100)
	if text != "" || sig.HasNewUserMessage {
		t.Fatalf("tool-only = text %q signals %+v", text, sig)
	}

	responsesToolOnly := []byte(`{"input":[{"type":"function_call_output","output":"secret tool result"}]}`)
	text, sig = Extract("responses", responsesToolOnly, 100)
	if text != "" || sig.HasNewUserMessage {
		t.Fatalf("responses tool-only = text %q signals %+v", text, sig)
	}
}

func TestExtractResponsesStringAndUnicodeTail(t *testing.T) {
	text, sig := Extract("responses", []byte(`{"input":"你好，世界"}`), 3)
	if text != "，世界" {
		t.Fatalf("unicode tail = %q", text)
	}
	if sig.Messages != 1 || !sig.HasNewUserMessage {
		t.Fatalf("signals = %+v", sig)
	}
}
