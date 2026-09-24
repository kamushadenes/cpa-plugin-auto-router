package main

import "testing"

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
