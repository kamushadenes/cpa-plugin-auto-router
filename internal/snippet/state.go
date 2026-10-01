package snippet

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
)

// State is what Jev sees, ported from dirien/jev-router (src/jev.mjs
// buildState, commit f9093109): the request and a little context, never tool
// output, file contents or the system prompt. Field order is the order Jev
// reads the keys.
type State struct {
	Request              string   `json:"request"`
	RecentUserTurns      []string `json:"recent_user_turns,omitempty"`
	LastAssistantMessage string   `json:"last_assistant_message,omitempty"`
	Session              Session  `json:"session"`
}

type Session struct {
	Harness     string `json:"harness"`
	Depth       string `json:"depth"`
	RecentTools string `json:"recent_tools,omitempty"`
}

const (
	recentTurnChars     = 600
	assistantChars      = 800
	shortRequestWords   = 30
	recentToolCallCount = 20
)

// Harness text wrapped around or instead of a prompt (Claude Code and Codex
// CLI tags from jev-router), plus Hermes' injected memory-context.
var wrappers = func() []*regexp.Regexp {
	tags := []string{
		"system-reminder", "bash-input", "bash-stdout", "bash-stderr",
		"command-name", "command-message", "command-args",
		"local-command-stdout", "local-command-stderr", "local-command-caveat",
		"persisted-output", "task-notification", "teammate-message",
		"user-memory-input", "user-prompt-submit-hook", "environment_context",
		"user_shell_command", "turn_aborted", "user_instructions", "memory-context",
	}
	out := make([]*regexp.Regexp, 0, len(tags)+1)
	for _, tag := range tags {
		out = append(out, regexp.MustCompile(`(?s)<`+tag+`>.*?</`+tag+`>`))
	}
	// Codex sends AGENTS.md as a user message ending in </INSTRUCTIONS>.
	return append(out, regexp.MustCompile(`(?s)# AGENTS\.md instructions.*?</INSTRUCTIONS>`))
}()

var (
	codeFence = regexp.MustCompile("(?s)```.*?(?:```|\\z)")
	fenceLang = regexp.MustCompile("^```([\\w+-]*)")
)

type turn struct {
	text   string
	images int
}

func buildState(headers http.Header, items []gjson.Result, max int, estTokens int) State {
	turns := humanTurns(items)
	var latest turn
	if len(turns) > 0 {
		latest = turns[len(turns)-1]
	}
	state := State{Session: Session{Harness: harness(headers), Depth: depth(len(turns), estTokens), RecentTools: recentTools(items)}}
	switch {
	case latest.text != "":
		state.Request = prepare(latest.text, max)
	case latest.images > 0:
		state.Request = fmt.Sprintf("(the user sent %d image(s) and no text)", latest.images)
	}
	for _, earlier := range turns[max0(len(turns)-3):max0(len(turns)-1)] {
		if text := prepare(earlier.text, recentTurnChars); text != "" {
			state.RecentUserTurns = append(state.RecentUserTurns, text)
		}
	}
	// A short "yes, go ahead" inherits the work it approves.
	if len(strings.Fields(latest.text)) < shortRequestWords {
		if text := lastAssistantText(items); text != "" {
			state.LastAssistantMessage = prepare(text, assistantChars)
		}
	}
	return state
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// prepare scrubs secrets before describing code and clipping, as jev-router does.
func prepare(text string, max int) string {
	return clip(describeCode(Scrub(text)), max)
}

// humanTurns are user messages with prose or images and no tool results.
func humanTurns(items []gjson.Result) []turn {
	var turns []turn
	for _, item := range items {
		if item.Get("role").String() != "user" {
			continue
		}
		if typeName := item.Get("type").String(); typeName != "" && typeName != "message" {
			continue
		}
		content := item.Get("content")
		if hasToolResult(content) {
			continue
		}
		t := turn{text: stripWrappers(contentText(content)), images: imageCount(content)}
		if t.text != "" || t.images > 0 {
			turns = append(turns, t)
		}
	}
	return turns
}

func hasToolResult(content gjson.Result) bool {
	for _, part := range content.Array() {
		if part.Get("type").String() == "tool_result" {
			return true
		}
	}
	return false
}

func stripWrappers(text string) string {
	for _, wrapper := range wrappers {
		text = wrapper.ReplaceAllString(text, "")
	}
	return strings.TrimSpace(text)
}

// lastAssistantText is the assistant's last prose, without thinking or tool calls.
func lastAssistantText(items []gjson.Result) string {
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Get("role").String() != "assistant" {
			continue
		}
		content := items[i].Get("content")
		var text string
		if content.Type == gjson.String {
			text = content.String()
		} else {
			parts := make([]string, 0)
			for _, part := range content.Array() {
				if typeName := part.Get("type").String(); typeName == "text" || typeName == "output_text" {
					parts = append(parts, part.Get("text").String())
				}
			}
			text = strings.Join(parts, "\n")
		}
		if text = strings.TrimSpace(text); text != "" {
			return text
		}
	}
	return ""
}

// recentTools is "Bash 6 times, Edit 3 times" over the latest tool calls in
// Anthropic, Responses and Chat Completions shapes.
func recentTools(items []gjson.Result) string {
	var names []string
	for _, item := range items {
		switch item.Get("type").String() {
		case "function_call", "custom_tool_call":
			names = append(names, item.Get("name").String())
			continue
		}
		if item.Get("role").String() != "assistant" {
			continue
		}
		for _, part := range item.Get("content").Array() {
			if part.Get("type").String() == "tool_use" {
				names = append(names, part.Get("name").String())
			}
		}
		for _, call := range item.Get("tool_calls").Array() {
			names = append(names, call.Get("function.name").String())
		}
	}
	if len(names) > recentToolCallCount {
		names = names[len(names)-recentToolCallCount:]
	}
	type count struct {
		name string
		n    int
	}
	var counts []count
	index := map[string]int{}
	for _, name := range names {
		if name == "" {
			continue
		}
		if i, ok := index[name]; ok {
			counts[i].n++
			continue
		}
		index[name] = len(counts)
		counts = append(counts, count{name: name, n: 1})
	}
	sort.SliceStable(counts, func(i, j int) bool { return counts[i].n > counts[j].n })
	parts := make([]string, len(counts))
	for i, c := range counts {
		unit := "times"
		if c.n == 1 {
			unit = "time"
		}
		parts[i] = fmt.Sprintf("%s %d %s", c.name, c.n, unit)
	}
	return strings.Join(parts, ", ")
}

// harness names the coding agent from request headers. OMP and Hermes are not
// listed: their real User-Agent at the proxy has not been captured.
func harness(headers http.Header) string {
	agent := headers.Get("User-Agent")
	if strings.HasPrefix(agent, "claude-cli") || headers.Get("X-Claude-Code-Session-Id") != "" {
		return "Claude Code"
	}
	if strings.HasPrefix(headers.Get("Originator"), "codex") || strings.Contains(strings.ToLower(agent), "codex") {
		return "Codex CLI"
	}
	return "unknown"
}

func depth(turns, estTokens int) string {
	switch {
	case turns == 1:
		return "new session"
	case estTokens < 20000:
		return "early (under 20k tokens)"
	case estTokens < 100000:
		return "mid (20k to 100k tokens)"
	default:
		return "long (over 100k tokens)"
	}
}

// describeCode replaces each code block with a one-line description: Jev
// judges the request, not the pasted code.
func describeCode(text string) string {
	return codeFence.ReplaceAllStringFunc(text, func(block string) string {
		lang := ""
		if m := fenceLang.FindStringSubmatch(block); m != nil && m[1] != "" {
			lang = " (" + m[1] + ")"
		}
		lines := max(strings.Count(block, "\n")-1, 1)
		return fmt.Sprintf("[code block%s, %d lines]", lang, lines)
	})
}

// clip keeps 25% from the start and 75% from the end, in runes: the question
// usually comes after the pasted material.
func clip(text string, max int) string {
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	head := max / 4
	tail := max - head
	return fmt.Sprintf("%s … [%d characters omitted] … %s", string(runes[:head]), len(runes)-max, string(runes[len(runes)-tail:]))
}
