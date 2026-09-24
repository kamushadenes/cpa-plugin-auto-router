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

var streamLifecycle = newPluginStreamLifecycle()

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
	decision, err := decisionForExecutor(req)
	if err != nil {
		return errorEnvelope("executor_error", err.Error()), nil
	}
	model := routedModel(decision)
	responseRaw, err := callHost(pluginabi.MethodHostModelExecute, hostModelExecutionRequest{
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
		return errorEnvelope("executor_error", err.Error()), nil
	}
	var hostResponse pluginapi.HostModelExecutionResponse
	if err := json.Unmarshal(responseRaw, &hostResponse); err != nil {
		return errorEnvelope("executor_error", "invalid host model response"), nil
	}
	headers := cloneHeaders(hostResponse.Headers)
	if headers == nil {
		headers = make(http.Header)
	}
	if headers.Get("Content-Type") == "" {
		headers.Set("Content-Type", contentTypeFor(req.SourceFormat, false))
	}
	headers.Set("X-Auto-Router", model+";"+decision.Reason)
	return okEnvelope(pluginapi.ExecutorResponse{Payload: hostResponse.Body, Headers: headers})
}

func executeStream(raw []byte) ([]byte, error) {
	var req rpcExecutorRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	streamID := strings.TrimSpace(req.StreamID)
	if streamID == "" {
		return errorEnvelope("executor_error", "stream_id is required for executor.execute_stream"), nil
	}
	if !beginPluginStream() {
		return errorEnvelope("executor_error", "plugin is shutting down"), nil
	}
	decision, err := decisionForExecutor(req)
	if err != nil {
		endPluginStream()
		return errorEnvelope("executor_error", err.Error()), nil
	}
	model := routedModel(decision)
	go func() {
		defer endPluginStream()
		if err := forwardStream(context.Background(), req, streamID, model, decision); err != nil {
			closePluginStream(streamID, err.Error())
			return
		}
		closePluginStream(streamID, "")
	}()
	headers := http.Header{
		"Content-Type":  []string{contentTypeFor(req.SourceFormat, true)},
		"X-Auto-Router": []string{model + ";" + decision.Reason},
	}
	return okEnvelope(map[string]any{"headers": headers})
}

func decisionForExecutor(req rpcExecutorRequest) (decide.Decision, error) {
	key := requestKey(req.Headers, req.OriginalRequest, req.Metadata)
	if key != "" {
		if value, ok := pending.LoadAndDelete(key); ok {
			if decision, ok := value.(decide.Decision); ok {
				return decision, nil
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
	decision, err := decideFor(request, prev, hasPrev)
	if err != nil {
		return decide.Decision{}, err
	}
	if sid != "" {
		store.Put(sid, decision.State)
	}
	return decision, nil
}

func routedModel(decision decide.Decision) string {
	model := decision.Model
	if decision.Thinking != "" {
		model += "(" + decision.Thinking + ")"
	}
	return model
}

func forwardStream(ctx context.Context, req rpcExecutorRequest, pluginStreamID, model string, decision decide.Decision) error {
	responseRaw, err := callHost(pluginabi.MethodHostModelExecuteStream, hostModelExecutionRequest{
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
		return err
	}
	var response pluginapi.HostModelStreamResponse
	if err := json.Unmarshal(responseRaw, &response); err != nil {
		return err
	}
	if response.StatusCode >= http.StatusBadRequest {
		_ = closeHostModelStream(response.StreamID)
		return fmt.Errorf("host model status %d", response.StatusCode)
	}
	if strings.TrimSpace(response.StreamID) == "" {
		return errors.New("host model stream has no stream id")
	}
	defer func() { _ = closeHostModelStream(response.StreamID) }()
	for {
		chunkRaw, err := callHost(pluginabi.MethodHostModelStreamRead, pluginapi.HostModelStreamReadRequest{StreamID: response.StreamID})
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

func emitPluginStreamChunk(streamID string, payload []byte) error {
	if strings.TrimSpace(streamID) == "" {
		return errors.New("plugin stream id is required")
	}
	_, err := callHost(pluginabi.MethodHostStreamEmit, rpcStreamEmitRequest{StreamID: streamID, Payload: payload})
	return err
}

func closePluginStream(streamID, errMessage string) {
	if strings.TrimSpace(streamID) == "" {
		return
	}
	_, _ = callHost(pluginabi.MethodHostStreamClose, rpcStreamCloseRequest{StreamID: streamID, Error: strings.TrimSpace(errMessage)})
}

func closeHostModelStream(streamID string) error {
	if strings.TrimSpace(streamID) == "" {
		return nil
	}
	_, err := callHost(pluginabi.MethodHostModelStreamClose, pluginapi.HostModelStreamCloseRequest{StreamID: streamID})
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
