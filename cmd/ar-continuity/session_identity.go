package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
)

func digest(value string) string { h := sha256.Sum256([]byte(value)); return hex.EncodeToString(h[:]) }
func stringMeta(meta map[string]any, name string) string {
	s, _ := meta[name].(string)
	return strings.TrimSpace(s)
}
func stableSession(headers http.Header, body []byte, meta map[string]any) (string, string) {
	raw, _ := codexSessionPair(headers, meta)
	for _, name := range []string{"Session-Id", "Session_id", "X-Codex-Session-Id", "Thread-Id", "Thread_id", "X-Session-Id", "X-Claude-Code-Session-Id"} {
		if raw != "" {
			break
		}
		if s := headers.Get(name); s != "" {
			raw = s
			break
		}
	}
	if raw == "" {
		var turn map[string]any
		_ = json.Unmarshal([]byte(headers.Get("X-Codex-Turn-Metadata")), &turn)
		raw = stringMeta(turn, "session_id")
	}
	var root map[string]any
	_ = json.Unmarshal(body, &root)
	// DSH's generic Responses SDK carries the conversation ID in these two
	// fields. X-Client-Request-Id is only a request ID for other clients, so do
	// not treat it as a conversation identity outside this adapter contract.
	if raw == "" && strings.HasPrefix(strings.ToLower(strings.TrimSpace(headers.Get("User-Agent"))), "deepseek-harness/") {
		requestSession := strings.TrimSpace(headers.Get("X-Client-Request-Id"))
		cacheSession := stringMeta(root, "prompt_cache_key")
		if requestSession != "" && cacheSession != "" && requestSession != cacheSession {
			return "", ""
		}
		raw = requestSession
		if raw == "" {
			raw = cacheSession
		}
	}
	if raw == "" {
		objects := []map[string]any{root}
		if metadata, ok := root["metadata"].(map[string]any); ok {
			objects = append(objects, metadata)
		}
		for _, object := range objects {
			for _, name := range []string{"session_id", "thread_id"} {
				if raw == "" {
					raw = stringMeta(object, name)
				}
			}
		}
	}
	if raw == "" {
		canonical := stringMeta(meta, "canonical_session_id")
		if !strings.HasPrefix(canonical, "execution:") {
			raw = rawHostSession(canonical)
		}
	}
	raw = strings.TrimSpace(raw)
	if !validSessionID(raw) {
		return "", ""
	}
	scope := stringMeta(meta, "caller_scope")
	return digest(scope + "\x00" + raw), raw
}

// Invalid/conflicting identifiers must not be converted into anonymous probes.
func hasSessionHint(headers http.Header, body []byte, meta map[string]any) bool {
	for _, name := range []string{"Session-Id", "Session_id", "X-Codex-Session-Id", "Thread-Id", "Thread_id", "X-Session-Id", "X-Claude-Code-Session-Id", "X-Codex-Turn-Metadata"} {
		if headers.Get(name) != "" {
			return true
		}
	}
	var root map[string]any
	_ = json.Unmarshal(body, &root)
	if strings.HasPrefix(strings.ToLower(headers.Get("User-Agent")), "deepseek-harness/") && (headers.Get("X-Client-Request-Id") != "" || root["prompt_cache_key"] != nil) {
		return true
	}
	objects := []map[string]any{root}
	if metadata, ok := root["metadata"].(map[string]any); ok {
		objects = append(objects, metadata)
	}
	for _, object := range objects {
		for _, key := range []string{"session_id", "thread_id"} {
			if value, exists := object[key]; exists && value != nil {
				return true
			}
		}
	}
	canonical := stringMeta(meta, "canonical_session_id")
	return canonical != "" && !strings.HasPrefix(canonical, "execution:")
}
func freshConversation(body []byte, meta map[string]any) bool {
	if meta["is_fork"] == true || meta["is_compaction"] == true || stringMeta(meta, "parent_session_id") != "" {
		return false
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil {
		return false
	}
	for _, name := range []string{"previous_response_id", "conversation", "parent_session_id"} {
		if v := root[name]; len(v) > 0 && string(v) != "null" && string(v) != "\"\"" {
			return false
		}
	}
	var text string
	if json.Unmarshal(root["input"], &text) == nil {
		return strings.TrimSpace(text) != ""
	}
	input := root["input"]
	if len(input) == 0 {
		input = root["messages"]
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(input, &items) != nil || len(items) == 0 {
		return false
	}
	user := false
	for _, item := range items {
		var role, kind string
		_ = json.Unmarshal(item["role"], &role)
		_ = json.Unmarshal(item["type"], &kind)
		if len(item["encrypted_content"]) > 0 || len(item["call_id"]) > 0 || len(item["tool_calls"]) > 0 {
			return false
		}
		// Codex Desktop sends tool definitions as a developer input item even
		// on its first turn. These definitions are not assistant/tool history.
		if kind == "additional_tools" {
			if role != "developer" && role != "system" {
				return false
			}
			for name := range item {
				if name != "type" && name != "id" && name != "role" && name != "tools" {
					return false
				}
			}
			var tools []map[string]json.RawMessage
			if json.Unmarshal(item["tools"], &tools) != nil || tools == nil {
				return false
			}
			continue
		}
		if kind != "" && kind != "message" {
			return false
		}
		switch role {
		case "user":
			user = true
		case "system", "developer":
		default:
			return false
		}
	}
	return user
}
