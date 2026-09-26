// Package notice appends a factual model-change notice to a request body so
// the model that runs a retry sees which model answered before it and why.
package notice

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Failover is the notice for a retry after a transport-level failure. It says
// only that the request did not complete, never that the earlier model was
// incapable.
func Failover(previousModel, effectiveModel, reason string) string {
	return fmt.Sprintf(
		"Router note: the previous request to %s did not complete and it was reissued to %s (reason: %s). "+
			"Preserve the goals, constraints, verified facts, and working tree already established; "+
			"reassess unverified hypotheses and the approach that failed.",
		previousModel, effectiveModel, reason,
	)
}

// Capability is the notice for a routing change that moved the session to a
// different model because the router raised the tier or the previous model
// became unusable for this request.
func Capability(previousModel, effectiveModel, reason string) string {
	return fmt.Sprintf(
		"Router note: this session moved from %s to %s (reason: %s). "+
			"Preserve the goals, constraints, verified facts, and working tree already established; "+
			"reassess unverified hypotheses and the approach that was not working.",
		previousModel, effectiveModel, reason,
	)
}

// Inject returns body with text appended as a trailing user turn, in the
// request's own format. The body is returned unchanged when the format is not
// supported, the JSON does not parse, the expected turn list is absent, or the
// text is empty, so a request never degrades into an invalid shape.
func Inject(sourceFormat string, body []byte, text string) []byte {
	if strings.TrimSpace(text) == "" {
		return body
	}
	var root map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil || root == nil {
		return body
	}

	key := ""
	var turn any
	switch sourceFormat {
	case "chat-completions":
		key = "messages"
		turn = map[string]any{"role": "user", "content": text}
	case "responses":
		key = "input"
		turn = map[string]any{
			"type": "message",
			"role": "user",
			"content": []any{map[string]any{
				"type": "input_text",
				"text": text,
			}},
		}
	default:
		return body
	}

	turns, ok := root[key].([]any)
	if !ok {
		return body
	}
	root[key] = append(turns, turn)

	updated, err := json.Marshal(root)
	if err != nil {
		return body
	}
	return updated
}
