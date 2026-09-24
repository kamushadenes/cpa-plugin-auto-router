package session

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/chloeassistant/cpa-plugin-auto-router/internal/decide"
	"github.com/tidwall/gjson"
)

type entry struct {
	state decide.State
	at    time.Time
}

type Store struct {
	mu      sync.Mutex
	entries map[string]entry
	ttl     time.Duration
	max     int
}

func New(ttl time.Duration, max int) *Store {
	return &Store{
		entries: make(map[string]entry),
		ttl:     ttl,
		max:     max,
	}
}

func (s *Store) Get(id string) (decide.State, bool) {
	if id == "" {
		return decide.State{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.entries[id]
	if !ok {
		return decide.State{}, false
	}
	if s.ttl > 0 && time.Since(e.at) >= s.ttl {
		delete(s.entries, id)
		return decide.State{}, false
	}
	return e.state, true
}

func (s *Store) Put(id string, state decide.State) {
	if id == "" || s.max <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	s.entries[id] = entry{state: state, at: time.Now()}
	// ponytail: O(n) eviction, fine at 65k; use a heap only if profiling requires it.
	for len(s.entries) > s.max {
		oldestID := ""
		var oldest time.Time
		for candidateID, candidate := range s.entries {
			if oldestID == "" || candidate.at.Before(oldest) || (candidate.at.Equal(oldest) && candidateID < oldestID) {
				oldestID = candidateID
				oldest = candidate.at
			}
		}
		delete(s.entries, oldestID)
	}
}

// ID mirrors the host's explicit identity order, then the body identities, and
// finally hashes only the first user message. It never hashes system or tool text.
func ID(headers http.Header, body []byte) string {
	if value := headerValue(headers, "X-Claude-Code-Session-Id"); value != "" {
		return value
	}

	root := gjson.ParseBytes(body)
	if value := claudeMetadataSession(root.Get("metadata.user_id").String()); value != "" {
		return value
	}
	for _, name := range []string{
		"Session-Id",
		"Session_id",
		"X-Http-Session-Id",
		"X-Session-ID",
		"X-Session-Affinity",
		"X-Client-Request-Id",
	} {
		if value := headerValue(headers, name); value != "" {
			return value
		}
	}
	if len(body) == 0 {
		return ""
	}

	for _, path := range []string{"session_id", "sessionId", "sessionID"} {
		if value := bodyCandidate(root, path); value != "" {
			return value
		}
	}
	for _, path := range []string{"prompt_cache_key", "promptCacheKey"} {
		if value := bodyCandidate(root, path); value != "" {
			return value
		}
	}
	conversation := bodyValue(root, "conversation")
	if value := cleanCandidate(conversation.Get("id").String()); value != "" {
		return value
	}
	if conversation.Type == gjson.String {
		if value := cleanCandidate(conversation.String()); value != "" {
			return value
		}
	}
	if value := bodyCandidate(root, "metadata.user_id"); value != "" {
		return value
	}
	for _, path := range []string{"conversation_id", "conversationId", "chat_id", "chatId"} {
		if value := bodyCandidate(root, path); value != "" {
			return value
		}
	}

	if text := firstUserText(root); text != "" {
		hash := sha256.Sum256([]byte(text))
		return "h:" + hex.EncodeToString(hash[:8])
	}
	return ""
}

func headerValue(headers http.Header, name string) string {
	for key, values := range headers {
		if !strings.EqualFold(key, name) {
			continue
		}
		for _, value := range values {
			if value = cleanCandidate(value); value != "" {
				return value
			}
		}
	}
	return ""
}

func bodyValue(root gjson.Result, path string) gjson.Result {
	value := root.Get(path)
	if value.Exists() {
		return value
	}
	request := root.Get("request")
	if request.Exists() {
		return request.Get(path)
	}
	return value
}

func bodyCandidate(root gjson.Result, path string) string {
	return cleanCandidate(bodyValue(root, path).String())
}

func claudeMetadataSession(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "{") {
		return cleanCandidate(gjson.Parse(value).Get("session_id").String())
	}
	if strings.HasPrefix(value, "user_hash_") {
		parts := strings.Split(value, "_")
		if len(parts) >= 4 {
			return cleanCandidate(parts[len(parts)-1])
		}
	}
	return ""
}

func cleanCandidate(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return ""
	}
	return value
}

func firstUserText(root gjson.Result) string {
	messages := bodyValue(root, "messages")
	if messages.IsArray() {
		for _, message := range messages.Array() {
			if message.Get("role").String() != "user" {
				continue
			}
			if text := strings.TrimSpace(messageText(message.Get("content"))); text != "" {
				return text
			}
		}
	}

	input := bodyValue(root, "input")
	if input.Type == gjson.String {
		return strings.TrimSpace(input.String())
	}
	if input.IsArray() {
		for _, item := range input.Array() {
			if item.Get("role").String() != "user" {
				continue
			}
			if text := strings.TrimSpace(messageText(item.Get("content"))); text != "" {
				return text
			}
		}
	}
	return ""
}

func messageText(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	if !content.IsArray() {
		return ""
	}
	parts := make([]string, 0)
	for _, part := range content.Array() {
		typeName := part.Get("type").String()
		if typeName != "" && typeName != "text" && typeName != "input_text" {
			continue
		}
		if text := strings.TrimSpace(part.Get("text").String()); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}
