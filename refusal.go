package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/kamushadenes/cpa-plugin-auto-router/internal/jev"
	"github.com/kamushadenes/cpa-plugin-auto-router/internal/snippet"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
	"gopkg.in/yaml.v3"
)

// refusalFallbackAlias is one configured alias: requests for Alias run on
// Primary, and move down the ordered Fallback chain only when the current
// model refuses on safety grounds, answers with nothing, or (fallbacks only)
// fails. Primary and every fallback reach the host verbatim, so a thinking
// suffix such as "claude-opus-5-5(high)" belongs in the configured value.
// ProseRefusal also treats a Jev-confirmed prose refusal as a refusal, on the
// non-streaming path only.
type refusalFallbackAlias struct {
	Alias        string         `yaml:"alias"`
	Primary      string         `yaml:"primary"`
	Fallback     fallbackModels `yaml:"fallback"`
	ProseRefusal bool           `yaml:"prose_refusal"`
}

// fallbackModels is the ordered fallback chain. YAML accepts a single string
// (the original form) or a list of strings.
type fallbackModels []string

func (f *fallbackModels) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		*f = fallbackModels{node.Value}
		return nil
	}
	var models []string
	if err := node.Decode(&models); err != nil {
		return err
	}
	*f = models
	return nil
}

const (
	refusalGuardReason      = "refusal-guard"
	refusalFallbackReason   = "refusal-fallback"
	refusalAfterOutputLog   = "refusal_after_output"
	refusalContentFilter    = "content_filter"
	formatChatCompletions   = "chat-completions"
	formatResponses         = "responses"
	responsesIncompleteType = "response.incomplete"
)

// requestFormat maps the entry protocol the host reports in SourceFormat to the
// response shape the refusal detectors read. The host sends a translator
// identifier ("openai", "openai-response"); the names this plugin declares in
// executor_input_formats ("chat-completions", "responses") are aliases the host
// resolves to the same two formats in normalizeExecutorFormatName
// (internal/pluginhost/adapters.go), and it never maps the inbound value back.
// Any other entry protocol, Claude and Gemini included, returns "" and no
// detector claims the response.
func requestFormat(sourceFormat string) string {
	switch strings.ToLower(strings.TrimSpace(sourceFormat)) {
	case "openai", "chat-completions", "chat_completions", "openai-chat-completions", "openai_chat_completions":
		return formatChatCompletions
	case "openai-response", "responses", "openai-responses", "openai_responses":
		return formatResponses
	}
	return ""
}

// ponytail: the guard holds at most 1 MiB of the primary stream, which covers a
// long reasoning prelude before the first visible token. Past that ceiling it
// flushes and streams through unguarded.
const refusalGuardMaxBytes = 1 << 20

// Switch reasons: refusalProseReason is a Jev-confirmed prose refusal;
// refusalErrorReason is an error carrying a refusal marker;
// fallbackErrorReason is any other error from a fallback that is not the last;
// emptyAnswerReason is an answer with no visible text and no tool call;
// toolErrorEscalationReason sends a request whose trailing tool results failed
// straight to the last fallback. proseRefusalUnconfirmedLog logs a prose
// candidate kept because Jev failed.
const (
	refusalProseReason         = "prose-refusal"
	refusalErrorReason         = "refusal-error"
	fallbackErrorReason        = "fallback-error"
	emptyAnswerReason          = "empty-answer"
	toolErrorEscalationReason  = "tool-error-escalation"
	proseRefusalUnconfirmedLog = "prose_refusal_unconfirmed"
)

// proseRefusalThreshold is the Jev probability a prose refusal needs before the
// alias switches. A switch sends the whole conversation to the fallback
// provider, so an uncertain answer keeps the primary response.
const proseRefusalThreshold = 0.8

// proseRefusalCue matches first-person refusal phrasing in English and in
// Brazilian Portuguese (the wiki consumer writes in PT-BR). The verb list is
// deliberately narrow: "I can't access the file" and "I cannot find evidence"
// are not refusals and must not reach Jev. It only decides whether Jev is
// asked; Jev decides whether the answer is a refusal.
var proseRefusalCue = regexp.MustCompile(`(?i)` +
	// "I can't help with that", "I won't be creating that", "I do not provide that"
	`\bi (?:cannot|can't|can not|won't|will not|don't|do not|must not)(?: be)?(?: able to)? ` + proseRefusalVerbs + `\b` +
	// "I'm unable to provide that", "I am not comfortable producing that"
	`|\bi(?:'m| am) (?:unable to|not able to|not going to|not comfortable|not willing to|not permitted to|not allowed to) ` + proseRefusalVerbs + `\b` +
	`|\bi(?: must| have to| need to| will|'m going to| am going to) (?:decline|refuse)\b` +
	`|\bagainst my (?:guidelines|principles|values)\b|\b(?:content|usage) polic(?:y|ies)\b` +
	// Portuguese: accents are optional because models drop them in haste.
	`|\bn[ãa]o (?:posso|poderei|vou poder|consigo|serei capaz de|vou|pretendo|estou (?:disposto|disposta) a) ` + proseRefusalVerbsPT + `\b` +
	`|\b(?:me recuso|devo recusar|preciso recusar|tenho que recusar)\b` +
	`|\bpol[íi]ticas? de (?:conte[úu]do|uso)\b` +
	`|\b(?:contra|violam?) (?:as |os )?(?:minhas|meus) (?:diretrizes|pol[íi]ticas|princ[íi]pios|valores)\b`)

// refusalErrorCue matches a safety refusal the provider delivers as an HTTP
// error instead of a refused 2xx body, e.g. Claude's "claude refusal (cyber):
// Cyber safeguards flagged this message". Only explicit refusal markers count,
// so an ordinary 400 that echoes a parameter name such as content_filter never
// sends the request to the fallback provider. Any other error is returned
// unchanged.
var refusalErrorCue = regexp.MustCompile(`(?i)refusal \(cyber\)|cyber safeguards|content_policy_violation`)

const (
	proseRefusalVerbs = `(?:help(?:ing)?|assist(?:ing)?|provid(?:e|ing)|creat(?:e|ing)|writ(?:e|ing)|generat(?:e|ing)|` +
		`produc(?:e|ing)|compl(?:y|ying)|complet(?:e|ing)|fulfill(?:ing)?|engag(?:e|ing)|support(?:ing)?|` +
		`continu(?:e|ing)|proceed(?:ing)?|do that|do this|go along with)`
	proseRefusalVerbsPT = `(?:ajudar|auxiliar|fornecer|criar|escrever|gerar|produzir|atender|colaborar|cumprir|` +
		`continuar|prosseguir|seguir|fazer isso|fazer esse|fazer essa)`
)

// normalizeRefusalFallback trims the configured aliases and drops incomplete
// entries, so a half-written entry disables that alias instead of claiming a
// request it cannot serve. Fallback entries are trimmed, and empty entries and
// repeats of an earlier model in the chain are dropped, so each model runs at
// most once per request.
func normalizeRefusalFallback(entries []refusalFallbackAlias) []refusalFallbackAlias {
	kept := make([]refusalFallbackAlias, 0, len(entries))
	for _, entry := range entries {
		entry.Alias = strings.TrimSpace(entry.Alias)
		entry.Primary = strings.TrimSpace(entry.Primary)
		chain := []string{entry.Primary}
		for _, model := range entry.Fallback {
			if model = strings.TrimSpace(model); model != "" && !slices.Contains(chain, model) {
				chain = append(chain, model)
			}
		}
		entry.Fallback = chain[1:]
		if entry.Alias == "" || entry.Primary == "" || len(entry.Fallback) == 0 || entry.Alias == pluginIdentifier {
			continue
		}
		kept = append(kept, entry)
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

// refusalAlias returns the configured alias named by model.
func refusalAlias(cfg pluginConfig, model string) (refusalFallbackAlias, bool) {
	for _, entry := range cfg.RefusalFallback {
		if entry.Alias == model {
			return entry, true
		}
	}
	return refusalFallbackAlias{}, false
}

// refusalChain returns the models a request tries, in order. A request whose
// trailing tool results are toolErrorBumpThreshold or more explicit failures
// (the auto-router's signal and exclusions) runs on the last fallback only;
// the streak resets as soon as a later tool result succeeds.
func refusalChain(req rpcExecutorRequest, alias refusalFallbackAlias, stream bool) (chain []string, escalated bool) {
	escalated = snippet.Extract(req.OriginalRequest).ToolErrorStreak >= toolErrorBumpThreshold
	stats.tagTrace(req.Metadata, traceTag{alias: alias.Alias, primary: stripThinkingSuffix(alias.Primary), escalated: escalated})
	if escalated {
		last := alias.Fallback[len(alias.Fallback)-1]
		logRefusalFallback(req, alias, alias.Primary, last, toolErrorEscalationReason, stream)
		return []string{last}, true
	}
	return append([]string{alias.Primary}, alias.Fallback...), false
}

func refusalRouterHeader(model string, failed []string, escalated bool) string {
	if escalated {
		return model + ";" + toolErrorEscalationReason
	}
	if len(failed) == 0 {
		return model + ";" + refusalGuardReason
	}
	raw, _ := json.Marshal(failed)
	return model + ";" + refusalFallbackReason + ";failed_from=" + string(raw)
}

// logRefusalFallback logs one switch from the model that failed to the model
// that runs next. Bodies are never logged.
func logRefusalFallback(req rpcExecutorRequest, alias refusalFallbackAlias, from, to, reason string, stream bool) {
	payload, _ := json.Marshal(map[string]any{
		"alias":    alias.Alias,
		"primary":  alias.Primary,
		"fallback": to,
		"from":     from,
		"reason":   reason,
		"stream":   stream,
	})
	hostLog(req.HostCallbackID, "info", string(payload), nil)
	stats.recordSwitch(time.Now(), alias.Alias, from, to, reason)
}

// errorSwitchReason is the switch reason for an error from a model that is not
// the last in the chain: an error carrying a refusal marker always switches,
// any other error switches only from a fallback. "" returns the error as-is.
func errorSwitchReason(err error, fromFallback bool) string {
	if refusalErrorCue.MatchString(err.Error()) {
		return refusalErrorReason
	}
	if fromFallback {
		return fallbackErrorReason
	}
	return ""
}

// executeRefusal runs a non-streaming alias request down its chain with the
// untouched body. A model that is not the last hands over on a refusal (2xx
// body, refusal-marked error, or confirmed prose), an empty answer, or, for a
// fallback, any error. The primary's other errors and the last model's
// outcome are returned as-is.
func executeRefusal(req rpcExecutorRequest, alias refusalFallbackAlias) ([]byte, error) {
	chain, escalated := refusalChain(req, alias, false)
	format := requestFormat(req.SourceFormat)
	var failed []string
	var response pluginapi.HostModelExecutionResponse
	for i, model := range chain {
		var err error
		response, err = executeRefusalModel(req, model)
		reason := ""
		if i < len(chain)-1 {
			if err != nil {
				reason = errorSwitchReason(err, i > 0)
			} else if refusedResponse(format, response.Body) {
				reason = refusalFallbackReason
			} else if emptyAnswer(format, response.Body) {
				reason = emptyAnswerReason
			} else if alias.ProseRefusal && proseRefused(req, alias, model, chain[i+1], format, response.Body) {
				reason = refusalProseReason
			}
		}
		if reason == "" {
			if err != nil {
				return errorEnvelope("executor_error", err.Error()), nil
			}
			break
		}
		logRefusalFallback(req, alias, model, chain[i+1], reason, false)
		failed = append(failed, model)
	}
	headers := cloneHeaders(response.Headers)
	if headers == nil {
		headers = make(http.Header)
	}
	if headers.Get("Content-Type") == "" {
		headers.Set("Content-Type", contentTypeFor(req.SourceFormat, false))
	}
	headers.Set("X-Auto-Router", refusalRouterHeader(chain[len(failed)], failed, escalated))
	return okEnvelope(pluginapi.ExecutorResponse{Payload: response.Body, Headers: headers})
}

// proseRefused reports a Jev-confirmed prose refusal. Jev is asked only when
// the visible answer carries refusal phrasing, and any Jev failure keeps the
// answer. Jev only picks which model answers the unchanged request.
func proseRefused(req rpcExecutorRequest, alias refusalFallbackAlias, model, next, format string, body []byte) bool {
	text, _ := visibleAnswerText(format, body)
	if !proseRefusalCue.MatchString(strings.ReplaceAll(text, "’", "'")) {
		return false
	}
	cfg := loadedConfig()
	probability, err := jev.Refusal(context.Background(), jevConfig(cfg), snippet.Prepare(text, cfg.SnippetChars))
	if err != nil {
		logRefusalFallback(req, alias, model, next, proseRefusalUnconfirmedLog, false)
		return false
	}
	return probability >= proseRefusalThreshold
}

// emptyAnswer reports a 2xx answer with no visible text and no tool call. An
// entry protocol the detectors do not read never counts as empty.
func emptyAnswer(format string, body []byte) bool {
	text, acted := visibleAnswerText(format, body)
	return format != "" && !acted && strings.TrimSpace(text) == ""
}

// visibleAnswerText returns the assistant answer text of a non-streaming body
// in the host's exit format: message.content strings for chat-completions, and
// output_text parts of message items for responses (see the translator shapes
// below). Reasoning and tool arguments are never returned. A response with a
// tool call yields "" and acted: the model acted, and a switch would drop that
// call.
func visibleAnswerText(format string, body []byte) (text string, acted bool) {
	root := gjson.ParseBytes(body)
	var parts []string
	switch format {
	case formatChatCompletions:
		for _, choice := range root.Get("choices").Array() {
			message := choice.Get("message")
			if len(message.Get("tool_calls").Array()) > 0 {
				return "", true
			}
			if content := message.Get("content"); content.Type == gjson.String {
				parts = append(parts, content.String())
			}
		}
	case formatResponses:
		for _, item := range root.Get("output").Array() {
			switch item.Get("type").String() {
			case "reasoning":
			case "message":
				for _, part := range item.Get("content").Array() {
					if part.Get("type").String() == "output_text" {
						parts = append(parts, part.Get("text").String())
					}
				}
			default:
				// function_call, custom_tool_call, web_search_call and the other tool items.
				return "", true
			}
		}
	}
	return strings.Join(parts, "\n"), false
}

func executeRefusalModel(req rpcExecutorRequest, model string) (pluginapi.HostModelExecutionResponse, error) {
	responseRaw, err := hostCall(pluginabi.MethodHostModelExecute, hostModelExecutionRequest{
		HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{
			EntryProtocol: req.SourceFormat,
			ExitProtocol:  req.SourceFormat,
			Model:         model,
			Stream:        false,
			Body:          req.OriginalRequest,
			Headers:       req.Headers,
			Query:         req.Query,
			Alt:           req.Alt,
		},
		HostCallbackID: req.HostCallbackID,
	})
	if err != nil {
		return pluginapi.HostModelExecutionResponse{}, err
	}
	var response pluginapi.HostModelExecutionResponse
	if err := json.Unmarshal(responseRaw, &response); err != nil {
		return pluginapi.HostModelExecutionResponse{}, errors.New("invalid host model response")
	}
	if response.StatusCode >= http.StatusBadRequest {
		return response, fmt.Errorf("host model status %d: %s", response.StatusCode, string(response.Body))
	}
	return response, nil
}

// refusalGuardedStream is the state handed to the stream pump once the guard
// knows which model the client is going to hear. next is the model a switch
// would have used, empty when the answering model is the last in the chain.
type refusalGuardedStream struct {
	ready     hostStreamReady
	buffered  [][]byte
	model     string
	next      string
	failed    []string
	escalated bool
}

// executeRefusalStream serves a streaming alias request. The buffering phase
// runs synchronously so the response headers name the model that answers.
func executeRefusalStream(req rpcExecutorRequest, alias refusalFallbackAlias) ([]byte, error) {
	pluginStreamID := strings.TrimSpace(req.StreamID)
	if pluginStreamID == "" {
		return errorEnvelope("executor_error", "stream_id is required for executor.execute_stream"), nil
	}
	if !beginPluginStream() {
		return errorEnvelope("executor_error", "plugin is shutting down"), nil
	}
	start, err := startRefusalGuardedStream(req, alias)
	if err != nil {
		closePluginStream(pluginStreamID, err.Error())
		endPluginStream()
		return errorEnvelope("executor_error", err.Error()), nil
	}
	go func(start refusalGuardedStream) {
		defer endPluginStream()
		if err := pumpRefusalGuardedStream(req, alias, start, pluginStreamID); err != nil {
			closePluginStream(pluginStreamID, err.Error())
			return
		}
		closePluginStream(pluginStreamID, "")
	}(start)
	headers := http.Header{
		"Content-Type":  []string{contentTypeFor(req.SourceFormat, true)},
		"X-Auto-Router": []string{refusalRouterHeader(start.model, start.failed, start.escalated)},
	}
	return okEnvelope(map[string]any{"headers": headers})
}

// startRefusalGuardedStream walks the chain: every model except the last is
// guarded until its first visible output, and the last model's stream (or open
// error) is returned as-is.
func startRefusalGuardedStream(req rpcExecutorRequest, alias refusalFallbackAlias) (refusalGuardedStream, error) {
	chain, escalated := refusalChain(req, alias, true)
	var failed []string
	for i, model := range chain[:len(chain)-1] {
		start, reason, err := guardRefusalStream(req, model, i > 0)
		if reason == "" {
			if err != nil {
				return refusalGuardedStream{}, err
			}
			start.next, start.failed = chain[i+1], failed
			return start, nil
		}
		logRefusalFallback(req, alias, model, chain[i+1], reason, true)
		failed = append(failed, model)
	}
	last := chain[len(chain)-1]
	ready, err := openHostStream(context.Background(), req, last, req.OriginalRequest)
	if err != nil {
		return refusalGuardedStream{}, err
	}
	return refusalGuardedStream{ready: ready, model: last, failed: failed, escalated: escalated}, nil
}

// guardRefusalStream opens model's stream and buffers its chunks until the
// first visible answer output (flush and stream through). A non-empty reason
// hands the request to the next model: a refusal or an end of stream with
// nothing visible yet, or an error errorSwitchReason accepts. Reasoning deltas
// are buffered, not output: a model with reasoning enabled streams thinking
// before it refuses, and that thinking is dropped on a switch.
func guardRefusalStream(req rpcExecutorRequest, model string, fromFallback bool) (refusalGuardedStream, string, error) {
	ready, err := openHostStream(context.Background(), req, model, req.OriginalRequest)
	if err != nil {
		return refusalGuardedStream{}, errorSwitchReason(err, fromFallback), err
	}
	format := requestFormat(req.SourceFormat)
	payload, done := ready.firstPayload, ready.done
	ready.firstPayload = nil
	buffered := make([][]byte, 0, 8)
	bufferedBytes := 0
	for {
		if len(payload) > 0 {
			buffered = append(buffered, payload)
			bufferedBytes += len(payload)
			if streamChunkHasVisibleOutput(format, payload) {
				break
			}
			if streamChunkRefused(format, payload) {
				closeRegisteredPluginHostStream(ready.streamID)
				return refusalGuardedStream{}, refusalFallbackReason, nil
			}
		}
		if done {
			if format != "" {
				closeRegisteredPluginHostStream(ready.streamID)
				return refusalGuardedStream{}, emptyAnswerReason, nil
			}
			break
		}
		if bufferedBytes >= refusalGuardMaxBytes {
			break
		}
		payload, done, err = readHostStreamChunk(ready.streamID)
		if err != nil {
			closeRegisteredPluginHostStream(ready.streamID)
			return refusalGuardedStream{}, errorSwitchReason(err, fromFallback), err
		}
	}
	ready.done = done
	return refusalGuardedStream{ready: ready, buffered: buffered, model: model}, "", nil
}

func pumpRefusalGuardedStream(req rpcExecutorRequest, alias refusalFallbackAlias, start refusalGuardedStream, pluginStreamID string) error {
	var observe func([]byte)
	format := requestFormat(req.SourceFormat)
	if start.next != "" {
		logged := false
		observe = func(payload []byte) {
			// The stream is already reaching the client, so a refusal that
			// arrives now cannot be un-sent: report it, never switch.
			if logged || !streamChunkRefused(format, payload) {
				return
			}
			logged = true
			logRefusalFallback(req, alias, start.model, start.next, refusalAfterOutputLog, true)
		}
	}
	for _, chunk := range start.buffered {
		if err := emitPluginStreamChunk(pluginStreamID, chunk); err != nil {
			closeRegisteredPluginHostStream(start.ready.streamID)
			return err
		}
		if observe != nil {
			observe(chunk)
		}
	}
	return continueHostStream(context.Background(), start.ready, pluginStreamID, observe)
}

// Refusal shapes match what the CLIProxyAPI translators emit:
//   - chat-completions: Anthropic stop_reason "refusal" or "sensitive" becomes
//     finish_reason "content_filter"
//     (internal/translator/claude/openai/chat-completions/claude_openai_response.go).
//   - responses: a filtered turn becomes status "incomplete" with
//     incomplete_details.reason "content_filter", streamed as the
//     "response.incomplete" event
//     (internal/translator/openai/openai/responses/openai_openai-responses_response.go).
//
// No translator emits a responses "refusal" output part, so detection keys on
// the finish reason and the incomplete details only.

// refusedResponse reports whether a non-streaming response body is a refusal.
func refusedResponse(format string, body []byte) bool {
	root := gjson.ParseBytes(body)
	switch format {
	case formatResponses:
		return root.Get("status").String() == "incomplete" &&
			root.Get("incomplete_details.reason").String() == refusalContentFilter
	case formatChatCompletions:
		return chatFinishedByContentFilter(root)
	}
	return false
}

// streamChunkRefused reports whether a stream chunk carries a terminal refusal.
func streamChunkRefused(format string, payload []byte) bool {
	for _, data := range streamChunkEvents(payload) {
		event := gjson.ParseBytes(data)
		switch format {
		case formatResponses:
			if event.Get("type").String() == responsesIncompleteType &&
				event.Get("response.incomplete_details.reason").String() == refusalContentFilter {
				return true
			}
		case formatChatCompletions:
			if chatFinishedByContentFilter(event) {
				return true
			}
		}
	}
	return false
}

// streamChunkHasVisibleOutput reports whether a stream chunk carries assistant
// output the client can see: answer text or a tool call. Role-only deltas,
// empty deltas and reasoning deltas do not count, so the guard can still switch
// models after a reasoning prelude.
//
// The responses translators emit exactly three delta kinds: visible text as
// "response.output_text.delta", tool arguments as
// "response.function_call_arguments.delta", and thinking as
// "response.reasoning_summary_text.delta"
// (internal/translator/claude/openai/responses/claude_openai-responses_response.go
// and internal/translator/openai/openai/responses/openai_openai-responses_response.go).
func streamChunkHasVisibleOutput(format string, payload []byte) bool {
	for _, data := range streamChunkEvents(payload) {
		event := gjson.ParseBytes(data)
		switch format {
		case formatResponses:
			eventType := event.Get("type").String()
			if eventType != "response.output_text.delta" && eventType != "response.function_call_arguments.delta" {
				continue
			}
			if event.Get("delta").String() != "" {
				return true
			}
		case formatChatCompletions:
			for _, choice := range event.Get("choices").Array() {
				delta := choice.Get("delta")
				if delta.Get("content").String() != "" || len(delta.Get("tool_calls").Array()) > 0 {
					return true
				}
			}
		}
	}
	return false
}

// streamChunkEvents yields the JSON documents carried by one stream chunk. The
// host passes provider SSE bytes through, so a chunk may hold several "data:"
// lines; "data: [DONE]" and comment lines carry no JSON and are skipped. Some
// upstream routes arrive double-framed as "data: data: {...}", so the prefix is
// stripped until the JSON starts.
func streamChunkEvents(payload []byte) [][]byte {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 {
		return nil
	}
	if trimmed[0] == '{' {
		return [][]byte{trimmed}
	}
	var events [][]byte
	for _, line := range bytes.Split(trimmed, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(line[len("data:"):])
		for bytes.HasPrefix(data, []byte("data:")) {
			data = bytes.TrimSpace(data[len("data:"):])
		}
		if len(data) == 0 || data[0] != '{' {
			continue
		}
		events = append(events, data)
	}
	return events
}

func chatFinishedByContentFilter(event gjson.Result) bool {
	for _, choice := range event.Get("choices").Array() {
		if choice.Get("finish_reason").String() == refusalContentFilter {
			return true
		}
	}
	return false
}
