package snippet

import (
	"strings"

	"github.com/tidwall/gjson"
)

type Signals struct {
	Tools             int    `json:"tools"`
	Images            int    `json:"images"`
	Messages          int    `json:"messages"`
	Format            string `json:"format"`
	HasNewUserMessage bool   `json:"-"`
	// ToolErrorStreak counts the trailing tool results that carry an explicit
	// error marker. Free-form text is never inspected for failure, and markers
	// naming an access or environment problem are excluded because no model
	// tier resolves them.
	ToolErrorStreak int `json:"tool_error_streak"`
	// ToolErrorEpisode identifies that trailing run by the call it started
	// with, so a longer run of the same failures stays one episode and a fresh
	// run after any progress is a different one.
	ToolErrorEpisode string `json:"-"`
}

// notCapability lists access and environment problems that a stronger model
// cannot fix, so a marked failure of this kind never counts toward the streak.
var notCapability = []string{
	"authentication", "authenticate", "unauthorized", "401", "403",
	"permission denied", "forbidden", "access denied",
	"credential", "api key", "token expired", "quota", "billing", "payment",
}

// executionEnvelopeFailure reports a non-zero exit from a tool execution
// envelope. hermes-lab captures show the terminal tool returning
// {"output":..., "exit_code":N, "error":null} as a JSON string, so both the
// output and the numeric exit_code must be present before the code is read.
// Requiring that shape keeps a user document that merely contains an "error"
// key from masquerading as execution metadata.
func executionEnvelopeFailure(payload gjson.Result) bool {
	if !payload.IsObject() || !payload.Get("output").Exists() {
		return false
	}
	code := payload.Get("exit_code")
	return code.Type == gjson.Number && code.Int() != 0
}

// meaningfulError reports whether an error field carries an actual complaint.
// null, false, 0, "", {} and [] are all absence of failure.
func meaningfulError(value gjson.Result) bool {
	switch value.Type {
	case gjson.String:
		return strings.TrimSpace(value.String()) != ""
	case gjson.JSON:
		raw := strings.TrimSpace(value.Raw)
		return raw != "{}" && raw != "[]"
	default:
		return false
	}
}

// explicitToolError reports whether a tool result declares failure through a
// structured field, either on the result itself or inside its JSON payload.
// Prose is never inspected, so a message that merely mentions an error is inert.
func explicitToolError(item gjson.Result) (marked bool, counts bool) {
	flag := item.Get("is_error")
	status := strings.ToLower(strings.TrimSpace(item.Get("status").String()))
	errorField := item.Get("error")
	content := item.Get("content")
	output := item.Get("output")
	switch {
	case flag.Exists() && flag.Type == gjson.True:
	case status == "failed" || status == "error":
	case meaningfulError(errorField):
	case executionEnvelopeFailure(gjson.Parse(content.String())):
	case executionEnvelopeFailure(gjson.Parse(output.String())):
	default:
		return false, false
	}
	text := strings.ToLower(contentText(content) + " " + output.String() + " " + errorField.String())
	for _, marker := range notCapability {
		if strings.Contains(text, marker) {
			return true, false
		}
	}
	return true, true
}

// Extract returns the last user text, bounded by Unicode characters, and local
// request counters. It never includes system prompts, tool results, or images.
func Extract(format string, body []byte, max int) (string, Signals) {
	sig := Signals{Format: format}
	root := gjson.ParseBytes(body)
	sig.Tools = arrayLen(root.Get("tools"))

	var text string
	var lastIsUserText bool
	if messages := root.Get("messages"); messages.IsArray() {
		items := messages.Array()
		sig.Messages = len(items)
		for _, item := range items {
			textForItem := userText(item)
			if textForItem != "" {
				text = textForItem
			}
			sig.Images += imageCount(item.Get("content"))
		}
		if len(items) > 0 {
			lastIsUserText = userText(items[len(items)-1]) != ""
		}
	} else if input := root.Get("input"); input.Type == gjson.String {
		sig.Messages = 1
		text = strings.TrimSpace(input.String())
		lastIsUserText = text != ""
	} else if input.IsArray() {
		items := input.Array()
		sig.Messages = len(items)
		for _, item := range items {
			textForItem := userText(item)
			if textForItem != "" {
				text = textForItem
			}
			sig.Images += imageCount(item.Get("content"))
			if item.Get("type").String() == "input_image" || item.Get("type").String() == "image" {
				sig.Images++
			}
		}
		if len(items) > 0 {
			lastIsUserText = userText(items[len(items)-1]) != ""
		}
	}
	sig.ToolErrorStreak, sig.ToolErrorEpisode = toolErrorStreak(root)
	if max <= 0 {
		return "", sig
	}
	text = tailRunes(text, max)
	sig.HasNewUserMessage = lastIsUserText && text != ""
	return text, sig
}

func arrayLen(value gjson.Result) int {
	if !value.IsArray() {
		return 0
	}
	return len(value.Array())
}

// toolErrorStreak counts trailing tool results that carry an explicit error
// marker and answer a tool call present in the same request, and returns the
// call id the run started with. Assistant tool calls between results are
// skipped; any unmarked tool result, uncorrelated result, user turn, or
// excluded failure ends the run.
func toolErrorStreak(root gjson.Result) (int, string) {
	items := root.Get("messages").Array()
	if len(items) == 0 {
		items = root.Get("input").Array()
	}
	calls := issuedToolCalls(items)
	streak := 0
	episode := ""
	for i := len(items) - 1; i >= 0; i-- {
		item := items[i]
		itemType := item.Get("type").String()
		isToolResult := item.Get("role").String() == "tool" ||
			itemType == "function_call_output" || itemType == "custom_tool_call_output"
		if !isToolResult {
			if item.Get("role").String() == "assistant" || itemType == "function_call" ||
				itemType == "custom_tool_call" || itemType == "reasoning" {
				continue
			}
			return streak, episode
		}
		callID := item.Get("tool_call_id").String()
		if callID == "" {
			callID = item.Get("call_id").String()
		}
		if callID == "" || !calls[callID] {
			return streak, episode
		}
		marked, counts := explicitToolError(item)
		if !marked || !counts {
			return streak, episode
		}
		streak++
		episode = callID
	}
	return streak, episode
}

// issuedToolCalls collects the call identifiers the assistant actually issued,
// in either request format, so a result cannot claim a failure nobody called.
func issuedToolCalls(items []gjson.Result) map[string]bool {
	calls := make(map[string]bool)
	for _, item := range items {
		for _, call := range item.Get("tool_calls").Array() {
			if id := call.Get("id").String(); id != "" {
				calls[id] = true
			}
		}
		switch item.Get("type").String() {
		case "function_call", "custom_tool_call":
			if id := item.Get("call_id").String(); id != "" {
				calls[id] = true
			}
		}
	}
	return calls
}

func userText(item gjson.Result) string {
	if item.Get("role").String() != "user" {
		return ""
	}
	return strings.TrimSpace(contentText(item.Get("content")))
}

func contentText(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	if !content.IsArray() {
		return ""
	}
	parts := make([]string, 0)
	for _, part := range content.Array() {
		typeName := part.Get("type").String()
		if typeName != "" && typeName != "text" && typeName != "input_text" {
			continue
		}
		if text := strings.TrimSpace(part.Get("text").String()); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func imageCount(content gjson.Result) int {
	if !content.IsArray() {
		return 0
	}
	count := 0
	for _, part := range content.Array() {
		switch part.Get("type").String() {
		case "image_url", "input_image", "image":
			count++
		}
	}
	return count
}

func tailRunes(text string, max int) string {
	if text == "" {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[len(runes)-max:])
}
