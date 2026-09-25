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

	"github.com/chloeassistant/cpa-plugin-auto-router/internal/decide"
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
type Result struct {
	Factors decide.Factors
	Effort  decide.EffortDistribution
	Millis  int64
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
		Model     string              `json:"model"`
		State     decisionState       `json:"state"`
		Questions map[string]question `json:"questions"`
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

type question struct {
	Type         string   `json:"type"`
	Instructions string   `json:"instructions"`
	Criteria     []string `json:"criteria,omitempty"`
}

func questions() map[string]question {
	return map[string]question{
		"touches_code":    {Type: "noul", Instructions: "Does `item` ask to write or change code?"},
		"frontend":        {Type: "noul", Instructions: "Is the deliverable of `item` a user-visible web UI (HTML/CSS/JS/components)?"},
		"fix_existing":    {Type: "noul", Instructions: "Does `item` ask to explain or fix something that already fails?"},
		"judges_existing": {Type: "noul", Instructions: "Does `item` ask to evaluate, critique, review or test code that already exists?"},
		"design_only":     {Type: "noul", Instructions: "Does `item` want a plan, architecture or spec rather than code now?"},
		"many_steps":      {Type: "noul", Instructions: "Will fulfilling `item` require chaining several shell commands, tools or files?"},
		"transform_only":  {Type: "noul", Instructions: "Is `item` just extracting, reformatting or classifying given data?"},
		"exact_answer":    {Type: "noul", Instructions: "Does `item` ask for a number or figure that can be computed or verified from given data?"},
		"writes_tests":    {Type: "noul", Instructions: "Does `item` ask to write or add tests for code?"},
		"effort": {Type: "score", Instructions: "How much effort would a strong senior engineer need for `item`?", Criteria: []string{
			"a minute: one-liner, lookup or trivial edit",
			"under an hour: known pattern, one file or one component",
			"a few hours: several parts, needs some design or care",
			"a day or more: real trade-offs, many constraints or a large system",
			"open-ended: investigation or research before the work can even start",
		}},
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
	factors := make(decide.Factors, len(categoryFactors))
	for _, name := range categoryFactors {
		value, err := parseNoul(envelope.Answers[name])
		if err != nil {
			return Result{}, fmt.Errorf("invalid factor %s: %w", name, err)
		}
		factors[name] = value
	}
	effort, err := parseEffort(envelope.Answers["effort"])
	if err != nil {
		return Result{}, fmt.Errorf("invalid effort answer: %w", err)
	}
	return Result{Factors: factors, Effort: effort}, nil
}

var categoryFactors = []string{"touches_code", "frontend", "fix_existing", "judges_existing", "design_only", "many_steps", "transform_only", "exact_answer", "writes_tests"}

func parseNoul(raw json.RawMessage) (float64, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, errors.New("answer is missing")
	}
	var wire struct {
		Noul *float64 `json:"noul"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return 0, errors.New("answer is malformed")
	}
	if wire.Noul == nil {
		return 0, errors.New("noul is missing")
	}
	if !validProbability(*wire.Noul) {
		return 0, errors.New("noul is invalid")
	}
	return *wire.Noul, nil
}

func parseEffort(raw json.RawMessage) (decide.EffortDistribution, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, errors.New("answer is missing")
	}
	var wire struct {
		Probabilities map[string]*float64 `json:"probabilities"`
		Score         *float64            `json:"score"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, errors.New("answer is malformed")
	}
	if wire.Probabilities != nil {
		if len(wire.Probabilities) != 5 {
			return nil, errors.New("effort probabilities are incomplete")
		}
		out := make(decide.EffortDistribution, 5)
		total := 0.0
		for i := 0; i < 5; i++ {
			key := fmt.Sprint(i)
			probability, ok := wire.Probabilities[key]
			if !ok || probability == nil || !validProbability(*probability) {
				return nil, fmt.Errorf("probability for %s is invalid", key)
			}
			out[key] = *probability
			total += *probability
		}
		if total < 0.98-1e-9 || total > 1.02+1e-9 {
			return nil, errors.New("effort probabilities are not normalized")
		}
		return out, nil
	}
	if wire.Score == nil || math.IsNaN(*wire.Score) || math.IsInf(*wire.Score, 0) || *wire.Score < 0 || *wire.Score > 4 || math.Trunc(*wire.Score) != *wire.Score {
		return nil, errors.New("effort score is invalid")
	}
	out := make(decide.EffortDistribution, 5)
	for i := 0; i < 5; i++ {
		out[fmt.Sprint(i)] = 0
	}
	out[fmt.Sprint(int(*wire.Score))] = 1
	return out, nil
}

func validProbability(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
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
