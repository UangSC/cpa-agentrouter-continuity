package main

import (
	"bytes"
	"encoding/json"
	"strings"
)

const collaborationAlias = "cpa_ar_collaboration"

// Only protocol tool identifiers are rewritten. Text, schemas, arguments,
// call IDs, results and encrypted reasoning remain opaque.
func rewriteToolNames(raw []byte, from, to string) ([]byte, bool) {
	var list []json.RawMessage
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '[' && json.Unmarshal(raw, &list) == nil {
		changed := false
		for i := range list {
			v, ok := rewriteToolNames(list[i], from, to)
			if ok {
				list[i], changed = v, true
			}
		}
		if changed {
			out, _ := json.Marshal(list)
			return out, true
		}
		return raw, false
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return raw, false
	}
	var kind string
	_ = json.Unmarshal(obj["type"], &kind)
	changed := false
	rename := func(field string, qualified bool) {
		var name string
		if json.Unmarshal(obj[field], &name) != nil {
			return
		}
		updated := name
		if !qualified && name == from {
			updated = to
		}
		if qualified && strings.HasPrefix(name, from+".") {
			updated = to + strings.TrimPrefix(name, from)
		}
		if updated != name {
			obj[field], _ = json.Marshal(updated)
			changed = true
		}
	}
	switch kind {
	case "namespace":
		rename("name", false)
	case "function", "function_call", "custom", "custom_tool_call":
		rename("namespace", false)
		rename("name", true)
	}
	for _, field := range []string{"tools", "input", "output", "item", "response", "tool_choice"} {
		if v, ok := rewriteToolNames(obj[field], from, to); ok {
			obj[field], changed = v, true
		}
	}
	if changed {
		out, _ := json.Marshal(obj)
		return out, true
	}
	return raw, false
}

func aliasRequestTools(raw []byte) ([]byte, bool, error) {
	// Avoid ambiguity with a client that already defines our upstream alias.
	_, collision := rewriteToolNames(raw, collaborationAlias, "collision_check")
	out, changed := rewriteToolNames(raw, "collaboration", collaborationAlias)
	if collision && changed {
		return nil, false, &rpcError{"ar_tool_alias_conflict", "工具命名空间与渠道兼容映射冲突", 400}
	}
	return out, changed, nil
}

func restoreToolFrame(frame []byte) []byte {
	data, changed := rewriteToolNames(eventData(frame), collaborationAlias, "collaboration")
	if !changed {
		return frame
	}
	// Preserve event/id/retry/comment fields and the existing line endings.
	newline := []byte("\n")
	if bytes.Contains(frame, []byte("\r\n")) {
		newline = []byte("\r\n")
	}
	lines := bytes.Split(frame, newline)
	out := make([][]byte, 0, len(lines))
	written := false
	for _, line := range lines {
		if bytes.HasPrefix(line, []byte("data:")) {
			if !written {
				out = append(out, append([]byte("data: "), data...))
				written = true
			}
		} else {
			out = append(out, line)
		}
	}
	return bytes.Join(out, newline)
}
