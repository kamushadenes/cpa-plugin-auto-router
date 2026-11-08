package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/kamushadenes/cpa-plugin-auto-router/internal/jev"
	"github.com/kamushadenes/cpa-plugin-auto-router/internal/snippet"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

// refusalFallbackAlias is one configured alias: requests for Alias run on
// Primary, and are re-run on Fallback only when Primary refuses on safety
// grounds. Primary and Fallback reach the host verbatim, so a thinking suffix
// such as "claude-opus-5-5(high)" belongs in the configured value.
// ProseRefusal also treats a Jev-confirmed prose refusal as a refusal, on the
// non-streaming path only.
type refusalFallbackAlias struct {
	Alias        string `yaml:"alias"`
	Primary      string `yaml:"primary"`
	Fallback     string `yaml:"fallback"`
	ProseRefusal bool   `yaml:"prose_refusal"`
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

// refusalProseReason logs a switch on a Jev-confirmed prose refusal;
// proseRefusalUnconfirmedLog logs a prose candidate kept because Jev failed.
const (
	refusalProseReason         = "prose-refusal"
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

const (
	proseRefusalVerbs = `(?:help(?:ing)?|assist(?:ing)?|provid(?:e|ing)|creat(?:e|ing)|writ(?:e|ing)|generat(?:e|ing)|` +
		`produc(?:e|ing)|compl(?:y|ying)|complet(?:e|ing)|fulfill(?:ing)?|engag(?:e|ing)|support(?:ing)?|` +
		`continu(?:e|ing)|proceed(?:ing)?|do that|do this|go along with)`
	proseRefusalVerbsPT = `(?:ajudar|auxiliar|fornecer|criar|escrever|gerar|produzir|atender|colaborar|cumprir|` +
		`continuar|prosseguir|seguir|fazer isso|fazer esse|fazer essa)`
)

// normalizeRefusalFallback trims the configured aliases and drops incomplete
// entries, so a half-written entry disables that alias instead of claiming a
// request it cannot serve.
func normalizeRefusalFallback(entries []refusalFallbackAlias) []refusalFallbackAlias {
	kept := make([]refusalFallbackAlias, 0, len(entries))
	for _, entry := range entries {
		entry.Alias = strings.TrimSpace(entry.Alias)
		entry.Primary = strings.TrimSpace(entry.Primary)
		entry.Fallback = strings.TrimSpace(entry.Fallback)
		if entry.Alias == "" || entry.Primary == "" || entry.Fallback == "" || entry.Alias == pluginIdentifier {
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

func refusalRouterHeader(model string, failed []string) string {
	if len(failed) == 0 {
		return model + ";" + refusalGuardReason
	}
	raw, _ := json.Marshal(failed)
	return model + ";" + refusalFallbackReason + ";failed_from=" + string(raw)
}

func logRefusalFallback(req rpcExecutorRequest, alias refusalFallbackAlias, reason string, stream bool) {
	payload, _ := json.Marshal(map[string]any{
		"alias":    alias.Alias,
		"primary":  alias.Primary,
		"fallback": alias.Fallback,
		"reason":   reason,
		"stream":   stream,
	})
	hostLog(req.HostCallbackID, "info", string(payload), nil)
}

// executeRefusal runs a non-streaming alias request on the primary model and
// re-runs the untouched body on the fallback model when the primary refuses.
// Every other outcome, host errors included, is returned as-is.
func executeRefusal(req rpcExecutorRequest, alias refusalFallbackAlias) ([]byte, error) {
	response, err := executeRefusalModel(req, alias.Primary)
	if err != nil {
		return errorEnvelope("executor_error", err.Error()), nil
	}
	model := alias.Primary
	var failed []string
	reason := ""
	format := requestFormat(req.SourceFormat)
	if refusedResponse(format, response.Body) {
		reason = refusalFallbackReason
	} else if alias.ProseRefusal && proseRefused(req, alias, format, response.Body) {
		reason = refusalProseReason
	}
	if reason != "" {
		fallback, fallbackErr := executeRefusalModel(req, alias.Fallback)
		if fallbackErr != nil {
			return errorEnvelope("executor_error", fallbackErr.Error()), nil
		}
		response = fallback
		model = alias.Fallback
		failed = []string{alias.Primary}
		logRefusalFallback(req, alias, reason, false)
	}
	headers := cloneHeaders(response.Headers)
	if headers == nil {
		headers = make(http.Header)
	}
	if headers.Get("Content-Type") == "" {
		headers.Set("Content-Type", contentTypeFor(req.SourceFormat, false))
	}
	headers.Set("X-Auto-Router", refusalRouterHeader(model, failed))
	return okEnvelope(pluginapi.ExecutorResponse{Payload: response.Body, Headers: headers})
}

// proseRefused reports a Jev-confirmed prose refusal. Jev is asked only when
// the visible answer carries refusal phrasing, and any Jev failure keeps the
// primary answer. Jev only picks which model answers the unchanged request.
func proseRefused(req rpcExecutorRequest, alias refusalFallbackAlias, format string, body []byte) bool {
	text := visibleAnswerText(format, body)
	if !proseRefusalCue.MatchString(strings.ReplaceAll(text, "’", "'")) {
		return false
	}
	cfg := loadedConfig()
	probability, err := jev.Refusal(context.Background(), jevConfig(cfg), snippet.Prepare(text, cfg.SnippetChars))
	if err != nil {
		logRefusalFallback(req, alias, proseRefusalUnconfirmedLog, false)
		return false
	}
	return probability >= proseRefusalThreshold
}

// visibleAnswerText returns the assistant answer text of a non-streaming body
// in the host's exit format: message.content strings for chat-completions, and
// output_text parts of message items for responses (see the translator shapes
// below). Reasoning and tool arguments are never returned. A response with a
// tool call yields "": the primary acted, and a switch would drop that call.
func visibleAnswerText(format string, body []byte) string {
	root := gjson.ParseBytes(body)
	var parts []string
	switch format {
	case formatChatCompletions:
		for _, choice := range root.Get("choices").Array() {
			message := choice.Get("message")
			if len(message.Get("tool_calls").Array()) > 0 {
				return ""
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
				return ""
			}
		}
	}
	return strings.Join(parts, "\n")
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
// knows which model the client is going to hear.
type refusalGuardedStream struct {
	ready    hostStreamReady
	buffered [][]byte
	model    string
	failed   []string
	switched bool
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
	if start.switched {
		logRefusalFallback(req, alias, refusalFallbackReason, true)
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
		"X-Auto-Router": []string{refusalRouterHeader(start.model, start.failed)},
	}
	return okEnvelope(map[string]any{"headers": headers})
}

// startRefusalGuardedStream opens the primary stream and buffers its chunks
// until the first visible answer output (flush and stream through) or a refusal
// with nothing emitted yet (drop the buffer and stream the fallback instead).
// Reasoning deltas are buffered, not output: a model with reasoning enabled
// streams thinking before it refuses, and that thinking is dropped on a switch.
func startRefusalGuardedStream(req rpcExecutorRequest, alias refusalFallbackAlias) (refusalGuardedStream, error) {
	ready, err := openHostStream(context.Background(), req, alias.Primary, req.OriginalRequest)
	if err != nil {
		return refusalGuardedStream{}, err
	}
	format := requestFormat(req.SourceFormat)
	payload, done := ready.firstPayload, ready.done
	ready.firstPayload = nil
	buffered := make([][]byte, 0, 8)
	bufferedBytes := 0
	refused := false
	for {
		if len(payload) > 0 {
			buffered = append(buffered, payload)
			bufferedBytes += len(payload)
			if streamChunkHasVisibleOutput(format, payload) {
				break
			}
			if streamChunkRefused(format, payload) {
				refused = true
				break
			}
		}
		if done || bufferedBytes >= refusalGuardMaxBytes {
			break
		}
		payload, done, err = readHostStreamChunk(ready.streamID)
		if err != nil {
			closeRegisteredPluginHostStream(ready.streamID)
			return refusalGuardedStream{}, err
		}
	}
	ready.done = done
	if !refused {
		return refusalGuardedStream{ready: ready, buffered: buffered, model: alias.Primary}, nil
	}
	closeRegisteredPluginHostStream(ready.streamID)
	fallbackReady, err := openHostStream(context.Background(), req, alias.Fallback, req.OriginalRequest)
	if err != nil {
		return refusalGuardedStream{}, err
	}
	return refusalGuardedStream{
		ready:    fallbackReady,
		model:    alias.Fallback,
		failed:   []string{alias.Primary},
		switched: true,
	}, nil
}

func pumpRefusalGuardedStream(req rpcExecutorRequest, alias refusalFallbackAlias, start refusalGuardedStream, pluginStreamID string) error {
	var observe func([]byte)
	format := requestFormat(req.SourceFormat)
	if !start.switched {
		logged := false
		observe = func(payload []byte) {
			// The primary stream is already reaching the client, so a refusal
			// that arrives now cannot be un-sent: report it, never switch.
			if logged || !streamChunkRefused(format, payload) {
				return
			}
			logged = true
			logRefusalFallback(req, alias, refusalAfterOutputLog, true)
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
