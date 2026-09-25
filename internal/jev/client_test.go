package jev

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chloeassistant/cpa-plugin-auto-router/internal/snippet"
)

func TestDecideSendsCalibratedQuestionMapAndParsesFactors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			State struct {
				Context string `json:"context"`
				Item    string `json:"item"`
			} `json:"state"`
			Questions map[string]struct {
				Type         string   `json:"type"`
				Instructions string   `json:"instructions"`
				Criteria     []string `json:"criteria"`
			} `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload.State.Context != "Request to an LLM proxy. Classify the task the user is asking for." || payload.State.Item != "fix the API" {
			t.Fatalf("state = %#v", payload.State)
		}
		names := []string{"touches_code", "frontend", "fix_existing", "judges_existing", "design_only", "many_steps", "transform_only", "exact_answer", "writes_tests", "effort"}
		instructions := map[string]string{
			"touches_code":    "Does `item` ask to write or change code?",
			"frontend":        "Is the deliverable of `item` a user-visible web UI (HTML/CSS/JS/components)?",
			"fix_existing":    "Does `item` ask to explain or fix something that already fails?",
			"judges_existing": "Does `item` ask to evaluate, critique, review or test code that already exists?",
			"design_only":     "Does `item` want a plan, architecture or spec rather than code now?",
			"many_steps":      "Will fulfilling `item` require chaining several shell commands, tools or files?",
			"transform_only":  "Is `item` just extracting, reformatting or classifying given data?",
			"exact_answer":    "Does `item` ask for a number or figure that can be computed or verified from given data?",
			"writes_tests":    "Does `item` ask to write or add tests for code?",
			"effort":          "How much effort would a strong senior engineer need for `item`?",
		}
		criteria := []string{
			"a minute: one-liner, lookup or trivial edit",
			"under an hour: known pattern, one file or one component",
			"a few hours: several parts, needs some design or care",
			"a day or more: real trade-offs, many constraints or a large system",
			"open-ended: investigation or research before the work can even start",
		}
		if len(payload.Questions) != len(names) {
			t.Fatalf("questions = %#v", payload.Questions)
		}
		for _, name := range names {
			question, ok := payload.Questions[name]
			if !ok || question.Instructions != instructions[name] {
				t.Errorf("question[%q] = %#v", name, question)
			}
			if name != "effort" {
				if question.Type != "noul" || question.Criteria != nil {
					t.Errorf("question[%q] type/criteria = %q/%#v", name, question.Type, question.Criteria)
				}
			} else if question.Type != "score" || !reflect.DeepEqual(question.Criteria, criteria) {
				t.Errorf("effort question = %#v", question)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"answers":{"touches_code":{"noul":0.91},"frontend":{"noul":0.12},"fix_existing":{"noul":0.83},"judges_existing":{"noul":0.08},"design_only":{"noul":0.04},"many_steps":{"noul":0.71},"transform_only":{"noul":0.02},"exact_answer":{"noul":0.01},"writes_tests":{"noul":0.14},"effort":{"probabilities":{"0":0.01,"1":0.09,"2":0.2,"3":0.6,"4":0.1}}}}`))
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
	wantFactors := map[string]float64{"touches_code": 0.91, "frontend": 0.12, "fix_existing": 0.83, "judges_existing": 0.08, "design_only": 0.04, "many_steps": 0.71, "transform_only": 0.02, "exact_answer": 0.01, "writes_tests": 0.14}
	wantEffort := map[string]float64{"0": 0.01, "1": 0.09, "2": 0.2, "3": 0.6, "4": 0.1}
	if !reflect.DeepEqual(map[string]float64(result.Factors), wantFactors) || !reflect.DeepEqual(map[string]float64(result.Effort), wantEffort) || result.Millis < 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestDecideUsesOneHotEffortScoreWhenProbabilitiesAreMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"answers":{"touches_code":{"noul":0.9},"frontend":{"noul":0.1},"fix_existing":{"noul":0.2},"judges_existing":{"noul":0.1},"design_only":{"noul":0.1},"many_steps":{"noul":0.2},"transform_only":{"noul":0.1},"exact_answer":{"noul":0.1},"writes_tests":{"noul":0.1},"effort":{"score":3}}}`))
	}))
	defer server.Close()
	result, err := Decide(context.Background(), Config{BaseURL: server.URL, Model: "jev", APIKey: "secret", Timeout: time.Second}, "item", snippet.Signals{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Effort) != 5 || result.Effort["3"] != 1 || result.Effort["0"] != 0 || result.Effort["4"] != 0 {
		t.Fatalf("effort = %#v", result.Effort)
	}
}

func TestDecideRejectsMalformedCalibratedAnswers(t *testing.T) {
	validFactors := `"touches_code":{"noul":0.9},"frontend":{"noul":0.1},"fix_existing":{"noul":0.2},"judges_existing":{"noul":0.1},"design_only":{"noul":0.1},"many_steps":{"noul":0.2},"transform_only":{"noul":0.1},"exact_answer":{"noul":0.1},"writes_tests":{"noul":0.1}`
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "missing factor", body: `{"answers":{"touches_code":{"noul":0.9},"effort":{"probabilities":{"0":0,"1":0,"2":0,"3":1,"4":0}}}}`},
		{name: "invalid factor probability", body: `{"answers":{"touches_code":{"noul":1.1},"frontend":{"noul":0.1},"fix_existing":{"noul":0.2},"judges_existing":{"noul":0.1},"design_only":{"noul":0.1},"many_steps":{"noul":0.2},"transform_only":{"noul":0.1},"exact_answer":{"noul":0.1},"writes_tests":{"noul":0.1},"effort":{"probabilities":{"0":0,"1":0,"2":0,"3":1,"4":0}}}}`},
		{name: "invalid effort probability", body: `{"answers":{` + validFactors + `,"effort":{"probabilities":{"0":0,"1":0,"2":0,"3":1.1,"4":0}}}}`},
		{name: "null effort probability", body: `{"answers":{` + validFactors + `,"effort":{"probabilities":{"0":null,"1":0,"2":0,"3":1,"4":0}}}}`},
		{name: "invalid fallback score", body: `{"answers":{` + validFactors + `,"effort":{"score":2.5}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tc.body)) }))
			defer server.Close()
			_, err := Decide(context.Background(), Config{BaseURL: server.URL, Model: "jev", APIKey: "secret", Timeout: time.Second}, "item", snippet.Signals{})
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("error = %v, want ErrUnavailable", err)
			}
		})
	}
}

func TestDecideParsesCalibratedResultAndUsesLowercaseSignals(t *testing.T) {
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
		_, _ = w.Write([]byte(`{"answers":{"touches_code":{"noul":0.8},"frontend":{"noul":0.1},"fix_existing":{"noul":0.2},"judges_existing":{"noul":0.1},"design_only":{"noul":0.1},"many_steps":{"noul":0.2},"transform_only":{"noul":0.1},"exact_answer":{"noul":0.1},"writes_tests":{"noul":0.1},"effort":{"probabilities":{"0":0,"1":0,"2":0,"3":1,"4":0}}}}`))
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
	if result.Factors["touches_code"] != 0.8 || len(result.Factors) != 9 {
		t.Fatalf("factors = %#v", result.Factors)
	}
	if result.Effort["3"] != 1 || len(result.Effort) != 5 || result.Millis < 0 {
		t.Fatalf("effort = %#v", result.Effort)
	}
}

func TestDecideRejectsFailuresAndMalformedCalibratedAnswers(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "http failure", body: "upstream secret body"},
		{name: "invalid factor", body: `{"answers":{"touches_code":{"noul":1.2}}}`},
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
