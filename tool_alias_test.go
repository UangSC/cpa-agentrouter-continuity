package main

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestAliasRestoredInExecutorDelivery(t *testing.T) {
	call := `{"type":"function_call","namespace":"cpa_ar_collaboration","name":"wait_agent","call_id":"call_1","arguments":"{}"}`
	t.Run("stream", func(t *testing.T) {
		m := &mockHost{attempts: []attempt{{200, "", []string{sse(`{"type":"response.output_item.added","item":` + call + `}`), sse(`{"type":"response.completed","response":{"status":"completed","output":[` + call + `]}}`)}}}}
		withMock(t, m)
		runStreaming(context.Background(), execution{StreamID: "down", toolNamespaceAliased: true}, testCredential(), []byte(`{}`))
		wire := bytes.Join(m.emitted, nil)
		if bytes.Contains(wire, []byte(collaborationAlias)) || !bytes.Contains(wire, []byte(`"namespace":"collaboration"`)) {
			t.Fatal("stream alias leaked")
		}
	})
	t.Run("json", func(t *testing.T) {
		m := &mockHost{attempts: []attempt{{200, "", []string{`{"object":"response","status":"completed","output":[` + call + `]}`}}}}
		withMock(t, m)
		wire, err := runJSON(context.Background(), execution{toolNamespaceAliased: true}, testCredential(), []byte(`{}`))
		if err != nil || bytes.Contains(wire, []byte(collaborationAlias)) || !bytes.Contains(wire, []byte(`"namespace":"collaboration"`)) {
			t.Fatal("JSON alias not restored", err)
		}
	})
}

func TestCollaborationAliasRoundTrip(t *testing.T) {
	raw := []byte(`{"tools":[{"type":"function","name":"collaboration.wait_agent","parameters":{"type":"object","properties":{"name":{"const":"collaboration.wait_agent"}}}}],"tool_choice":{"type":"function","namespace":"collaboration","name":"wait_agent"},"input":[{"type":"additional_tools","role":"developer","tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"wait_agent","parameters":{"type":"object","properties":{"timeout_ms":{"type":"number"}}}}]}]},{"type":"function_call","namespace":"collaboration","name":"wait_agent","call_id":"unchanged","arguments":"{\"name\":\"collaboration.wait_agent\"}"},{"type":"function_call_output","call_id":"unchanged","output":"collaboration.wait_agent"},{"type":"reasoning","encrypted_content":"opaque collaboration.wait_agent"},{"role":"user","content":"Use collaboration.wait_agent"}]}`)
	aliased, changed, err := aliasRequestTools(raw)
	if err != nil || !changed {
		t.Fatalf("not aliased: %v", err)
	}
	var root map[string]json.RawMessage
	_ = json.Unmarshal(aliased, &root)
	if !bytes.Contains(root["tool_choice"], []byte(collaborationAlias)) {
		t.Fatal("tool choice not aliased")
	}
	if !bytes.Contains(aliased, []byte(`"name":"wait_agent"`)) {
		t.Fatal("unqualified function name changed")
	}
	restored, changed := rewriteToolNames(aliased, collaborationAlias, "collaboration")
	var before, after any
	_ = json.Unmarshal(raw, &before)
	_ = json.Unmarshal(restored, &after)
	if !changed || !reflect.DeepEqual(before, after) {
		t.Fatal("round trip changed data")
	}
	for _, opaque := range []string{`"const":"collaboration.wait_agent"`, `"output":"collaboration.wait_agent"`, `"encrypted_content":"opaque collaboration.wait_agent"`, `"content":"Use collaboration.wait_agent"`} {
		if !bytes.Contains(aliased, []byte(opaque)) {
			t.Fatalf("opaque data changed: %s", opaque)
		}
	}
}

func TestToolAliasCollisionAndUnrelatedPreservation(t *testing.T) {
	raw := []byte(`{"tools":[{"type":"namespace","name":"collaboration","tools":[]},{"type":"namespace","name":"cpa_ar_collaboration","tools":[]}]}`)
	if _, _, err := aliasRequestTools(raw); err == nil {
		t.Fatal("collision admitted")
	}
	raw = []byte(`{"input":[{"role":"user","content":"collaboration.wait_agent"}],"tools":[{"type":"function","name":"other"}]}`)
	out, changed, err := aliasRequestTools(raw)
	if err != nil || changed || !bytes.Equal(out, raw) {
		t.Fatal("unrelated request changed")
	}
}

func TestToolAliasSSEAcrossFragments(t *testing.T) {
	frames := []string{
		"event: response.output_item.added\r\nid: event_1\r\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"namespace\":\"cpa_ar_collaboration\",\"name\":\"wait_agent\",\"call_id\":\"call_1\",\"arguments\":\"\"}}\r\n\r\n",
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"cpa_ar_collaboration\"}\n\n",
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"function_call\",\"name\":\"cpa_ar_collaboration.wait_agent\",\"arguments\":\"{}\"}]}}\n\n",
	}
	parser := frameReader{}
	var got [][]byte
	for _, f := range frames {
		for _, ch := range []byte(f) {
			parts, err := parser.feed([]byte{ch})
			if err != nil {
				t.Fatal(err)
			}
			for _, part := range parts {
				got = append(got, restoreToolFrame(part))
			}
		}
	}
	if len(got) != 3 {
		t.Fatal("lost SSE frames")
	}
	if !bytes.Contains(got[0], []byte(`"namespace":"collaboration"`)) || !bytes.Contains(got[0], []byte("id: event_1\r\n")) {
		t.Fatal("item or SSE metadata not restored")
	}
	if string(got[1]) != frames[1] {
		t.Fatal("tool argument delta changed")
	}
	if !bytes.Contains(got[2], []byte(`"name":"collaboration.wait_agent"`)) {
		t.Fatal("completion not restored")
	}
}
