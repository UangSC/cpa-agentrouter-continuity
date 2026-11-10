package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestPublicErrorsPreserveDiagnosticsAndInternalCode(t *testing.T) {
	original := &rpcError{Code: "ar_upstream_timeout", Message: "AR AgentRouter request to https://agentrouter.org/v1/responses: TLS handshake timeout [trace_id=AR-trace-17]", HTTPStatus: 504}
	raw, err := json.Marshal(envelope{Error: original})
	if err != nil {
		t.Fatal(err)
	}
	var result envelope
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Error.Code != "channel_upstream_timeout" || result.Error.HTTPStatus != 504 {
		t.Fatal(string(raw))
	}
	for _, value := range []string{"渠道", "TLS handshake timeout", "/v1/responses", "trace_id=AR-trace-17"} {
		if !strings.Contains(result.Error.Message, value) {
			t.Fatal("diagnostic missing", value)
		}
	}
	withoutTrace := strings.ReplaceAll(result.Error.Message, "trace_id=AR-trace-17", "")
	if strings.Contains(withoutTrace, "AgentRouter") || strings.Contains(withoutTrace, "AR") || strings.Contains(withoutTrace, "agentrouter.org") {
		t.Fatal(result.Error.Message)
	}
	if original.Code != "ar_upstream_timeout" || !strings.Contains(original.Message, "AgentRouter") {
		t.Fatal("internal error was mutated")
	}
	if channelErrorMessage("PARSER parameter quota_error") != "PARSER parameter quota_error" {
		t.Fatal("unrelated identifiers changed")
	}
	pick := rejectAdmission("AR 会话来源未知 [trace_id=trace-1]")
	if pick.RejectCode != "channel_session_ineligible" || pick.RejectReason != "渠道 会话来源未知 [trace_id=trace-1]" {
		t.Fatal(pick)
	}
}

func TestStreamErrorPresentationKeepsReasonAndRedactsFirst(t *testing.T) {
	c := testCredential()
	zero := 0
	c.MaxRetries = &zero
	body := `{"error":{"code":"ar_provider_failure","message":"AgentRouter AR failed: invalid_api_key mock-secret [trace_id=trace-42]"}}`
	m := &mockHost{attempts: []attempt{{401, body, nil}}}
	withMock(t, m)
	runStreaming(context.Background(), execution{StreamID: "down"}, c, []byte(`{}`))
	wire := string(bytes.Join(m.emitted, nil))
	for _, value := range []string{"channel_provider_failure", "invalid_api_key", "trace-42", "401", "[REDACTED]"} {
		if !strings.Contains(wire, value) {
			t.Fatal("missing detail", value, wire)
		}
	}
	for _, value := range []string{"AgentRouter", "mock-", "ar_provider_failure"} {
		if strings.Contains(wire, value) {
			t.Fatal("unexpected error text", wire)
		}
	}
	brandedSecretBody := strings.ReplaceAll(body, "mock-secret", "mock-AR-secret")
	raw, err := json.Marshal(safeError(401, []byte(brandedSecretBody), "mock-AR-secret"))
	if err != nil || bytes.Contains(raw, []byte("mock-")) || !bytes.Contains(raw, []byte("[REDACTED]")) {
		t.Fatal("credential normalization preceded redaction", string(raw), err)
	}
}

func TestSuccessfulOutputIsNotRewritten(t *testing.T) {
	m := &mockHost{attempts: []attempt{{200, "", []string{sse(`{"type":"response.output_text.delta","delta":"AR AgentRouter https://agentrouter.org"}`), sse(completed)}}}}
	withMock(t, m)
	runStreaming(context.Background(), execution{StreamID: "down"}, testCredential(), []byte(`{}`))
	if !bytes.Contains(bytes.Join(m.emitted, nil), []byte("AR AgentRouter https://agentrouter.org")) {
		t.Fatal("successful model content rewritten")
	}
}
