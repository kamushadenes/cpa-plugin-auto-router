package main

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const shutdownTestTimeout = time.Second

func resetPluginLifecycleForTest() {
	streamLifecycle = newPluginStreamLifecycle()
}

func TestPluginShutdownWaitsForActiveStreams(t *testing.T) {
	resetPluginLifecycleForTest()
	t.Cleanup(resetPluginLifecycleForTest)
	if !beginPluginStream() {
		t.Fatal("stream admission unexpectedly closed")
	}

	done := beginPluginShutdown()
	select {
	case <-done:
		t.Fatal("shutdown completed before the active stream closed")
	default:
	}
	if beginPluginStream() {
		endPluginStream()
		t.Fatal("shutdown admitted a new stream")
	}
	endPluginStream()
	select {
	case <-done:
	default:
		t.Fatal("shutdown did not finish after the active stream closed")
	}
}

func TestWaitPluginShutdownHasBoundedDrain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		done := make(chan struct{})
		finished := make(chan struct{})
		go func() {
			waitPluginShutdown(done)
			close(finished)
		}()
		time.Sleep(pluginShutdownDrainTimeout)
		synctest.Wait()
		select {
		case <-finished:
		default:
			t.Fatal("shutdown wait did not reach its deadline")
		}
	})
}

type shutdownExecuteResult struct {
	err error
}

func TestPluginShutdownClosesStalledHostStream(t *testing.T) {
	runPluginShutdownStreamTest(t, false)
}

func TestPluginShutdownClosesHostStreamRegisteredDuringShutdown(t *testing.T) {
	runPluginShutdownStreamTest(t, true)
}

func runPluginShutdownStreamTest(t *testing.T, late bool) {
	t.Helper()
	configureFailoverHostTest(t)
	resetPluginLifecycleForTest()
	t.Cleanup(resetPluginLifecycleForTest)

	streamOpened := make(chan struct{})
	streamReadBlocked := make(chan struct{})
	allowOpen := make(chan struct{})
	releaseRead := make(chan struct{})
	hostCloses := make(chan string, 4)
	pluginCloses := make(chan rpcStreamCloseRequest, 4)
	workerDone := make(chan struct{})
	var openOnce sync.Once
	var blockedOnce sync.Once
	var allowOpenOnce sync.Once
	var releaseReadOnce sync.Once
	var workerDoneOnce sync.Once
	var readMu sync.Mutex
	readCount := 0

	previousHostCall := hostCall
	hostCall = func(method string, payload any) (json.RawMessage, error) {
		switch method {
		case pluginabi.MethodHostModelExecuteStream:
			openOnce.Do(func() { close(streamOpened) })
			if late {
				<-allowOpen
			}
			return json.Marshal(pluginapi.HostModelStreamResponse{StatusCode: 200, StreamID: "host-stalled"})
		case pluginabi.MethodHostModelStreamRead:
			if _, ok := payload.(pluginapi.HostModelStreamReadRequest); !ok {
				return nil, fmt.Errorf("model stream read payload type %T", payload)
			}
			readMu.Lock()
			readCount++
			currentRead := readCount
			readMu.Unlock()
			if currentRead == 1 {
				return json.Marshal(pluginapi.HostModelStreamReadResponse{Payload: []byte("data: first\n\n")})
			}
			blockedOnce.Do(func() { close(streamReadBlocked) })
			<-releaseRead
			return json.Marshal(pluginapi.HostModelStreamReadResponse{Done: true})
		case pluginabi.MethodHostModelStreamClose:
			req, ok := payload.(pluginapi.HostModelStreamCloseRequest)
			if !ok {
				return nil, fmt.Errorf("model stream close payload type %T", payload)
			}
			hostCloses <- req.StreamID
			releaseReadOnce.Do(func() { close(releaseRead) })
			return json.RawMessage(`{}`), nil
		case pluginabi.MethodHostStreamEmit:
			return json.RawMessage(`{}`), nil
		case pluginabi.MethodHostStreamClose:
			req, ok := payload.(rpcStreamCloseRequest)
			if !ok {
				return nil, fmt.Errorf("plugin stream close payload type %T", payload)
			}
			pluginCloses <- req
			workerDoneOnce.Do(func() { close(workerDone) })
			return json.RawMessage(`{}`), nil
		case pluginabi.MethodHostLog:
			return json.RawMessage(`{}`), nil
		default:
			return nil, fmt.Errorf("unexpected host callback %s", method)
		}
	}

	raw := seedHostDecision(t, "shutdown-stream", "first", "plugin-stalled")
	executeDone := make(chan shutdownExecuteResult, 1)
	go func() {
		_, err := executeStream(raw)
		executeDone <- shutdownExecuteResult{err: err}
	}()
	executeFinished := false
	defer func() {
		allowOpenOnce.Do(func() { close(allowOpen) })
		releaseReadOnce.Do(func() { close(releaseRead) })
		if !executeFinished {
			select {
			case <-executeDone:
			case <-time.After(shutdownTestTimeout):
				t.Errorf("executeStream() did not drain during test cleanup")
			}
		}
		select {
		case <-workerDone:
		case <-time.After(shutdownTestTimeout):
			t.Errorf("stream worker did not close during test cleanup")
		}
		hostCall = previousHostCall
	}()
	waitExecute := func() {
		select {
		case result := <-executeDone:
			executeFinished = true
			if result.err != nil {
				t.Fatalf("executeStream() error = %v", result.err)
			}
		case <-time.After(shutdownTestTimeout):
			t.Fatal("executeStream() did not return")
		}
	}

	select {
	case <-streamOpened:
	case <-time.After(shutdownTestTimeout):
		t.Fatal("stream worker did not open the host stream")
	}
	if late {
		shutdownDone := beginPluginShutdown()
		allowOpenOnce.Do(func() { close(allowOpen) })
		assertShutdownStreamClosed(t, hostCloses, pluginCloses, shutdownDone)
		waitExecute()
		return
	}
	select {
	case <-streamReadBlocked:
	case <-time.After(shutdownTestTimeout):
		t.Fatal("stream worker did not block in host.model.stream_read")
	}
	shutdownDone := beginPluginShutdown()
	assertShutdownStreamClosed(t, hostCloses, pluginCloses, shutdownDone)
	waitExecute()
}

func assertShutdownStreamClosed(t *testing.T, hostCloses <-chan string, pluginCloses <-chan rpcStreamCloseRequest, shutdownDone <-chan struct{}) {
	t.Helper()
	select {
	case streamID := <-hostCloses:
		if streamID != "host-stalled" {
			t.Fatalf("closed host stream = %q, want host-stalled", streamID)
		}
	case <-time.After(shutdownTestTimeout):
		t.Fatal("shutdown did not close the active host stream")
	}
	select {
	case closeRequest := <-pluginCloses:
		if closeRequest.StreamID != "plugin-stalled" {
			t.Fatalf("closed plugin stream = %q, want plugin-stalled", closeRequest.StreamID)
		}
	case <-time.After(shutdownTestTimeout):
		t.Fatal("stream worker did not close the plugin stream")
	}
	select {
	case <-shutdownDone:
	case <-time.After(shutdownTestTimeout):
		t.Fatal("shutdown did not drain the active stream")
	}
	select {
	case extraID := <-hostCloses:
		t.Fatalf("host stream closed more than once: extra %q", extraID)
	default:
	}
}
