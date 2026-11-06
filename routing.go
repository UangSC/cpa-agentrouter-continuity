package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/UangSC/cpa-agentrouter-continuity/internal/sdk"
)

const admissionHeader = "X-Cpa-Agentrouter-Continuity-Admission"

type admissionRequest struct {
	ID, Session, RawSession, PreferredAuth string
	Fresh, ExplicitFresh                   bool
}

var admissionRequests sync.Map

func digest(value string) string { h := sha256.Sum256([]byte(value)); return hex.EncodeToString(h[:]) }
func stringMeta(meta map[string]any, name string) string {
	s, _ := meta[name].(string)
	return strings.TrimSpace(s)
}
func stableSession(headers http.Header, body []byte, meta map[string]any) (string, string) {
	raw := ""
	for _, name := range []string{"Session-Id", "Session_id", "X-Codex-Session-Id", "Thread-Id", "Thread_id", "X-Session-Id", "X-Claude-Code-Session-Id"} {
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
	if raw == "" {
		for _, name := range []string{"session_id", "thread_id"} {
			if s := stringMeta(root, name); s != "" {
				raw = s
				break
			}
		}
	}
	if raw == "" {
		raw = stringMeta(meta, "canonical_session_id")
	}
	if raw == "" {
		raw = stringMeta(meta, "execution_session_id")
	}
	raw = strings.TrimSpace(raw)
	if len(raw) > 256 {
		return "", ""
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", ""
		}
	}
	if raw == "" {
		return "", ""
	}
	scope := stringMeta(meta, "caller_scope")
	return digest(scope + "\x00" + raw), raw
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
func beforeAdmission(q sdk.RequestInterceptRequest) sdk.RequestInterceptResponse {
	session, raw := stableSession(q.Headers, q.Body, q.Metadata)
	r := admissionRequest{ID: q.RequestID, Session: session, RawSession: raw, Fresh: freshConversation(q.Body, q.Metadata), ExplicitFresh: stringMeta(q.Metadata, "history_initialization") == "new"}
	admissionRequests.Store(q.RequestID, r)
	// Always overwrite a client-supplied admission marker; it is never trusted.
	return sdk.RequestInterceptResponse{Headers: http.Header{admissionHeader: {q.RequestID}}}
}
func chosenRuntime(meta map[string]any) (sdk.HostAuthFileEntry, error) {
	var result sdk.HostAuthGetRuntimeResponse
	index := stringMeta(meta, "selected_auth_index")
	if index == "" {
		return result.Auth, fmt.Errorf("selected auth identity unavailable")
	}
	e := hostCall("host.auth.get_runtime", map[string]string{"auth_index": index}, &result)
	return result.Auth, e
}
func afterAdmission(q sdk.RequestInterceptRequest) sdk.RequestInterceptResponse {
	result := sdk.RequestInterceptResponse{ClearHeaders: []string{admissionHeader}}
	v, ok := admissionRequests.Load(q.RequestID)
	if !ok {
		return result
	}
	r := v.(admissionRequest)
	chosen, e := chosenRuntime(q.Metadata)
	if e != nil {
		return result
	}
	if chosen.Provider == pluginID {
		return sdk.RequestInterceptResponse{}
	}
	if chosen.Provider != pluginID && r.Session != "" {
		_ = withRoutingLedger(func(s *routingLedger) error {
			s.Origins[r.Session] = sessionOrigin{Provider: chosen.Provider, AuthID: chosen.ID, Foreign: true}
			delete(s.Leases, r.ID)
			return nil
		})
	}
	return result
}
func completeAdmission(q sdk.RequestCompletion) {
	admissionRequests.Delete(q.RequestID)
	_ = withRoutingLedger(func(s *routingLedger) error { delete(s.Leases, q.RequestID); return nil })
}

func affinityCandidate(q sdk.SchedulerPickRequest, r admissionRequest) (string, string) {
	if r.RawSession == "" {
		return "", ""
	}
	provider := q.Provider
	if provider == "" {
		provider = "mixed"
	}
	var lookup sdk.HostAffinityLookupResponse
	e := hostCall("host.affinity.lookup", map[string]string{"provider": provider, "model": q.Model, "session_id": r.RawSession}, &lookup)
	if e != nil || lookup.Status != "bound" {
		return "", ""
	}
	var runtime sdk.HostAuthGetRuntimeResponse
	if hostCall("host.auth.get_runtime", map[string]string{"auth_index": lookup.AuthIndex}, &runtime) != nil {
		return "", ""
	}
	return runtime.Auth.ID, runtime.Auth.Provider
}
func keyForCandidate(c sdk.SchedulerAuthCandidate) string { return c.Attributes["ar_key_hash"] }
func limitForCandidate(c sdk.SchedulerAuthCandidate) int {
	limit, err := strconv.Atoi(c.Attributes["ar_max_concurrent_sessions"])
	if err != nil || limit < 1 || limit > maximumConcurrentSessions {
		return defaultMaxConcurrentSessions
	}
	return limit
}

func keySessions(s *routingLedger, key string) map[string]bool {
	sessions := map[string]bool{}
	for _, lease := range s.Leases {
		if lease.KeyHash == key {
			sessions[lease.Session] = true
		}
	}
	return sessions
}

func availableSession(s *routingLedger, key, session string, limit int) bool {
	if key == "" || session == "" {
		return false
	}
	sessions := keySessions(s, key)
	return sessions[session] || len(sessions) < limit
}
func chooseWeighted(candidates []sdk.SchedulerAuthCandidate) sdk.SchedulerAuthCandidate {
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	priority := candidates[0].Priority
	for _, c := range candidates {
		if c.Priority > priority {
			priority = c.Priority
		}
	}
	filtered := []sdk.SchedulerAuthCandidate{}
	weights := []int{}
	total := 0
	for _, c := range candidates {
		if c.Priority != priority {
			continue
		}
		w, e := strconv.Atoi(c.Attributes["weight"])
		if e != nil || w < 1 {
			w = 1
		}
		if w > 1000000 {
			w = 1000000
		}
		filtered = append(filtered, c)
		weights = append(weights, w)
		total += w
	}
	roll := rand.Intn(total)
	for i, w := range weights {
		roll -= w
		if roll < 0 {
			return filtered[i]
		}
	}
	return filtered[0]
}
func rejectAdmission(reason string) sdk.SchedulerPickResponse {
	return sdk.SchedulerPickResponse{Handled: true, Reject: true, RejectCode: "ar_session_ineligible", RejectReason: reason}
}
func pickAdmission(q sdk.SchedulerPickRequest) sdk.SchedulerPickResponse {
	hasAR := false
	for _, c := range q.Candidates {
		if c.Provider == pluginID {
			hasAR = true
		}
	}
	if !hasAR {
		id := http.Header(q.Options.Headers).Get(admissionHeader)
		if value, ok := admissionRequests.Load(id); ok {
			r := value.(admissionRequest)
			if r.Session != "" {
				_ = withRoutingLedger(func(s *routingLedger) error {
					s.Origins[r.Session] = sessionOrigin{Provider: "external", Foreign: true}
					delete(s.Leases, id)
					return nil
				})
			}
		}
		return sdk.SchedulerPickResponse{}
	}
	id := http.Header(q.Options.Headers).Get(admissionHeader)
	value, ok := admissionRequests.Load(id)
	r := admissionRequest{}
	if ok {
		r = value.(admissionRequest)
	}
	preferred, provider := affinityCandidate(q, r)
	result := rejectAdmission("AR requires a new identified conversation and a free session slot")
	e := withRoutingLedger(func(s *routingLedger) error {
		origin, known := s.Origins[r.Session]
		if provider != "" && provider != pluginID && r.Session != "" {
			origin = sessionOrigin{Provider: provider, AuthID: preferred, Foreign: true}
			s.Origins[r.Session] = origin
			known = true
		}
		// A retry after an earlier failed first turn can leave a foreign ledger
		// entry behind. Codex explicitly marks a genuinely new initialization;
		// permit that one clean first turn to replace the stale marker. A cold
		// resume, assistant history, tool result or encrypted item remains out.
		eligible := ok && r.ID != "" && r.Session != "" && ((known && !origin.Foreign && origin.Provider == pluginID) || (!known && r.Fresh) || (known && origin.Foreign && r.Fresh && r.ExplicitFresh))
		allowed := []sdk.SchedulerAuthCandidate{}
		for _, candidate := range q.Candidates {
			if candidate.Provider == pluginID {
				if !eligible || !availableSession(s, keyForCandidate(candidate), r.Session, limitForCandidate(candidate)) {
					continue
				}
				if known && origin.KeyHash != "" && origin.KeyHash != keyForCandidate(candidate) {
					continue
				}
			}
			allowed = append(allowed, candidate)
		}
		if len(allowed) == 0 {
			return nil
		}
		var chosen sdk.SchedulerAuthCandidate
		// Preserve existing affinity where eligible, before considering candidate priority.
		sticky := preferred
		if sticky == "" && known {
			sticky = origin.AuthID
		}
		for _, c := range allowed {
			if c.ID == sticky {
				chosen = c
				break
			}
		}
		if chosen.ID == "" {
			chosen = chooseWeighted(allowed)
		}
		if r.Session != "" {
			if chosen.Provider == pluginID {
				if !known || (origin.Foreign && r.ExplicitFresh) {
					s.Origins[r.Session] = sessionOrigin{Provider: pluginID, AuthID: chosen.ID, KeyHash: keyForCandidate(chosen)}
				}
				s.Leases[id] = admissionLease{Session: r.Session, KeyHash: keyForCandidate(chosen), AuthID: chosen.ID, RequestID: id, Created: time.Now().UTC()}
			} else {
				s.Origins[r.Session] = sessionOrigin{Provider: chosen.Provider, AuthID: chosen.ID, Foreign: true}
				delete(s.Leases, id)
			}
		}
		result = sdk.SchedulerPickResponse{Handled: true, AuthID: chosen.ID}
		return nil
	})
	if e != nil {
		// A broken origin ledger must never allow an old conversation into AR.
		others := []sdk.SchedulerAuthCandidate{}
		for _, c := range q.Candidates {
			if c.Provider != pluginID {
				others = append(others, c)
			}
		}
		if len(others) > 0 {
			return sdk.SchedulerPickResponse{Handled: true, AuthID: chooseWeighted(others).ID}
		}
		return rejectAdmission("AR session admission state unavailable")
	}
	return result
}
func acquireExecution(q execution, c credential) (func(), error) {
	session, _ := stableSession(q.Headers, q.OriginalRequest, q.Metadata)
	// After-auth strips the transport marker; recover the request from its reserved lease.
	requestID := ""
	e := withRoutingLedger(func(s *routingLedger) error {
		id := q.Headers.Get(admissionHeader)
		lease, ok := s.Leases[id]
		if ok && lease.Session == session && lease.AuthID == q.AuthID && lease.KeyHash == digest(c.APIKey) && !lease.Running {
			requestID = id
			lease.Running = true
			s.Leases[id] = lease
			return nil
		}
		return fmt.Errorf("no AR session reservation")
	})
	if e != nil {
		return nil, &rpcError{"ar_session_ineligible", "This conversation is not admitted to AR. Start a new conversation or use its original provider.", 409}
	}
	return func() {
		_ = withRoutingLedger(func(s *routingLedger) error { delete(s.Leases, requestID); return nil })
	}, nil
}
func routingSnapshot() map[string]any {
	result := map[string]any{"limit": defaultMaxConcurrentSessions, "default_limit": defaultMaxConcurrentSessions, "policy": "ar-origin-only", "active_sessions": map[string]int{}, "state": "ready"}
	e := withRoutingLedger(func(s *routingLedger) error {
		perKey := map[string]map[string]bool{}
		for _, lease := range s.Leases {
			if perKey[lease.KeyHash] == nil {
				perKey[lease.KeyHash] = map[string]bool{}
			}
			perKey[lease.KeyHash][lease.Session] = true
		}
		counts := map[string]int{}
		for _, lease := range s.Leases {
			counts[lease.AuthID] = len(perKey[lease.KeyHash])
		}
		result["active_sessions"] = counts
		result["known_sessions"] = len(s.Origins)
		return nil
	})
	if e != nil {
		result["state"] = "unavailable"
	}
	return result
}
