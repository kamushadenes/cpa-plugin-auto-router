package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/chloeassistant/cpa-plugin-auto-router/internal/snippet"
)

var ErrUnavailable = errors.New("jev unavailable")

const (
	defaultEndpointPath = "/api/alpha/decisions"
	defaultModel        = "typesafe/jev-1.13"
	maxResponseBytes    = 1 << 20
)

type Config struct {
	BaseURL      string
	EndpointPath string
	Model        string
	APIKey       string
	Timeout      time.Duration
}

type Answer struct {
	Choice        string
	Probabilities map[string]float64
	Confidence    float64
}

type Result struct {
	Category   Answer
	Difficulty Answer
	Millis     int64
}

func (c Config) Validate() error {
	parts, err := url.Parse(strings.TrimSpace(c.BaseURL))
	if err != nil {
		return fmt.Errorf("invalid jev base url: %w", err)
	}
	if parts.User != nil || parts.Fragment != "" || parts.Scheme == "" || parts.Host == "" {
		return errors.New("invalid jev base url")
	}
	scheme := strings.ToLower(parts.Scheme)
	if scheme != "http" && scheme != "https" {
		return errors.New("jev base url must use http or https")
	}
	if scheme == "http" && !allowsCleartext(parts.Hostname()) {
		return errors.New("jev cleartext url is not local")
	}
	if _, err := normalizeEndpointPath(c.EndpointPath); err != nil {
		return err
	}
	return nil
}

func Decide(ctx context.Context, c Config, item string, sig snippet.Signals) (Result, error) {
	if err := c.Validate(); err != nil {
		return Result{}, unavailable(err.Error())
	}
	apiKey := strings.TrimSpace(c.APIKey)
	if apiKey == "" {
		return Result{}, unavailable("jev api key is empty")
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	model := strings.TrimSpace(c.Model)
	if model == "" {
		model = defaultModel
	}
	requestBody, err := json.Marshal(struct {
		Model     string                    `json:"model"`
		State     decisionState             `json:"state"`
		Questions map[string]choiceQuestion `json:"questions"`
	}{
		Model: model,
		State: decisionState{
			Context: "Request to an LLM proxy. Classify the task the user is asking for.",
			Item:    item,
			Signals: sig,
		},
		Questions: questions(),
	})
	if err != nil {
		return Result{}, unavailable("jev request encoding failed")
	}
	endpoint, err := joinURL(c.BaseURL, c.EndpointPath)
	if err != nil {
		return Result{}, unavailable(err.Error())
	}

	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(requestBody))
	if err != nil {
		return Result{}, unavailable("jev request creation failed")
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, unavailable("jev transport failed")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return Result{}, unavailable("jev response read failed")
	}
	if len(body) > maxResponseBytes {
		return Result{}, unavailable("jev response exceeds size limit")
	}
	if resp.StatusCode != http.StatusOK {
		return Result{}, unavailable(fmt.Sprintf("jev request failed with status %d", resp.StatusCode))
	}
	result, err := parseResult(body)
	if err != nil {
		return Result{}, unavailable(err.Error())
	}
	result.Millis = time.Since(started).Milliseconds()
	return result, nil
}

type decisionState struct {
	Context string          `json:"context"`
	Item    string          `json:"item"`
	Signals snippet.Signals `json:"signals"`
}

type choiceQuestion struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria"`
}

func questions() map[string]choiceQuestion {
	return map[string]choiceQuestion{
		"category": {
			Type:         "choice",
			Instructions: "Classify the task in `item` (use `signals` as hints).",
			Criteria: map[string]string{
				"webdev":           "front-end/web UI/HTML/CSS/JS apps",
				"backend":          "server code, APIs, data models, implementation in a repo",
				"agentic-terminal": "multi-step work driving shell/tools/files",
				"debugging":        "find why something fails; trace behaviour",
				"review":           "read, critique or test existing code; security review",
				"spec-design":      "architecture, design, planning, specs",
				"writing":          "prose, docs, messages, summaries for humans",
				"extraction":       "extract/reformat/classify data, tiny transformations",
				"math-data":        "math, statistics, data analysis with a definite answer",
			},
		},
		"difficulty": {
			Type:         "choice",
			Instructions: "How hard is `item` for a strong model?",
			Criteria: map[string]string{
				"trivial": "one-liner or lookup, no reasoning",
				"routine": "standard task, known pattern",
				"hard":    "needs real reasoning, many constraints or a large codebase",
				"extreme": "research-grade, ambiguous, or very long multi-step",
			},
		},
	}
}

func parseResult(body []byte) (Result, error) {
	var envelope struct {
		Answers map[string]json.RawMessage `json:"answers"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return Result{}, errors.New("jev response is malformed json")
	}
	if envelope.Answers == nil {
		return Result{}, errors.New("jev response is missing answers")
	}
	category, err := parseAnswer(envelope.Answers["category"], map[string]bool{
		"webdev": true, "backend": true, "agentic-terminal": true, "debugging": true,
		"review": true, "spec-design": true, "writing": true, "extraction": true, "math-data": true,
	})
	if err != nil {
		return Result{}, fmt.Errorf("invalid category answer: %w", err)
	}
	difficulty, err := parseAnswer(envelope.Answers["difficulty"], map[string]bool{
		"trivial": true, "routine": true, "hard": true, "extreme": true,
	})
	if err != nil {
		return Result{}, fmt.Errorf("invalid difficulty answer: %w", err)
	}
	return Result{Category: category, Difficulty: difficulty}, nil
}

func parseAnswer(raw json.RawMessage, allowed map[string]bool) (Answer, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return Answer{}, errors.New("answer is missing")
	}
	var wire struct {
		Choice        string             `json:"choice"`
		Probabilities map[string]float64 `json:"probabilities"`
		Confidence    *float64           `json:"confidence"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return Answer{}, errors.New("answer is malformed")
	}
	choice := strings.TrimSpace(wire.Choice)
	if !allowed[choice] {
		return Answer{}, errors.New("choice is invalid")
	}
	if wire.Probabilities == nil {
		wire.Probabilities = map[string]float64{choice: 1}
	}
	for name, probability := range wire.Probabilities {
		if !math.IsNaN(probability) && !math.IsInf(probability, 0) && probability >= 0 && probability <= 1 {
			continue
		}
		return Answer{}, fmt.Errorf("probability for %s is invalid", name)
	}
	confidence := float64(0)
	if wire.Confidence != nil {
		confidence = *wire.Confidence
		if math.IsNaN(confidence) || math.IsInf(confidence, 0) || confidence < 0 || confidence > 1 {
			return Answer{}, errors.New("confidence is invalid")
		}
	}
	return Answer{Choice: choice, Probabilities: wire.Probabilities, Confidence: confidence}, nil
}

func unavailable(message string) error {
	return fmt.Errorf("%w: %s", ErrUnavailable, message)
}

func allowsCleartext(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	addr, err := netip.ParseAddr(host)
	if err != nil || addr.Is4In6() {
		return false
	}
	for _, prefix := range []string{
		"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "127.0.0.0/8",
		"::1/128", "fc00::/7", "fe80::/10", "fec0::/10",
	} {
		if netPrefix, err := netip.ParsePrefix(prefix); err == nil && netPrefix.Contains(addr) {
			return true
		}
	}
	return false
}

func normalizeEndpointPath(raw string) (string, error) {
	path := strings.TrimSpace(raw)
	if path == "" {
		return defaultEndpointPath, nil
	}
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#%@") {
		return "", errors.New("invalid jev endpoint path")
	}
	for _, r := range path {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("/._-~", r) {
			continue
		}
		return "", errors.New("invalid jev endpoint path")
	}
	return strings.TrimRight(path, "/"), nil
}

func joinURL(baseURL, endpointPath string) (string, error) {
	parts, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parts.User != nil || parts.Fragment != "" || parts.Scheme == "" || parts.Host == "" {
		return "", errors.New("invalid jev base url")
	}
	path, err := normalizeEndpointPath(endpointPath)
	if err != nil {
		return "", err
	}
	parts.Path = strings.TrimRight(parts.Path, "/") + path
	parts.RawPath = ""
	return parts.String(), nil
}
