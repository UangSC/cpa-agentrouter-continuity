package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

const historyFailureBody = `{"error":{"code":"invalid_encrypted_content","message":"The encrypted content for item rs_keep could not be verified. [trace_id=history-trace]"}}`

func TestHistoryRecoversInternallyWithoutIntermediateErrors(t *testing.T) {
	for _, mode := range []string{"http", "sse", "json"} {
		t.Run(mode, func(t *testing.T) {
			bad := attempt{400, historyFailureBody, nil}
			good := attempt{200, "", []string{sse(`{"type":"response.output_text.delta","delta":"SUCCESS"}`), sse(completed)}}
			if mode == "sse" {
				bad = attempt{200, "", []string{sse(`{"type":"response.created"}`), sse(`{"type":"response.failed","response":` + historyFailureBody + `}`)}}
			}
			if mode == "json" {
				good = attempt{200, "", []string{`{"status":"completed","output":[]}`}}
			}
			m := &mockHost{attempts: []attempt{bad, bad, good}}
			withMock(t, m)
			body := []byte(`{"input":[{"type":"reasoning","id":"rs_keep","encrypted_content":"opaque"},{"type":"function_call","call_id":"keep","name":"read_file","arguments":"{}"}]}`)
			if mode == "json" {
				payload, err := runJSON(context.Background(), execution{}, testCredential(), body)
				if err != nil || !bytes.Contains(payload, []byte("completed")) {
					t.Fatal(string(payload), err)
				}
			} else {
				runStreaming(context.Background(), execution{StreamID: "down"}, testCredential(), body)
				wire := bytes.Join(m.emitted, nil)
				if bytes.Count(wire, []byte("response.completed")) != 1 || bytes.Contains(wire, []byte("response.failed")) || bytes.Contains(wire, []byte("history-trace")) {
					t.Fatalf("intermediate error escaped: %s", wire)
				}
			}
			if m.started != 3 || m.closed != 3 {
				t.Fatalf("attempts/closed = %d/%d", m.started, m.closed)
			}
			for _, sent := range m.bodies {
				if !bytes.Equal(sent, body) {
					t.Fatal("request history was changed during recovery")
				}
			}
		})
	}
}

func TestHistoryRetriesRespectPerKeyBudgetAndEmitOnlyFinalError(t *testing.T) {
	for _, limit := range []int{0, 1, 2, 4, 10} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			bad := attempt{400, historyFailureBody, nil}
			m := &mockHost{attempts: []attempt{bad, bad, bad}}
			withMock(t, m)
			c := testCredential()
			c.MaxRetries = &limit
			runStreaming(context.Background(), execution{StreamID: "down"}, c, []byte(`{}`))
			if want := 1 + min(limit, 2); m.started != want || m.closed != want {
				t.Fatalf("got %d/%d want %d", m.started, m.closed, want)
			}
			wire := bytes.Join(m.emitted, nil)
			if bytes.Count(wire, []byte(`"type":"response.failed"`)) != 1 || !bytes.Contains(wire, []byte("channel_history_verification_failed")) || !bytes.Contains(wire, []byte("history-trace")) {
				t.Fatalf("wrong final error: %s", wire)
			}
			if bytes.Contains(wire, []byte("Start a new session")) {
				t.Fatal("history failure still forces a new session")
			}
		})
	}
}

func TestHistorySharesBudgetAndUsesOwnBackoffIndex(t *testing.T) {
	withMock(t, &mockHost{})
	delays := []time.Duration{}
	waitRetry = func(_ context.Context, d time.Duration) error { delays = append(delays, d); return nil }
	b := newBudget(testCredential())
	if !b.retry(context.Background(), "history", nil) || !b.retry(context.Background(), "transport", nil) || !b.retry(context.Background(), "history", nil) {
		t.Fatal("eligible internal retry denied")
	}
	if b.retry(context.Background(), "history", nil) || b.used != 3 {
		t.Fatal("history got more than two extra attempts")
	}
	if delays[0] < time.Second || delays[0] >= 1250*time.Millisecond || delays[2] < 3*time.Second || delays[2] >= 3250*time.Millisecond {
		t.Fatal(delays)
	}
	b = newBudget(testCredential())
	b.max = 1
	if !b.retry(context.Background(), "transport", nil) || b.retry(context.Background(), "history", nil) {
		t.Fatal("history exceeded the shared Key budget")
	}
	b = newBudget(testCredential())
	b.end = time.Now().Add(500 * time.Millisecond)
	if b.retry(context.Background(), "history", nil) {
		t.Fatal("history exceeded the waiting budget")
	}
}

func TestHistoryNeverReplaysAfterOutput(t *testing.T) {
	m := &mockHost{attempts: []attempt{{200, "", []string{sse(`{"type":"response.output_text.delta","delta":"already sent"}`), sse(`{"type":"response.failed","response":` + historyFailureBody + `}`)}}}}
	withMock(t, m)
	runStreaming(context.Background(), execution{StreamID: "down"}, testCredential(), []byte(`{}`))
	wire := bytes.Join(m.emitted, nil)
	if m.started != 1 || bytes.Count(wire, []byte("already sent")) != 1 || !bytes.Contains(wire, []byte("channel_history_verification_failed")) {
		t.Fatalf("unsafe post-output replay: %s", wire)
	}
}

func TestCanceledHistoryBackoffDoesNotEmitFailure(t *testing.T) {
	m := &mockHost{attempts: []attempt{{400, historyFailureBody, nil}}}
	withMock(t, m)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waitRetry = func(context.Context, time.Duration) error { cancel(); return context.Canceled }
	runStreaming(ctx, execution{StreamID: "down"}, testCredential(), []byte(`{}`))
	if m.started != 1 || m.closed != 1 || len(m.emitted) != 0 {
		t.Fatal("cancellation retried or emitted a visible failure")
	}
}

func TestHistoryReadFailureDoesNotBypassHistoryLimit(t *testing.T) {
	bad := attempt{400, historyFailureBody, nil}
	m := &mockHost{attempts: []attempt{bad, bad, bad}}
	withMock(t, m)
	hostCall = func(method string, value any, out any) error {
		err := m.call(method, value, out)
		if method == "host.http.stream_read" {
			out.(*httpChunk).Error = "read interrupted"
		}
		return err
	}
	_, err := runJSON(context.Background(), execution{}, testCredential(), []byte(`{}`))
	if err == nil || asRPCError(err).Code != historyErrorCode || m.started != 3 || m.closed != 3 {
		t.Fatalf("history read failure lost its limit: %v", err)
	}
}

func TestUpstreamFailurePreservesUsefulDiagnostics(t *testing.T) {
	secret := "mock-\"secret"
	raw, _ := json.Marshal(map[string]any{"trace_id": "diagnostic-trace", "error": map[string]any{"message": "The encrypted content for item rs_keep could not be verified. " + secret}})
	err := safeError(400, raw, secret)
	if err.Code != historyErrorCode || !strings.Contains(err.Message, "diagnostic-trace") || !strings.Contains(err.Message, "rs_keep") || strings.Contains(err.Message, secret) {
		t.Fatalf("bad history diagnostic: %v", err)
	}
	for _, tc := range []struct{ cause, code string }{{"TLS handshake timeout", "ar_upstream_timeout"}, {"unexpected EOF", "ar_upstream_response_invalid"}, {"connection reset by peer", "ar_upstream_connection_error"}} {
		err := safeErrorWithCause(502, nil, errors.New(tc.cause))
		if err.Code != tc.code || !strings.Contains(err.Message, "HTTP 502") || !strings.Contains(err.Message, tc.cause) {
			t.Fatal(err)
		}
	}
	err = safeError(503, []byte(`<html>unavailable</html>`))
	if err.Code != "ar_upstream_response_invalid" || !strings.Contains(err.Message, "HTTP 503") || strings.Contains(err.Message, "<html>") {
		t.Fatal(err)
	}
}
