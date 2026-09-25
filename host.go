package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/chloeassistant/cpa-plugin-auto-router/internal/decide"
	"github.com/chloeassistant/cpa-plugin-auto-router/internal/session"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type hostModelExecutionRequest struct {
	pluginapi.HostModelExecutionRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type pluginStreamLifecycle struct {
	mu       sync.Mutex
	active   int
	closing  bool
	done     chan struct{}
	doneOnce bool
}

func newPluginStreamLifecycle() *pluginStreamLifecycle {
	return &pluginStreamLifecycle{done: make(chan struct{})}
}

var (
	streamLifecycle = newPluginStreamLifecycle()
	hostCall        = callHost
)

func beginPluginStream() bool {
	streamLifecycle.mu.Lock()
	defer streamLifecycle.mu.Unlock()
	if streamLifecycle.closing {
		return false
	}
	streamLifecycle.active++
	return true
}

func endPluginStream() {
	streamLifecycle.mu.Lock()
	defer streamLifecycle.mu.Unlock()
	if streamLifecycle.active > 0 {
		streamLifecycle.active--
	}
	if streamLifecycle.closing && streamLifecycle.active == 0 && !streamLifecycle.doneOnce {
		close(streamLifecycle.done)
		streamLifecycle.doneOnce = true
	}
}

func beginPluginShutdown() <-chan struct{} {
	streamLifecycle.mu.Lock()
	defer streamLifecycle.mu.Unlock()
	streamLifecycle.closing = true
	if streamLifecycle.active == 0 && !streamLifecycle.doneOnce {
		close(streamLifecycle.done)
		streamLifecycle.doneOnce = true
	}
	return streamLifecycle.done
}

func waitPluginShutdown(done <-chan struct{}) {
	<-done
}

func execute(raw []byte) ([]byte, error) {
	var req rpcExecutorRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	decision, routeCtx, err := decisionForExecutorWithContext(req)
	if err != nil {
		return errorEnvelope("executor_error", err.Error()), nil
	}
	failed := make([]string, 0, maxHostAttempts-1)
	failedSet := make(map[string]bool, maxHostAttempts-1)
	var lastErr error
	for attempt := range maxHostAttempts {
		model := routedModel(decision)
		responseRaw, callErr := hostCall(pluginabi.MethodHostModelExecute, hostModelExecutionRequest{
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
		if callErr != nil {
			lastErr = callErr
			if !retryableHostFailure(0, callErr) || attempt+1 == maxHostAttempts {
				return errorEnvelope("executor_error", callErr.Error()), nil
			}
		} else {
			var hostResponse pluginapi.HostModelExecutionResponse
			if err := json.Unmarshal(responseRaw, &hostResponse); err != nil {
				return errorEnvelope("executor_error", "invalid host model response"), nil
			}
			if hostResponse.StatusCode < http.StatusBadRequest {
				persistEffectiveSession(req, decision)
				headers := cloneHeaders(hostResponse.Headers)
				if headers == nil {
					headers = make(http.Header)
				}
				if headers.Get("Content-Type") == "" {
					headers.Set("Content-Type", contentTypeFor(req.SourceFormat, false))
				}
				headers.Set("X-Auto-Router", effectiveRouterHeader(model, decision, failed))
				if len(failed) > 0 {
					logFailover(req, decision, routeCtx, failed)
				}
				return okEnvelope(pluginapi.ExecutorResponse{Payload: hostResponse.Body, Headers: headers})
			}
			lastErr = fmt.Errorf("host model status %d: %s", hostResponse.StatusCode, string(hostResponse.Body))
			if !retryableHostFailure(hostResponse.StatusCode, lastErr) || attempt+1 == maxHostAttempts {
				return errorEnvelope("executor_error", lastErr.Error()), nil
			}
		}

		failed = append(failed, decision.Model)
		failedSet[decision.Model] = true
		next, nextErr := nextFailoverDecision(decision, routeCtx, failedSet)
		if nextErr != nil {
			lastErr = nextErr
			break
		}
		decision = next
	}
	if lastErr == nil {
		lastErr = errors.New("host model execution failed")
	}
	return errorEnvelope("executor_error", lastErr.Error()), nil
}

func executeStream(raw []byte) ([]byte, error) {
	var req rpcExecutorRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	pluginStreamID := strings.TrimSpace(req.StreamID)
	if pluginStreamID == "" {
		return errorEnvelope("executor_error", "stream_id is required for executor.execute_stream"), nil
	}
	if !beginPluginStream() {
		return errorEnvelope("executor_error", "plugin is shutting down"), nil
	}
	decision, routeCtx, err := decisionForExecutorWithContext(req)
	if err != nil {
		endPluginStream()
		return errorEnvelope("executor_error", err.Error()), nil
	}
	failed := make([]string, 0, maxHostAttempts-1)
	failedSet := make(map[string]bool, maxHostAttempts-1)
	var ready hostStreamReady
	var lastErr error
	for attempt := range maxHostAttempts {
		ready, lastErr = openHostStream(context.Background(), req, routedModel(decision))
		if lastErr == nil {
			break
		}
		if !retryableHostFailure(0, lastErr) || attempt+1 == maxHostAttempts {
			closePluginStream(pluginStreamID, lastErr.Error())
			endPluginStream()
			return errorEnvelope("executor_error", lastErr.Error()), nil
		}
		failed = append(failed, decision.Model)
		failedSet[decision.Model] = true
		next, nextErr := nextFailoverDecision(decision, routeCtx, failedSet)
		if nextErr != nil {
			closePluginStream(pluginStreamID, nextErr.Error())
			endPluginStream()
			return errorEnvelope("executor_error", nextErr.Error()), nil
		}
		decision = next
	}
	if lastErr != nil {
		closePluginStream(pluginStreamID, lastErr.Error())
		endPluginStream()
		return errorEnvelope("executor_error", lastErr.Error()), nil
	}
	persistEffectiveSession(req, decision)
	model := routedModel(decision)
	if len(failed) > 0 {
		logFailover(req, decision, routeCtx, failed)
	}
	go func(ready hostStreamReady) {
		defer endPluginStream()
		if err := continueHostStream(context.Background(), ready, pluginStreamID); err != nil {
			closePluginStream(pluginStreamID, err.Error())
			return
		}
		closePluginStream(pluginStreamID, "")
	}(ready)
	headers := http.Header{
		"Content-Type":  []string{contentTypeFor(req.SourceFormat, true)},
		"X-Auto-Router": []string{effectiveRouterHeader(model, decision, failed)},
	}
	return okEnvelope(map[string]any{"headers": headers})
}

func decisionForExecutor(req rpcExecutorRequest) (decide.Decision, error) {
	decision, _, err := decisionForExecutorWithContext(req)
	return decision, err
}

func decisionForExecutorWithContext(req rpcExecutorRequest) (decide.Decision, routeContext, error) {
	key := requestKey(req.Headers, req.OriginalRequest, req.Metadata)
	if key != "" {
		if value, ok := pending.LoadAndDelete(key); ok {
			if route, ok := value.(pendingRoute); ok {
				return route.decision, route.context, nil
			}
		}
	}
	sid := session.ID(req.Headers, req.OriginalRequest)
	prev, hasPrev := store.Get(sid)
	request := pluginapi.ModelRouteRequest{
		SourceFormat:   req.SourceFormat,
		RequestedModel: req.Model,
		Headers:        req.Headers,
		Body:           req.OriginalRequest,
		Metadata:       req.Metadata,
	}
	decision, _, routeCtx, err := decideForWithContext(request, prev, hasPrev)
	if err != nil {
		return decide.Decision{}, routeContext{}, err
	}
	if sid != "" {
		store.Put(sid, decision.State)
	}
	return decision, routeCtx, nil
}

func persistEffectiveSession(req rpcExecutorRequest, decision decide.Decision) {
	if sid := session.ID(req.Headers, req.OriginalRequest); sid != "" {
		store.Put(sid, decision.State)
	}
}

func routedModel(decision decide.Decision) string {
	model := decision.Model
	if decision.Thinking != "" {
		model += "(" + decision.Thinking + ")"
	}
	return model
}

// ponytail: three host attempts, no retry framework until a real policy needs one.
const maxHostAttempts = 3

type hostStreamReady struct {
	streamID     string
	firstPayload []byte
	done         bool
}

func openHostStream(ctx context.Context, req rpcExecutorRequest, model string) (hostStreamReady, error) {
	responseRaw, err := hostCall(pluginabi.MethodHostModelExecuteStream, hostModelExecutionRequest{
		HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{
			EntryProtocol: req.SourceFormat,
			ExitProtocol:  req.SourceFormat,
			Model:         model,
			Stream:        true,
			Body:          req.OriginalRequest,
			Headers:       req.Headers,
			Query:         req.Query,
			Alt:           req.Alt,
		},
		HostCallbackID: req.HostCallbackID,
	})
	if err != nil {
		return hostStreamReady{}, err
	}
	var response pluginapi.HostModelStreamResponse
	if err := json.Unmarshal(responseRaw, &response); err != nil {
		return hostStreamReady{}, err
	}
	if response.StatusCode >= http.StatusBadRequest {
		_ = closeHostModelStream(response.StreamID)
		return hostStreamReady{}, fmt.Errorf("host model status %d", response.StatusCode)
	}
	if strings.TrimSpace(response.StreamID) == "" {
		return hostStreamReady{}, errors.New("host model stream has no stream id")
	}
	for {
		chunkRaw, err := hostCall(pluginabi.MethodHostModelStreamRead, pluginapi.HostModelStreamReadRequest{StreamID: response.StreamID})
		if err != nil {
			_ = closeHostModelStream(response.StreamID)
			return hostStreamReady{}, err
		}
		var chunk pluginapi.HostModelStreamReadResponse
		if err := json.Unmarshal(chunkRaw, &chunk); err != nil {
			_ = closeHostModelStream(response.StreamID)
			return hostStreamReady{}, err
		}
		if chunk.Error != "" {
			_ = closeHostModelStream(response.StreamID)
			return hostStreamReady{}, errors.New(chunk.Error)
		}
		if len(chunk.Payload) > 0 {
			return hostStreamReady{streamID: response.StreamID, firstPayload: append([]byte(nil), chunk.Payload...), done: chunk.Done}, nil
		}
		if chunk.Done {
			return hostStreamReady{streamID: response.StreamID, done: true}, nil
		}
	}
}

func continueHostStream(ctx context.Context, ready hostStreamReady, pluginStreamID string) error {
	defer func() { _ = closeHostModelStream(ready.streamID) }()
	if len(ready.firstPayload) > 0 {
		if err := emitPluginStreamChunk(pluginStreamID, ready.firstPayload); err != nil {
			return err
		}
	}
	if ready.done {
		return nil
	}
	for {
		chunkRaw, err := hostCall(pluginabi.MethodHostModelStreamRead, pluginapi.HostModelStreamReadRequest{StreamID: ready.streamID})
		if err != nil {
			return err
		}
		var chunk pluginapi.HostModelStreamReadResponse
		if err := json.Unmarshal(chunkRaw, &chunk); err != nil {
			return err
		}
		if chunk.Error != "" {
			return errors.New(chunk.Error)
		}
		if len(chunk.Payload) > 0 {
			if err := emitPluginStreamChunk(pluginStreamID, chunk.Payload); err != nil {
				return err
			}
		}
		if chunk.Done {
			return nil
		}
	}
}

func nextFailoverDecision(current decide.Decision, routeCtx routeContext, failed map[string]bool) (decide.Decision, error) {
	tb, err := loadedTable()
	if err != nil {
		return decide.Decision{}, err
	}
	difficulty := current.State.Difficulty
	if routeCtx.difficulty != "" {
		difficulty = routeCtx.difficulty
	}
	next, err := decide.Next(decide.Input{
		Table:      tb,
		Category:   routeCtx.category,
		Difficulty: difficulty,
		HasImage:   routeCtx.hasImage,
		Available: func(model string) bool {
			return !failed[model]
		},
		Exclude: func(model string) bool {
			return failed[model] || excluded(model)
		},
	}, current.State, true)
	if err != nil {
		return decide.Decision{}, err
	}
	if next.Model == current.Model || failed[next.Model] {
		return decide.Decision{}, errors.New("no unfailed model available")
	}
	next.Reason = "failover"
	next.Choice.Reason = "failover"
	return next, nil
}

func effectiveRouterHeader(model string, decision decide.Decision, failed []string) string {
	if len(failed) == 0 {
		return model + ";" + decision.Reason
	}
	raw, _ := json.Marshal(failed)
	return model + ";failover;failed_from=" + string(raw)
}

func logFailover(req rpcExecutorRequest, decision decide.Decision, routeCtx routeContext, failed []string) {
	category, difficulty := routeCtx.category, routeCtx.difficulty
	if len(routeCtx.factors) > 0 {
		category = decide.Category(routeCtx.factors)
	}
	if len(routeCtx.effortP) > 0 {
		difficulty = decide.Difficulty(routeCtx.effortP)
	}
	fields := map[string]any{
		"session":               hashSession(session.ID(req.Headers, req.OriginalRequest)),
		"category":              category,
		"factors":               routeCtx.factors,
		"effort_p":              routeCtx.effortP,
		"effort_mean":           routeCtx.effortMean,
		"difficulty":            difficulty,
		"category_confidence":   routeCtx.categoryConfidence,
		"difficulty_confidence": routeCtx.difficultyConfidence,
		"confidence":            routeCtx.confidence,
		"tier":                  decision.Tier,
		"model":                 decision.Model,
		"thinking":              decision.Thinking,
		"reason":                "failover",
		"jev_ms":                routeCtx.jevMillis,
		"failed_from":           append([]string(nil), failed...),
	}
	payload, _ := json.Marshal(fields)
	hostLog(req.HostCallbackID, "info", string(payload), nil)
}

// ponytail: match transport errors by text on this host version; switch to numeric status if a future host exposes it.
func retryableHostFailure(status int, err error) bool {
	if status == 400 || status == 401 || status == 404 || status == 413 {
		return false
	}
	if status == 429 || status == 502 || status == 503 || status == 529 {
		return true
	}
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"authentication_error", "invalid_request_error"} {
		if strings.Contains(message, marker) {
			return false
		}
	}
	for _, marker := range []string{
		"rate_limit",
		"overloaded",
		"cooling down",
		"auth_unavailable",
		"status 429",
		"status 502",
		"status 503",
		"status 529",
		"stream closed before",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return strings.Contains(strings.Join(strings.Fields(message), ""), `"type":"api_error"`)
}

func emitPluginStreamChunk(streamID string, payload []byte) error {
	if strings.TrimSpace(streamID) == "" {
		return errors.New("plugin stream id is required")
	}
	_, err := hostCall(pluginabi.MethodHostStreamEmit, rpcStreamEmitRequest{StreamID: streamID, Payload: payload})
	return err
}

func closePluginStream(streamID, errMessage string) {
	if strings.TrimSpace(streamID) == "" {
		return
	}
	_, _ = hostCall(pluginabi.MethodHostStreamClose, rpcStreamCloseRequest{StreamID: streamID, Error: strings.TrimSpace(errMessage)})
}

func closeHostModelStream(streamID string) error {
	if strings.TrimSpace(streamID) == "" {
		return nil
	}
	_, err := hostCall(pluginabi.MethodHostModelStreamClose, pluginapi.HostModelStreamCloseRequest{StreamID: streamID})
	return err
}

func cloneHeaders(src http.Header) http.Header {
	if src == nil {
		return nil
	}
	dst := make(http.Header, len(src))
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
	return dst
}

func contentTypeFor(format string, stream bool) string {
	if stream {
		return "text/event-stream"
	}
	return "application/json"
}
