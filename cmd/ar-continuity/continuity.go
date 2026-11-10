package main

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"
)

const continuityTTL = 24 * time.Hour
const continuityLimit = 8192

type continuityEntry struct {
	Session   string        `json:"session"`
	Origin    sessionOrigin `json:"origin"`
	Expires   time.Time     `json:"expires"`
	Ambiguous bool          `json:"ambiguous,omitempty"`
}

var continuityStoreMu sync.Mutex

func withContinuity(fn func(*routingLedger) error) error {
	continuityStoreMu.Lock()
	defer continuityStoreMu.Unlock()
	return withLedgerFile(routingPath+".continuity", func(s *routingLedger) error {
		if s.References == nil {
			s.References = map[string]continuityEntry{}
		}
		now := time.Now()
		for key, entry := range s.References {
			if !entry.Expires.After(now) {
				delete(s.References, key)
			}
		}
		return fn(s)
	})
}

type responseReference struct{ kind, value string }

func referenceHash(scope string, ref responseReference) string {
	return digest("ar-output-v1\x00" + scope + "\x00" + ref.kind + "\x00" + ref.value)
}

// Only protocol identities returned by AR are evidence. Do not inspect tool
// arguments/results or infer ownership from matching prompts or assistant text.
func protocolReferences(raw []byte, output bool) []responseReference {
	var root map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil {
		return nil
	}
	refs := []responseReference{}
	add := func(kind string, raw json.RawMessage) {
		var value string
		if json.Unmarshal(raw, &value) != nil || value == "" || len(value) > 1<<20 {
			return
		}
		if kind != "encrypted" && !validSessionID(value) {
			return
		}
		if len(refs) < 256 {
			refs = append(refs, responseReference{kind, value})
		}
	}
	var item func(map[string]json.RawMessage)
	item = func(value map[string]json.RawMessage) {
		var role string
		_ = json.Unmarshal(value["role"], &role)
		if !output && (role == "user" || role == "system" || role == "developer") {
			return
		}
		add("item", value["id"])
		add("call", value["call_id"])
		add("call", value["tool_call_id"])
		add("encrypted", value["encrypted_content"])
		var calls []map[string]json.RawMessage
		if json.Unmarshal(value["tool_calls"], &calls) == nil {
			for _, call := range calls {
				add("call", call["id"])
			}
		}
	}
	if output {
		var kind string
		_ = json.Unmarshal(root["type"], &kind)
		if strings.HasPrefix(kind, "response.") {
			if kind != "response.created" && kind != "response.output_item.added" && kind != "response.output_item.done" && kind != "response.completed" && kind != "response.incomplete" {
				return nil
			}
		}
		var response map[string]json.RawMessage
		if json.Unmarshal(root["response"], &response) == nil && response != nil {
			root = response
		} else {
			var value map[string]json.RawMessage
			if json.Unmarshal(root["item"], &value) == nil && value != nil {
				item(value)
				return refs
			}
		}
		add("response", root["id"])
	} else {
		add("response", root["previous_response_id"])
	}
	fields := []string{"input", "messages"}
	if output {
		fields = []string{"output"}
	}
	for _, field := range fields {
		var items []map[string]json.RawMessage
		if json.Unmarshal(root[field], &items) == nil {
			// Prefer recent history when a very long input exceeds the bounded
			// reference scan. Old references may already have expired.
			for i := len(items) - 1; i >= 0; i-- {
				item(items[i])
			}
		}
	}
	return refs
}

func lookupContinuity(scope string, body []byte) (*continuityEntry, bool) {
	if scope == "" {
		return nil, false
	}
	refs := protocolReferences(body, false)
	if len(refs) == 0 {
		return nil, false
	}
	var found *continuityEntry
	conflict := false
	err := withContinuity(func(s *routingLedger) error {
		matched := []string{}
		for _, ref := range refs {
			key := referenceHash(scope, ref)
			entry, ok := s.References[key]
			if !ok {
				continue
			}
			if entry.Ambiguous || found != nil && found.Origin.KeyHash != entry.Origin.KeyHash {
				conflict = true
				break
			}
			copy := entry
			found = &copy
			matched = append(matched, key)
		}
		if !conflict {
			for _, key := range matched {
				entry := s.References[key]
				entry.Expires = time.Now().UTC().Add(continuityTTL)
				s.References[key] = entry
			}
		}
		return nil
	})
	if err != nil {
		return nil, true
	}
	return found, conflict
}

type continuityRecorder struct {
	scope, session string
	seen           map[string]bool
	required       bool
}

func newContinuityRecorder(q execution, c credential) *continuityRecorder {
	value, ok := admissionRequests.Load(q.Headers.Get(admissionHeader))
	if !ok {
		return nil
	}
	r := value.(admissionRequest)
	if r.CallerScope == "" {
		return nil
	}
	session := r.Session
	if r.Recovered != nil && r.Recovered.Origin.KeyHash == digest(c.APIKey) {
		session = r.Recovered.Session
	}
	return &continuityRecorder{scope: r.CallerScope, session: session, seen: map[string]bool{}, required: r.Transient || r.Recovered != nil}
}

func (r *continuityRecorder) remember(c credential, raw []byte) error {
	if r == nil {
		return nil
	}
	refs := protocolReferences(raw, true)
	keys := []string{}
	for _, ref := range refs {
		key := referenceHash(r.scope, ref)
		if !r.seen[key] {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	err := withContinuity(func(s *routingLedger) error {
		entry := continuityEntry{Session: r.session, Origin: sessionOrigin{Provider: pluginID, AuthID: c.ID, KeyHash: digest(c.APIKey)}, Expires: time.Now().UTC().Add(continuityTTL)}
		for _, key := range keys {
			value := entry
			if old, ok := s.References[key]; ok && (old.Ambiguous || old.Origin.KeyHash != entry.Origin.KeyHash) {
				value.Ambiguous = true
			}
			s.References[key] = value
		}
		if len(s.References) > continuityLimit {
			all := make([]string, 0, len(s.References))
			for key := range s.References {
				all = append(all, key)
			}
			sort.Slice(all, func(i, j int) bool { return s.References[all[i]].Expires.Before(s.References[all[j]].Expires) })
			for _, key := range all[:len(all)-continuityLimit] {
				delete(s.References, key)
			}
		}
		return nil
	})
	if err != nil {
		if !r.required {
			return nil
		}
		return &rpcError{"ar_continuity_state_unavailable", "无法保存本次响应的会话关联，请稍后重试。", 503}
	}
	for _, key := range keys {
		r.seen[key] = true
	}
	return nil
}
