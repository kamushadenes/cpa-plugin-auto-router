package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/kamushadenes/cpa-plugin-auto-router/internal/decide"
	"github.com/kamushadenes/cpa-plugin-auto-router/internal/jev"
	"github.com/kamushadenes/cpa-plugin-auto-router/internal/session"
	"github.com/kamushadenes/cpa-plugin-auto-router/internal/snippet"
	"github.com/kamushadenes/cpa-plugin-auto-router/internal/table"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

const (
	pluginIdentifier   = "auto-router"
	maxJevSnippetChars = 4000
)

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type pluginConfig struct {
	Enabled             bool    `yaml:"enabled"`
	JevAPIKeyEnv        string  `yaml:"jev_api_key_env"`
	JevBaseURL          string  `yaml:"jev_base_url"`
	JevEndpointPath     string  `yaml:"jev_endpoint_path"`
	JevModel            string  `yaml:"jev_model"`
	ConfidenceThreshold float64 `yaml:"confidence_threshold"`
	TablePath           string  `yaml:"table_path"`
	SnippetChars        int     `yaml:"snippet_chars"`
	JevTimeoutMS        int     `yaml:"jev_timeout_ms"`
}

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type registrationCapability struct {
	ModelRegistrar        bool     `json:"model_registrar"`
	ModelRouter           bool     `json:"model_router"`
	Executor              bool     `json:"executor"`
	ExecutorModelScope    string   `json:"executor_model_scope"`
	ExecutorInputFormats  []string `json:"executor_input_formats"`
	ExecutorOutputFormats []string `json:"executor_output_formats"`
}

type rpcExecutorRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcModelRouteRequest struct {
	pluginapi.ModelRouteRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcStreamEmitRequest struct {
	StreamID string `json:"stream_id"`
	Payload  []byte `json:"payload,omitempty"`
	Error    string `json:"error,omitempty"`
}

type rpcStreamCloseRequest struct {
	StreamID string `json:"stream_id"`
	Error    string `json:"error,omitempty"`
}

type pluginState struct {
	mu             sync.RWMutex
	cfg            pluginConfig
	watch          *table.Watched
	tableErr       error
	tableErrLogged bool
}

var (
	state pluginState
	// Anthropic's default prompt cache lives 5 minutes; after 10 idle minutes a
	// downgrade no longer costs cache, so the next message is decided afresh.
	store = session.New(10*time.Minute, 65536)
)

type pendingRoute struct {
	decision   decide.Decision
	context    routeContext
	generation uint64
}

var pending sync.Map

func init() {
	state.cfg = defaultPluginConfig()
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if err := configure(request); err != nil {
			return nil, err
		}
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodModelRegister, pluginabi.MethodModelStatic:
		return okEnvelope(modelRegistration())
	case pluginabi.MethodModelRoute:
		return routeModel(request)
	case pluginabi.MethodExecutorIdentifier:
		return okEnvelope(map[string]string{"identifier": pluginIdentifier})
	case pluginabi.MethodExecutorExecute:
		return execute(request)
	case pluginabi.MethodExecutorExecuteStream:
		return executeStream(request)
	case pluginabi.MethodExecutorCountTokens:
		return okEnvelope(pluginapi.ExecutorResponse{Payload: []byte(`{"input_tokens":0}`)})
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func defaultPluginConfig() pluginConfig {
	return pluginConfig{
		Enabled:             true,
		JevAPIKeyEnv:        "OPENROUTER_API_KEY",
		JevBaseURL:          "https://openrouter.ai",
		JevEndpointPath:     "/api/alpha/decisions",
		JevModel:            "typesafe/jev-1.13",
		ConfidenceThreshold: 0.6,
		TablePath:           "/home/hermes/cliproxyapi/plugins/auto-router/models.yaml",
		SnippetChars:        maxJevSnippetChars,
		JevTimeoutMS:        2000,
	}
}

func configure(raw []byte) error {
	var req lifecycleRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			return err
		}
	}
	cfg := defaultPluginConfig()
	if len(req.ConfigYAML) > 0 {
		if err := yaml.Unmarshal(req.ConfigYAML, &cfg); err != nil {
			return err
		}
	}
	if cfg.JevAPIKeyEnv == "" {
		cfg.JevAPIKeyEnv = "OPENROUTER_API_KEY"
	}
	if cfg.JevBaseURL == "" {
		cfg.JevBaseURL = "https://openrouter.ai"
	}
	if cfg.JevEndpointPath == "" {
		cfg.JevEndpointPath = "/api/alpha/decisions"
	}
	if cfg.JevModel == "" {
		cfg.JevModel = "typesafe/jev-1.13"
	}
	if cfg.ConfidenceThreshold == 0 {
		cfg.ConfidenceThreshold = 0.6
	}
	if cfg.TablePath == "" {
		cfg.TablePath = "/home/hermes/cliproxyapi/plugins/auto-router/models.yaml"
	}
	if cfg.SnippetChars <= 0 {
		cfg.SnippetChars = maxJevSnippetChars
	}
	if cfg.SnippetChars > maxJevSnippetChars {
		cfg.SnippetChars = maxJevSnippetChars
	}
	if cfg.JevTimeoutMS == 0 {
		cfg.JevTimeoutMS = 2000
	}
	watch, err := table.Watch(cfg.TablePath)
	state.mu.Lock()
	state.cfg = cfg
	state.watch = watch
	state.tableErr = err
	state.tableErrLogged = false
	state.mu.Unlock()
	return nil
}

func loadedConfig() pluginConfig {
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.cfg
}

func loadedTable() (*table.Table, error) {
	state.mu.RLock()
	watch := state.watch
	err := state.tableErr
	state.mu.RUnlock()
	if watch == nil {
		if err == nil {
			err = errors.New("auto-router table is not configured")
		}
		return nil, err
	}
	return watch.Get(), nil
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             "auto-router",
			Version:          "0.1.8",
			Author:           "kamushadenes",
			GitHubRepository: "https://github.com/kamushadenes/cpa-plugin-auto-router",
			ConfigFields: []pluginapi.ConfigField{
				{Name: "enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Enable auto-router requests."},
				{Name: "jev_api_key_env", Type: pluginapi.ConfigFieldTypeString, Description: "Environment variable containing the Jev API key."},
				{Name: "jev_base_url", Type: pluginapi.ConfigFieldTypeString, Description: "Jev service base URL."},
				{Name: "jev_endpoint_path", Type: pluginapi.ConfigFieldTypeString, Description: "Jev service endpoint path."},
				{Name: "jev_model", Type: pluginapi.ConfigFieldTypeString, Description: "Jev model identifier."},
				{Name: "confidence_threshold", Type: pluginapi.ConfigFieldTypeNumber, Description: "Minimum Jev confidence used for a label."},
				{Name: "table_path", Type: pluginapi.ConfigFieldTypeString, Description: "Path to the local benchmark table."},
				{Name: "snippet_chars", Type: pluginapi.ConfigFieldTypeInteger, Description: "Maximum user snippet size."},
				{Name: "jev_timeout_ms", Type: pluginapi.ConfigFieldTypeInteger, Description: "Jev request timeout in milliseconds."},
			},
		},
		Capabilities: registrationCapability{
			ModelRegistrar:        true,
			ModelRouter:           true,
			Executor:              true,
			ExecutorModelScope:    string(pluginapi.ExecutorModelScopeStatic),
			ExecutorInputFormats:  []string{"chat-completions", "responses"},
			ExecutorOutputFormats: []string{"chat-completions", "responses"},
		},
	}
}

func modelRegistration() pluginapi.ModelRegistrationResponse {
	return pluginapi.ModelRegistrationResponse{
		Provider: pluginIdentifier,
		Models: []pluginapi.ModelInfo{{
			ID:                         pluginIdentifier,
			Object:                     "model",
			OwnedBy:                    pluginIdentifier,
			DisplayName:                "Auto Router (Jev)",
			SupportedGenerationMethods: []string{"chat"},
			ContextLength:              1000000,
			UserDefined:                true,
		}},
	}
}

type routeMeta struct {
	category             string
	factors              decide.Factors
	effortP              decide.EffortDistribution
	effortMean           float64
	difficulty           string
	categoryConfidence   float64
	difficultyConfidence float64
	confidence           float64
	jevMillis            int64
	jevHardened          bool
	hasImage             bool
	estTokens            int
	sensitive            float64
	claim                float64
	guard                string
}

type routeContext struct {
	category             string
	factors              decide.Factors
	effortP              decide.EffortDistribution
	effortMean           float64
	difficulty           string
	categoryConfidence   float64
	difficultyConfidence float64
	confidence           float64
	jevMillis            int64
	hasImage             bool
	estTokens            int
	previousModel        string
	errorBumped          bool
	// transitionReason is the routing reason before the executor path rewrites
	// it to "reclassified", so a notice reports why the model actually changed.
	transitionReason string
}

func routeModel(raw []byte) ([]byte, error) {
	var req rpcModelRouteRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if stripThinkingSuffix(req.RequestedModel) != pluginIdentifier {
		return okEnvelope(pluginapi.ModelRouteResponse{Handled: false})
	}
	cfg := loadedConfig()
	if !cfg.Enabled {
		return okEnvelope(pluginapi.ModelRouteResponse{Handled: false})
	}
	sid := session.ID(req.Headers, req.Body)
	prev, hasPrev, generation := store.Begin(sid)
	decision, meta, routeCtx, err := decideForWithContext(req.ModelRouteRequest, prev, hasPrev)
	if err != nil {
		state.mu.Lock()
		tableErr := state.tableErr
		firstTableErr := tableErr != nil && !state.tableErrLogged
		if firstTableErr {
			state.tableErrLogged = true
		}
		state.mu.Unlock()
		if firstTableErr {
			hostLog(req.HostCallbackID, "error", "auto-router table unavailable", map[string]any{"error": tableErr.Error()})
		} else if tableErr == nil {
			hostLog(req.HostCallbackID, "error", "auto-router decision failed", map[string]any{"error": err.Error()})
		}
		return okEnvelope(pluginapi.ModelRouteResponse{Handled: false})
	}
	if sid != "" {
		effective := store.Put(sid, generation, decision.State)
		decision, err = reconcileRouteDecision(decision, effective, routeCtx)
		if err != nil {
			return nil, err
		}
	}
	key := requestKey(req.SourceFormat, req.Headers, req.Body)
	if key != "" {
		pending.LoadOrStore(key, pendingRoute{decision: decision, context: routeCtx, generation: generation})
	}
	return routeResponse(req.HostCallbackID, sid, decision, meta)
}

func reconcileRouteDecision(decision decide.Decision, effective decide.State, routeCtx routeContext) (decide.Decision, error) {
	if effective.Model == "" || (effective.Difficulty == decision.State.Difficulty && effective.Tier == decision.State.Tier && effective.Thinking == decision.State.Thinking) {
		return decision, nil
	}
	tb, err := loadedTable()
	if err != nil {
		return decide.Decision{}, err
	}
	reconciled, err := decide.Next(decide.Input{
		Table:      tb,
		Category:   routeCtx.category,
		Difficulty: effective.Difficulty,
		HasImage:   routeCtx.hasImage,
		EstTokens:  routeCtx.estTokens,
		Exclude:    excluded,
	}, effective, true)
	if err != nil {
		return decide.Decision{}, err
	}
	return reconciled, nil
}

func oneStepBelowDifficulty(difficulty string) string {
	switch difficulty {
	case decide.Routine:
		return decide.Trivial
	case decide.Hard:
		return decide.Routine
	case decide.Extreme:
		return decide.Hard
	default:
		return decide.Trivial
	}
}

func lowConfidenceDifficulty(jevLabel, previous string) string {
	// ponytail: below-gate means uncertainty between adjacent bands; take the lower band and keep the session floor.
	fallback := oneStepBelowDifficulty(jevLabel)
	if decide.Rank(previous) > decide.Rank(fallback) {
		return previous
	}
	return fallback
}

// toolErrorBumpThreshold is the number of consecutive explicitly marked,
// correlated tool failures that raise difficulty one band, once per episode.
const toolErrorBumpThreshold = 3

// Guard thresholds follow dirien/jev-router's design: a likely state-changing
// request goes to the top tier, and a likely routing claim in the prompt cannot
// pull a fresh session below the new-session default.
const (
	sensitiveGuardThreshold = 0.7
	claimGuardThreshold     = 0.5
)

func oneStepAboveDifficulty(difficulty string) string {
	switch difficulty {
	case decide.Trivial:
		return decide.Routine
	case decide.Routine:
		return decide.Hard
	case decide.Hard:
		return decide.Extreme
	default:
		return difficulty
	}
}

func decideFor(req pluginapi.ModelRouteRequest, prev decide.State, hasPrev bool) (decide.Decision, error) {
	decision, _, _, err := decideForWithContext(req, prev, hasPrev)
	return decision, err
}

func decideForWithMeta(req pluginapi.ModelRouteRequest, prev decide.State, hasPrev bool) (decide.Decision, routeMeta, error) {
	decision, meta, _, err := decideForWithContext(req, prev, hasPrev)
	return decision, meta, err
}

func decideForWithContext(req pluginapi.ModelRouteRequest, prev decide.State, hasPrev bool) (decide.Decision, routeMeta, routeContext, error) {
	cfg := loadedConfig()
	tb, err := loadedTable()
	if err != nil {
		return decide.Decision{}, routeMeta{}, routeContext{}, err
	}
	signals := snippet.Extract(req.Body)
	estTokens := signals.EstTokens
	meta := routeMeta{hasImage: signals.Images > 0, estTokens: estTokens}
	var category, difficulty string
	jevOK := false
	if hasPrev && (prev.Difficulty == decide.Extreme || !signals.HasNewUserMessage) {
		difficulty = prev.Difficulty
		jevOK = true
	} else {
		difficulty = decide.Routine
		if hasPrev {
			difficulty = prev.Difficulty
		}
		jevCfg := jev.Config{
			BaseURL:      cfg.JevBaseURL,
			EndpointPath: cfg.JevEndpointPath,
			Model:        cfg.JevModel,
			APIKey:       os.Getenv(cfg.JevAPIKeyEnv),
			Timeout:      time.Duration(cfg.JevTimeoutMS) * time.Millisecond,
		}
		state := snippet.BuildState(req.Headers, req.Body, cfg.SnippetChars, estTokens)
		jevResult, jevErr := jev.Decide(context.Background(), jevCfg, state)
		if jevErr == nil {
			jevOK = true
			meta.factors = jevResult.Factors
			meta.effortP = jevResult.Effort
			meta.effortMean = decide.EffortMean(jevResult.Effort)
			meta.categoryConfidence = decide.CategoryConfidence(jevResult.Factors)
			meta.difficultyConfidence = decide.DifficultyConfidence(jevResult.Effort)
			meta.confidence = meta.difficultyConfidence
			meta.jevMillis = jevResult.Millis
			meta.jevHardened = jevResult.Hardened
			if meta.categoryConfidence >= cfg.ConfidenceThreshold {
				category = decide.Category(jevResult.Factors)
				meta.category = category
			}
			if meta.difficultyConfidence >= cfg.ConfidenceThreshold {
				difficulty = decide.Difficulty(jevResult.Effort)
			} else {
				difficulty = lowConfidenceDifficulty(decide.Difficulty(jevResult.Effort), prev.Difficulty)
			}
			meta.sensitive = jevResult.Sensitive
			meta.claim = jevResult.Claim
			if jevResult.Sensitive >= sensitiveGuardThreshold && decide.Rank(difficulty) < decide.Rank(decide.Hard) {
				difficulty = decide.Hard
				meta.guard = "risk-override"
			} else if jevResult.Claim >= claimGuardThreshold && decide.Rank(difficulty) < decide.Rank(decide.Routine) {
				difficulty = decide.Routine
				meta.guard = "claim-guard"
			}
			meta.difficulty = difficulty
		}
	}
	if meta.difficulty == "" {
		meta.difficulty = difficulty
	}
	episode := prev.ErrorEpisode
	bumped := false
	if signals.ToolErrorStreak == 0 {
		episode = ""
	} else if signals.ToolErrorStreak >= toolErrorBumpThreshold && signals.ToolErrorEpisode != episode {
		difficulty = oneStepAboveDifficulty(difficulty)
		meta.difficulty = difficulty
		episode = signals.ToolErrorEpisode
		bumped = true
	}
	input := decide.Input{Table: tb, Category: category, Difficulty: difficulty, HasImage: signals.Images > 0, EstTokens: estTokens, Exclude: excluded}
	decision, err := decide.Next(input, prev, jevOK)
	if err != nil {
		return decide.Decision{}, routeMeta{}, routeContext{}, err
	}
	decision.State.ErrorEpisode = episode
	return decision, meta, routeContext{category: category, factors: meta.factors, effortP: meta.effortP, effortMean: meta.effortMean, difficulty: difficulty, categoryConfidence: meta.categoryConfidence, difficultyConfidence: meta.difficultyConfidence, confidence: meta.confidence, jevMillis: meta.jevMillis, hasImage: signals.Images > 0, estTokens: estTokens, previousModel: prev.Model, errorBumped: bumped, transitionReason: decision.Reason}, nil
}

func routeResponse(callbackID, sid string, decision decide.Decision, meta routeMeta) ([]byte, error) {
	// difficulty is the effective one, after the confidence gate, guards and
	// the tool-failure raise; category stays the composed label.
	category, difficulty := meta.category, meta.difficulty
	if len(meta.factors) > 0 {
		category = decide.Category(meta.factors)
	}
	fields := map[string]any{
		"session":               hashSession(sid),
		"category":              category,
		"factors":               meta.factors,
		"effort_p":              meta.effortP,
		"effort_mean":           meta.effortMean,
		"difficulty":            difficulty,
		"category_confidence":   meta.categoryConfidence,
		"difficulty_confidence": meta.difficultyConfidence,
		"confidence":            meta.confidence,
		"tier":                  decision.Tier,
		"model":                 decision.Model,
		"thinking":              decision.Thinking,
		"reason":                decision.Reason,
		"jev_ms":                meta.jevMillis,
		"jev_hardened":          meta.jevHardened,
		"est_tokens":            meta.estTokens,
		"context_filtered":      decision.ContextFiltered,
		"sensitive":             meta.sensitive,
		"claim":                 meta.claim,
		"guard":                 meta.guard,
	}
	payload, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	hostLog(callbackID, "info", string(payload), nil)
	return okEnvelope(pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetSelf, Reason: decision.Reason})
}

func requestKey(sourceFormat string, headers http.Header, body []byte) string {
	sid := session.ID(headers, body)
	if sid == "" {
		return ""
	}
	hashInput := make([]byte, 0, len(sourceFormat)+1+len(body))
	hashInput = append(hashInput, sourceFormat...)
	hashInput = append(hashInput, 0)
	hashInput = append(hashInput, body...)
	sum := sha256.Sum256(hashInput)
	return sid + ":" + hex.EncodeToString(sum[:])
}

func stripThinkingSuffix(model string) string {
	model = strings.TrimSpace(model)
	if open := strings.LastIndexByte(model, '('); open >= 0 && strings.HasSuffix(model, ")") {
		return strings.TrimSpace(model[:open])
	}
	return model
}

func excluded(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(model, "gpt-image-") || strings.HasPrefix(model, "abliterated-") || model == "codex-auto-review"
}

func hashSession(raw string) string {
	if raw == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(raw))
	return "h:" + hex.EncodeToString(sum[:4])
}

func hostLog(callbackID, level, message string, fields map[string]any) {
	_, _ = hostCall(pluginabi.MethodHostLog, map[string]any{
		"host_callback_id": callbackID,
		"level":            level,
		"message":          message,
		"fields":           fields,
	})
}
