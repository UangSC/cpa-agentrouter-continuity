package main

import (
	"net/http"
	"testing"

	"github.com/UangSC/cpa-agentrouter-continuity/internal/sdk"
)

func TestDesktopAdditionalToolsAdmission(t *testing.T) {
	routeFixture(t)
	body := []byte(`{"input":[{"type":"additional_tools","id":"tools_1","role":"developer","tools":[{"type":"function","name":"example","parameters":{"type":"object"}}]},{"type":"message","role":"developer","content":[{"type":"input_text","text":"Instructions"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`)
	if !freshConversation(body, nil) {
		t.Fatal("desktop tool definitions incorrectly classified as conversation history")
	}
	headers := http.Header{"Session-Id": {"desktop-new-session"}}
	r := beforeAdmission(sdk.RequestInterceptRequest{RequestID: "desktop-first", Headers: headers, Body: body})
	headers.Set(admissionHeader, r.Headers.Get(admissionHeader))
	picked := pickAdmission(sdk.SchedulerPickRequest{Model: "test-model", Options: sdk.SchedulerOptions{Headers: headers}, Candidates: []sdk.SchedulerAuthCandidate{arCandidate("ar", "mock-secret", 15), otherCandidate()}})
	if picked.AuthID != "ar" || picked.Reject {
		t.Fatalf("desktop first turn did not select AR: %+v", picked)
	}
}

func TestAdditionalToolsCannotHideHistory(t *testing.T) {
	for _, raw := range []string{
		`{"input":[{"type":"additional_tools","role":"assistant","tools":[]},{"role":"user","content":"hello"}]}`,
		`{"input":[{"type":"additional_tools","role":"developer","tools":[],"encrypted_content":"old"},{"role":"user","content":"hello"}]}`,
		`{"input":[{"type":"additional_tools","role":"developer","tools":[],"call_id":"call_old"},{"role":"user","content":"hello"}]}`,
		`{"input":[{"type":"additional_tools","role":"developer","tools":[]},{"role":"assistant","content":"old answer"},{"role":"user","content":"hello"}]}`,
		`{"input":[{"type":"additional_tools","role":"developer","tools":[]},{"type":"function_call_output","call_id":"old","output":"result"},{"role":"user","content":"hello"}]}`,
		`{"input":[{"type":"additional_tools","role":"developer","tools":[]}]}`,
		`{"input":[{"type":"unknown_item","role":"developer"},{"role":"user","content":"hello"}]}`,
	} {
		if freshConversation([]byte(raw), nil) {
			t.Fatalf("history or unknown item admitted: %s", raw)
		}
	}
}
