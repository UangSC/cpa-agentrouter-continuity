package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"unicode"

	"github.com/UangSC/cpa-agentrouter-continuity/internal/sdk"
)

func schedulerAdmission(q sdk.SchedulerPickRequest) (admissionRequest, bool) {
	id := http.Header(q.Options.Headers).Get(admissionHeader)
	value, ok := admissionRequests.Load(id)
	if !ok {
		return admissionRequest{}, false
	}
	r := value.(admissionRequest)
	// Some hosts resolve parent identity between interception and selection.
	// Keep the original caller scope when observing that host result.
	if parent := stringMeta(q.Options.Metadata, "parent_session_id"); parent != "" {
		parent = rawHostSession(parent)
		hash := digest(r.CallerScope + "\x00" + parent)
		if !validSessionID(parent) || parent == r.RawSession || r.ParentSession != "" && r.ParentSession != hash {
			r.ParentConflict = true
		} else {
			r.ParentSession = hash
		}
	}
	return r, true
}

func validSessionID(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// CPA v8.0.61 uses these namespaces for explicit identities. Our existing
// origin ledger stores the raw ID, so remove only a recognized namespace.
func rawHostSession(value string) string {
	value = strings.TrimSpace(value)
	for _, prefix := range []string{"codex:", "clientreq:", "header:", "session:", "thread:"} {
		if strings.HasPrefix(value, prefix) {
			return strings.TrimPrefix(value, prefix)
		}
	}
	return value
}

func codexSessionPair(headers http.Header, meta map[string]any) (child, parent string) {
	var turn map[string]any
	_ = json.Unmarshal([]byte(headers.Get("X-Codex-Turn-Metadata")), &turn)
	sid, tid := "", ""
	for _, h := range []string{"Session-Id", "Session_id", "X-Codex-Session-Id"} {
		if sid == "" {
			sid = headers.Get(h)
		}
	}
	for _, h := range []string{"Thread-Id", "Thread_id"} {
		if tid == "" {
			tid = headers.Get(h)
		}
	}
	if sid == "" {
		sid = stringMeta(turn, "session_id")
	}
	if tid == "" {
		tid = stringMeta(turn, "thread_id")
	}
	parent = headers.Get("X-Codex-Parent-Thread-Id")
	if parent == "" {
		parent = stringMeta(turn, "parent_thread_id")
	}
	for _, key := range []string{"forked_from_thread_id", "forked_from_id"} {
		if parent == "" {
			parent = stringMeta(turn, key)
		}
	}
	if tid != "" && sid != "" && tid != sid {
		child = tid
		if parent == "" {
			parent = sid
		}
	}
	// Named agents can share the parent's transport Session_id. Keep them
	// distinct using the same synthetic identity as CPA's explicit resolver.
	name := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(stringMeta(turn, "agent_name"), "/root/"), "/"))
	signal := strings.ToLower(headers.Get("X-Openai-Subagent"))
	spawn := signal != "" && signal != "false" && signal != "0" || stringMeta(turn, "subagent_kind") == "thread_spawn"
	if spawn && name != "" && name != "root" && name != "main" && sid != "" {
		child = sid + ":agent:" + name
		if parent == "" {
			parent = sid
		}
	}
	canonical := stringMeta(meta, "canonical_session_id")
	if strings.HasPrefix(canonical, "codex:") && strings.Contains(canonical, ":agent:") {
		child = rawHostSession(canonical)
	}
	return strings.TrimSpace(child), strings.TrimSpace(parent)
}

// Parse explicit references or host-resolved metadata only. Shared prompts,
// timestamps and similar text are not evidence of a parent-child relationship.
func parentSession(headers http.Header, body []byte, meta map[string]any, child string) (string, bool) {
	var root, turn map[string]any
	_ = json.Unmarshal(body, &root)
	_ = json.Unmarshal([]byte(headers.Get("X-Codex-Turn-Metadata")), &turn)
	var candidates []string
	_, inferred := codexSessionPair(headers, meta)
	if inferred != "" {
		candidates = append(candidates, inferred)
	}
	for _, header := range []string{"X-Codex-Parent-Thread-Id", "X-Parent-Session-Id", "X-Parent-Thread-Id", "X-Parent-Id"} {
		if value := headers.Get(header); value != "" {
			candidates = append(candidates, value)
		}
	}
	objects := []map[string]any{meta, root, turn}
	for _, field := range []string{"metadata", "extra_body"} {
		if value, ok := root[field].(map[string]any); ok {
			objects = append(objects, value)
		}
	}
	for _, object := range objects {
		for _, field := range []string{"parent_session_id", "parentSessionId", "parent_thread_id", "parentThreadId", "forked_from_thread_id", "forked_from_id"} {
			if value := stringMeta(object, field); value != "" {
				candidates = append(candidates, value)
			}
		}
	}
	parent := ""
	for _, value := range candidates {
		value = rawHostSession(value)
		if !validSessionID(value) || value == child {
			return "", true
		}
		if parent != "" && parent != value {
			return "", true
		}
		parent = value
	}
	if parent == "" {
		return "", false
	}
	return digest(stringMeta(meta, "caller_scope") + "\x00" + parent), false
}
