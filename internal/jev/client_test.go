package jev

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chloeassistant/cpa-plugin-auto-router/internal/snippet"
)

func TestDecideParsesAnswerAndUsesLowercaseSignals(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		state, ok := payload["state"].(map[string]any)
		if !ok {
			t.Fatalf("state = %#v", payload["state"])
		}
		signals, ok := state["signals"].(map[string]any)
		if !ok {
			t.Fatalf("signals = %#v", state["signals"])
		}
		for _, key := range []string{"tools", "images", "messages", "format"} {
			if _, ok := signals[key]; !ok {
				t.Errorf("missing lowercase signal %q in %#v", key, signals)
			}
		}
		if _, ok := signals["Tools"]; ok {
			t.Error("wire signal must not use Go field name Tools")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"answers":{"category":{"choice":"backend","probabilities":{"backend":0.8},"confidence":0.9},"difficulty":{"choice":"hard","confidence":0.7}}}`))
	}))
	defer server.Close()

	result, err := Decide(context.Background(), Config{
		BaseURL:      server.URL,
		EndpointPath: "/decide",
		Model:        "typesafe/jev-1.13",
		APIKey:       "secret",
		Timeout:      time.Second,
	}, "fix the API", snippet.Signals{Tools: 2, Images: 1, Messages: 3, Format: "responses"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Category.Choice != "backend" || result.Category.Probabilities["backend"] != 0.8 || result.Category.Confidence != 0.9 {
		t.Fatalf("category = %+v", result.Category)
	}
	if result.Difficulty.Choice != "hard" || result.Difficulty.Probabilities["hard"] != 1 || result.Millis < 0 {
		t.Fatalf("difficulty = %+v", result.Difficulty)
	}
}

func TestDecideRejectsFailuresAndInvalidChoices(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "http failure", body: "upstream secret body"},
		{name: "invalid choice", body: `{"answers":{"category":{"choice":"not-a-category","confidence":0.9},"difficulty":{"choice":"hard","confidence":0.9}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status := http.StatusOK
			if tc.name == "http failure" {
				status = http.StatusBadGateway
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			_, err := Decide(context.Background(), Config{BaseURL: server.URL, Model: "jev", APIKey: "secret", Timeout: time.Second}, "item", snippet.Signals{})
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("error = %v, want ErrUnavailable", err)
			}
		})
	}
}

func TestDecideRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 1<<20+1)))
	}))
	defer server.Close()
	_, err := Decide(context.Background(), Config{BaseURL: server.URL, Model: "jev", APIKey: "secret", Timeout: time.Second}, "item", snippet.Signals{})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
}

func TestDecideDoesNotFollowRedirectWithBearer(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirect.Close()
	_, err := Decide(context.Background(), Config{BaseURL: redirect.URL, Model: "jev", APIKey: "secret", Timeout: time.Second}, "item", snippet.Signals{})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
	if targetHits.Load() != 0 {
		t.Fatal("redirect target received a request")
	}
}

func TestValidateURLPolicy(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
		ok   bool
	}{
		{name: "external cleartext", url: "http://example.com", ok: false},
		{name: "loopback cleartext", url: "http://127.0.0.1:9", ok: true},
		{name: "embedded credentials", url: "https://u:p@host", ok: false},
		{name: "public tls", url: "https://example.com", ok: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := (Config{BaseURL: tc.url}).Validate()
			if (err == nil) != tc.ok {
				t.Fatalf("Validate(%q) error = %v, want ok=%v", tc.url, err, tc.ok)
			}
		})
	}
}
