package session

import (
	"net/http"
	"testing"
	"time"

	"github.com/kamushadenes/cpa-plugin-auto-router/internal/decide"
)

func TestIDPrecedence(t *testing.T) {
	h := http.Header{"X-Session-Id": {"abc"}}
	if ID(h, []byte(`{"prompt_cache_key":"pck"}`)) != "abc" {
		t.Fatal("header wins")
	}
	if ID(nil, []byte(`{"prompt_cache_key":"pck"}`)) != "pck" {
		t.Fatal("prompt_cache_key")
	}
	a := ID(nil, []byte(`{"messages":[{"role":"system","content":"s"},{"role":"user","content":"hello"}]}`))
	b := ID(nil, []byte(`{"messages":[{"role":"system","content":"s"},{"role":"user","content":"hello"},{"role":"assistant","content":"x"}]}`))
	if a == "" || a != b {
		t.Fatalf("first-user hash must be stable across turns: %q %q", a, b)
	}
	r := ID(nil, []byte(`{"input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`))
	if r == "" {
		t.Fatal("responses input must hash")
	}
}

func TestIDUsesReferenceHeaderOrder(t *testing.T) {
	h := http.Header{
		"X-Session-Id":             {"generic"},
		"Session-Id":               {"codex"},
		"X-Claude-Code-Session-Id": {"claude"},
	}
	if got := ID(h, nil); got != "claude" {
		t.Fatalf("reference header order = %q", got)
	}
	h.Del("X-Claude-Code-Session-Id")
	if got := ID(h, nil); got != "codex" {
		t.Fatalf("codex header should beat generic header = %q", got)
	}
}

func TestIDDoesNotHashNonUserContent(t *testing.T) {
	body := []byte(`{"system":"secret system prompt","messages":[{"role":"tool","content":"tool result"}]}`)
	if got := ID(nil, body); got != "" {
		t.Fatalf("must not hash system or tool content: %q", got)
	}
}

func TestStoreTTLAndEvict(t *testing.T) {
	clock := time.Unix(100, 0)
	s := New(50*time.Millisecond, 2)
	s.now = func() time.Time { return clock }
	_, _, generation := s.Begin("a")
	s.Put("a", generation, decide.State{Model: "m"})
	if _, ok := s.Get("a"); !ok {
		t.Fatal("get")
	}
	clock = clock.Add(50 * time.Millisecond)
	if _, ok := s.Get("a"); ok {
		t.Fatal("expired")
	}
	for _, id := range []string{"1", "2", "3"} {
		_, _, generation = s.Begin(id)
		s.Put(id, generation, decide.State{})
	}
	if _, ok := s.Get("1"); ok {
		t.Fatal("oldest evicted")
	}
}

func TestStorePutPreservesHigherDifficulty(t *testing.T) {
	s := New(time.Hour, 2)
	_, _, olderGeneration := s.Begin("session")
	_, _, newerGeneration := s.Begin("session")
	s.Put("session", olderGeneration, decide.State{Difficulty: decide.Hard, Model: "hard-model", Thinking: "xhigh", Tier: "top"})
	s.Put("session", newerGeneration, decide.State{Difficulty: decide.Routine, Model: "routine-model", Thinking: "high", Tier: "mid"})

	state, ok := s.Get("session")
	if !ok {
		t.Fatal("session missing")
	}
	if state.Difficulty != decide.Hard || state.Model != "hard-model" || state.Thinking != "xhigh" || state.Tier != "top" {
		t.Fatalf("state downgraded: %#v", state)
	}
}
