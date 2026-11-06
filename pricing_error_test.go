package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/UangSC/cpa-agentrouter-continuity/internal/sdk"
)

const pricingDiagnostic = "model test-model tiered expr run failed: expr compile error: unknown name cc12h (1:62)\n| tier(\"small\", cc12h * 25)\n[trace_id=trace-pricing-test]"

func pricingTestBody(kind string) string {
	detail := map[string]any{"code": "server_error", "message": pricingDiagnostic}
	root := map[string]any{"error": detail}
	if kind == "response.failed" {
		root = map[string]any{"type": kind, "response": map[string]any{"status": "failed", "error": detail}}
	} else if kind == "error" {
		root["type"] = kind
	}
	raw, _ := json.Marshal(root)
	return string(raw)
}

func assertPricingError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("missing pricing configuration error")
	}
	rpc := asRPCError(err)
	if rpc.Code != pricingErrorCode || rpc.HTTPStatus != 400 {
		t.Fatalf("wrong pricing error: %+v", rpc)
	}
	for _, required := range []string{"上游模型计费规则异常", "联系上游维护者", "cc12h", "trace-pricing-test"} {
		if !strings.Contains(rpc.Message, required) {
			t.Fatalf("lost diagnostic %q: %s", required, rpc.Message)
		}
	}
}

func TestPricingClassificationAndNoRetry(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{
		{400, pricingTestBody("")}, {500, pricingTestBody("")}, {429, pricingTestBody("")},
		{200, pricingTestBody("")}, {0, pricingTestBody("response.failed")}, {0, pricingTestBody("error")},
		{500, pricingDiagnostic},
	} {
		t.Run(fmt.Sprintf("%d/%d", tc.status, len(tc.body)), func(t *testing.T) {
			kind := classify(tc.status, []byte(tc.body))
			if kind != pricingErrorKind {
				t.Fatalf("classified as %q", kind)
			}
			budget := newBudget(testCredential())
			if budget.retry(context.Background(), kind, http.Header{"Retry-After": {"60"}}) || budget.used != 0 {
				t.Fatal("pricing configuration error consumed a retry")
			}
			assertPricingError(t, safeError(tc.status, []byte(tc.body)))
		})
	}
}

func TestPricingDetectionIgnoresSuccessfulOutputAndOtherErrors(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"status": "completed", "output": []any{map[string]any{"type": "message", "content": pricingDiagnostic}}})
	for _, tc := range []struct {
		status int
		body   []byte
	}{
		{200, raw}, {500, raw},
		{400, []byte(`{"error":{"message":"tool expr compile error: unknown name value"}}`)},
		{500, []byte(`{"error":{"message":"tiered expr run failed: timeout"}}`)},
		{503, []byte(`{"error":{"code":"server_error","message":"temporary upstream outage"}}`)},
	} {
		if pricingConfigError(tc.status, tc.body) != nil {
			t.Fatalf("misclassified: %s", tc.body)
		}
	}
	if classify(503, []byte(`{"error":{"code":"server_error"}}`)) != "transport" {
		t.Fatal("ordinary transport recovery changed")
	}
}

func TestPricingDiagnosticsBoundedAndSecretsRedacted(t *testing.T) {
	secret := "mock-\"private-key"
	message := "model test tiered expr run failed: expr compile error: unknown name cc12h " + secret + strings.Repeat("x", 4000) + " [trace_id=trace-pricing-test]"
	raw, _ := json.Marshal(map[string]any{"error": map[string]any{"message": message}})
	err := safeError(500, raw, secret)
	assertPricingError(t, err)
	if strings.Contains(err.Message, secret) || len([]rune(err.Message)) > 450 {
		t.Fatal("secret leaked or diagnostic was not bounded")
	}
	err = safeError(500, append([]byte("HTTP callback failed: "), raw...), secret)
	encoded, _ := json.Marshal(secret)
	if strings.Contains(err.Message, secret) || strings.Contains(err.Message, string(encoded[1:len(encoded)-1])) {
		t.Fatal("JSON-escaped key leaked from a callback diagnostic")
	}
	for _, place := range []string{"root", "response", "error"} {
		detail := map[string]any{"message": strings.Split(pricingDiagnostic, "\n")[0]}
		response := map[string]any{"error": detail}
		root := map[string]any{"type": "response.failed", "response": response}
		map[string]map[string]any{"root": root, "response": response, "error": detail}[place]["trace_id"] = "trace-pricing-test"
		raw, _ := json.Marshal(root)
		assertPricingError(t, pricingConfigError(0, raw))
	}
}

func TestPricingUnexpectedJSONAndCompactErrors(t *testing.T) {
	for _, mode := range []string{"json", "stream-json", "compact"} {
		t.Run(mode, func(t *testing.T) {
			m := &mockHost{attempts: []attempt{{200, "", []string{pricingTestBody("")}}}}
			withMock(t, m)
			hostCall = func(method string, value any, out any) error {
				if mode == "compact" && method == "host.http.do_stream" {
					request := value.(map[string]any)
					if request["url"] != "https://agentrouter.org/v1/responses/compact" {
						t.Error("wrong compact URL")
					}
					request["url"] = "https://agentrouter.org/v1/responses"
				}
				err := m.call(method, value, out)
				if method == "host.http.do_stream" {
					out.(*httpStart).Headers.Set("Content-Type", "application/json")
				}
				return err
			}
			c := testCredential()
			switch mode {
			case "stream-json":
				runStreaming(context.Background(), execution{StreamID: "down"}, c, []byte(`{}`))
				assertPricingWire(t, m.emitted)
			case "compact":
				c.Models = []sdk.ModelInfo{{ID: "test-model"}}
				_, err := compact(execution{ExecutorRequest: sdk.ExecutorRequest{Model: "test-model", Payload: []byte(`{"input":[]}`)}}, c)
				assertPricingError(t, err)
			default:
				_, err := runJSON(context.Background(), execution{}, c, []byte(`{}`))
				assertPricingError(t, err)
			}
			if m.started != 1 || m.closed != 1 {
				t.Fatal("pricing failure replayed or leaked a stream")
			}
		})
	}
}

func TestPricingHTTPAndStreamingNeverReplay(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, status := range []int{400, 500} {
			t.Run(fmt.Sprintf("stream=%v/status=%d", stream, status), func(t *testing.T) {
				m := &mockHost{attempts: []attempt{{status, pricingTestBody(""), nil}}}
				withMock(t, m)
				if stream {
					runStreaming(context.Background(), execution{StreamID: "down"}, testCredential(), []byte(`{}`))
					assertPricingWire(t, m.emitted)
				} else {
					_, err := runJSON(context.Background(), execution{}, testCredential(), []byte(`{}`))
					assertPricingError(t, err)
				}
				if m.started != 1 || m.closed != 1 {
					t.Fatalf("replayed or leaked HTTP stream: %d/%d", m.started, m.closed)
				}
			})
		}
	}
	for _, kind := range []string{"response.failed", "error"} {
		for _, hasOutput := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/output=%v", kind, hasOutput), func(t *testing.T) {
				frames := []string{sse(`{"type":"response.created"}`)}
				if hasOutput {
					frames = append(frames, sse(`{"type":"response.output_text.delta","delta":"keep this text"}`))
				}
				frames = append(frames, sse(pricingTestBody(kind)))
				m := &mockHost{attempts: []attempt{{200, "", frames}}}
				withMock(t, m)
				runStreaming(context.Background(), execution{StreamID: "down"}, testCredential(), []byte(`{}`))
				assertPricingWire(t, m.emitted)
				if m.started != 1 || m.closed != 1 {
					t.Fatal("SSE pricing failure replayed or left open")
				}
				if hasOutput && bytes.Count(bytes.Join(m.emitted, nil), []byte("keep this text")) != 1 {
					t.Fatal("already delivered text changed")
				}
			})
		}
	}
}

func assertPricingWire(t *testing.T, emitted [][]byte) {
	t.Helper()
	wire := bytes.Join(emitted, nil)
	if bytes.Count(wire, []byte(`"type":"response.failed"`)) != 1 || !bytes.Contains(wire, []byte(pricingErrorCode)) || !bytes.Contains(wire, []byte("trace-pricing-test")) || !bytes.Contains(wire, []byte("cc12h")) {
		t.Fatalf("unexpected terminal response: %s", wire)
	}
}

func TestPricingBodyTakesPriorityOverReadFailure(t *testing.T) {
	m := &mockHost{attempts: []attempt{{500, pricingTestBody(""), nil}}}
	withMock(t, m)
	hostCall = func(method string, value any, out any) error {
		err := m.call(method, value, out)
		if method == "host.http.stream_read" {
			out.(*httpChunk).Error = "read interrupted"
		}
		return err
	}
	_, err := runJSON(context.Background(), execution{}, testCredential(), []byte(`{}`))
	assertPricingError(t, err)
	if m.started != 1 || m.closed != 1 {
		t.Fatal("read failure overrode the pricing response")
	}
}

func TestPricingHostCallbackFailureNeverRetries(t *testing.T) {
	m := &mockHost{}
	withMock(t, m)
	starts := 0
	hostCall = func(method string, value any, out any) error {
		if method == "host.http.do_stream" {
			starts++
			return errors.New(pricingDiagnostic)
		}
		return m.call(method, value, out)
	}
	runStreaming(context.Background(), execution{StreamID: "down"}, testCredential(), []byte(`{}`))
	assertPricingWire(t, m.emitted)
	if starts != 1 {
		t.Fatal("callback pricing error was retried")
	}
}

func TestPricingExecutorReleasesSessionReservation(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			routeFixture(t)
			candidate := arCandidate("ar", "mock-secret", 10)
			candidate.Attributes["ar_max_concurrent_sessions"] = "1"
			_, q := routeRequest("pricing-request", "pricing-session", true, candidate)
			c := testCredential()
			c.Models = []sdk.ModelInfo{{ID: "test-model"}}
			q.Model = "test-model"
			q.StorageJSON, _ = json.Marshal(c)
			q.Payload = q.OriginalRequest
			method := "executor.execute"
			if stream {
				method = "executor.execute_stream"
				q.StreamID = "down"
			}
			m := &mockHost{attempts: []attempt{{500, pricingTestBody(""), nil}}}
			withMock(t, m)
			closed := make(chan struct{})
			hostCall = func(method string, value any, out any) error {
				err := m.call(method, value, out)
				if method == "host.stream.close" {
					close(closed)
				}
				return err
			}
			raw, _ := json.Marshal(q)
			_, err := handle(method, raw)
			if stream {
				if err != nil {
					t.Fatal(err)
				}
				select {
				case <-closed:
				case <-time.After(2 * time.Second):
					t.Fatal("stream never terminated")
				}
			} else {
				assertPricingError(t, err)
			}
			deadline := time.Now().Add(2 * time.Second)
			for {
				count := 0
				if err := withRoutingLedger(func(s *routingLedger) error { count = len(s.Leases); return nil }); err != nil {
					t.Fatal(err)
				}
				if count == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("session reservation leaked")
				}
				time.Sleep(time.Millisecond)
			}
			if m.started != 1 || m.closed != 1 {
				t.Fatal("executor retried or leaked the upstream")
			}
			monitor.mu.Lock()
			record := monitor.Recent[len(monitor.Recent)-1]
			monitor.mu.Unlock()
			if record.ErrorCode != pricingErrorCode || record.Attempts != 1 || record.Status != "failed" {
				t.Fatalf("wrong existing log fields: %+v", record)
			}
		})
	}
}
