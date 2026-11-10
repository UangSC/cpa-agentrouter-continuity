package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/UangSC/cpa-agentrouter-continuity/internal/sdk"
	"net/http"
	"strings"
	"testing"
	"time"
)

func testCredential() credential {
	return credential{Type: pluginID, ID: "test-ar", APIKey: "mock-secret", BaseURL: "https://agentrouter.org/v1", Headers: map[string]string{"User-Agent": "codex_exec/test"}}
}
func TestSiteAndCredentialIsolation(t *testing.T) {
	for _, target := range []string{"https://api.openai.com/v1", "https://agentrouter.org.attacker.invalid/v1", "http://agentrouter.org/v1", "https://agentrouter.org:444/v1", "https://agentrouter.org/v1?x=1", "https://x@agentrouter.org/v1"} {
		t.Run(target, func(t *testing.T) {
			c := testCredential()
			c.BaseURL = target
			if validateCredential(c) == nil {
				t.Fatal("accepted wrong site")
			}
		})
	}
	c := testCredential()
	c.Type = "codex"
	if validateCredential(c) == nil {
		t.Fatal("accepted unrelated provider")
	}
	c = testCredential()
	c.Headers["User-Agent"] = "unsupported-client"
	if validateCredential(c) == nil {
		t.Fatal("unsupported UA accepted")
	}
}
func TestUnrelatedAuthNotClaimed(t *testing.T) {
	for _, provider := range []string{"codex", "claude", "other"} {
		q, _ := json.Marshal(sdk.AuthParseRequest{RawJSON: []byte(`{"type":"` + provider + `"}`)})
		v, e := handle("auth.parse", q)
		if e != nil || v.(sdk.AuthParseResponse).Handled {
			t.Fatal(provider, v, e)
		}
	}
}
func TestErrorClassification(t *testing.T) {
	cases := []struct {
		status     int
		body, kind string
	}{
		{400, `{"error":{"message":"OpenAI Responses bad request: The encrypted content for item rs_058e1a could not be verified. Reason: Encrypted content could not be decrypted or parsed.","type":"invalid_request_error","code":null}}`, "history"},
		{400, `{"error":{"message":"The requested item was created under a different Azure OpenAI resource."}}`, "history"},
		{400, `{"error":{"message":"The encrypted content for item compact_abc could not be verified."}}`, ""},
		{400, `{"error":{"code":"invalid_encrypted_content"}}`, "history"},
		{429, `{"error":{"code":"rate_limit_exceeded"}}`, "rate"},
		{429, `{"error":{"code":"insufficient_quota"}}`, ""},
		{500, `{"error":{"message":"insufficient balance","code":"insufficient_balance"}}`, ""},
		{401, `{"error":{"message":"retry server_error"}}`, ""},
		{403, `{"error":{"code":"server_error"}}`, ""},
		{503, `{"error":{"code":"no_available_channel"}}`, "transport"},
		{0, `{"type":"response.failed","response":{"error":{"code":"server_error","message":"try again"}}}`, "transport"},
		{0, `{"type":"response.failed","response":{"error":null}}`, "transport"},
		{400, `{"error":{"code":"context_length_exceeded"}}`, ""},
	}
	for i, c := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			if got := classify(c.status, []byte(c.body)); got != c.kind {
				t.Fatalf("got %q want %q", got, c.kind)
			}
		})
	}
}

func TestIncompleteReasonAndContentFilterPolicy(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{"top-level", `{"status":"incomplete","incomplete_details":{"reason":"content_filter"}}`, "content_filter"},
		{"event", `{"type":"response.incomplete","response":{"incomplete_details":{"reason":"content-filter"}}}`, "content_filter"},
		{"other", `{"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}}}`, "max_output_tokens"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := incompleteReason([]byte(tc.raw)); got != tc.want {
				t.Fatalf("reason=%q want %q", got, tc.want)
			}
		})
	}
	if e := incompleteError([]byte(`{"incomplete_details":{"reason":"max_output_tokens"}}`)); e != nil {
		t.Fatal("max_output_tokens must remain a normal incomplete response")
	}
	e := incompleteError([]byte(`{"incomplete_details":{"reason":"content_filter"}}`))
	if e == nil || e.Code != "ar_content_filter" || !strings.Contains(e.Message, "content_filter") {
		t.Fatalf("unexpected content filter error: %#v", e)
	}
}
func TestHistoryAlwaysPreserved(t *testing.T) {
	for _, raw := range []string{`{"input":[{"type":"reasoning","id":"rs_old","encrypted_content":"keep"},{"type":"function_call","id":"fc_old","call_id":"pair"}],"reasoning":{"effort":"high"}}`, `{"input":[{"type":"compaction","encrypted_content":"opaque"}],"previous_response_id":"previous"}`} {
		body, err := prepareBody([]byte(raw), "test", true)
		if err != nil {
			t.Fatal(err)
		}
		var want, got map[string]json.RawMessage
		_ = json.Unmarshal([]byte(raw), &want)
		_ = json.Unmarshal(body, &got)
		for k, v := range want {
			if !bytes.Equal(v, got[k]) {
				t.Fatalf("history changed: %s", k)
			}
		}
		b := newBudget(testCredential())
		b.max = 0
		if b.retry(context.Background(), "history", nil) {
			t.Fatal("retried history")
		}
	}
}
func TestSSEEveryByteBoundary(t *testing.T) {
	wire := []byte("event: a\r\ndata: {\"type\":\"response.created\"}\r\n\r\ndata: {\"type\":\"response.completed\"}\n\n")
	for size := 1; size <= len(wire); size++ {
		f := frameReader{}
		var out [][]byte
		for i := 0; i < len(wire); i += size {
			x, e := f.feed(wire[i:min(i+size, len(wire))])
			if e != nil {
				t.Fatal(e)
			}
			out = append(out, x...)
		}
		if len(out) != 2 || eventType(eventData(out[1])) != "response.completed" {
			t.Fatal(size, out)
		}
	}
}
func TestRetryAfterIsFloor(t *testing.T) {
	if d := retryDelay("transport", 0, http.Header{"Retry-After": {"20"}}); d < 20*time.Second {
		t.Fatal(d)
	}
	if d := retryDelay("transport", 0, http.Header{"X-Ms-Retry-After-Ms": {"25000"}}); d < 25*time.Second {
		t.Fatal(d)
	}
}

type attempt struct {
	status int
	body   string
	frames []string
}
type mockHost struct {
	attempts []attempt
	started  int
	reads    int
	emitted  [][]byte
	bodies   [][]byte
	closed   int
}

func (m *mockHost) call(method string, v any, out any) error {
	raw, _ := json.Marshal(v)
	var q map[string]json.RawMessage
	_ = json.Unmarshal(raw, &q)
	switch method {
	case "host.http.do_stream":
		var u string
		_ = json.Unmarshal(q["url"], &u)
		if u != "https://agentrouter.org/v1/responses" {
			return fmt.Errorf("wrong URL")
		}
		var h http.Header
		_ = json.Unmarshal(q["headers"], &h)
		if h.Get("User-Agent") != "codex_exec/test" || h.Get("Authorization") != "Bearer mock-secret" {
			return fmt.Errorf("wrong auth")
		}
		var body []byte
		_ = json.Unmarshal(q["body"], &body)
		m.bodies = append(m.bodies, body)
		m.started++
		m.reads = 0
		if m.started > len(m.attempts) {
			return fmt.Errorf("extra request")
		}
		a := m.attempts[m.started-1]
		*out.(*httpStart) = httpStart{Status: a.status, Headers: http.Header{"Content-Type": {"text/event-stream"}}, StreamID: "up"}
	case "host.http.stream_read":
		a := m.attempts[m.started-1]
		c := httpChunk{}
		if a.status >= 400 {
			c.Payload = []byte(a.body)
			c.Done = true
		} else if m.reads < len(a.frames) {
			c.Payload = []byte(a.frames[m.reads])
			m.reads++
		} else {
			c.Done = true
		}
		*out.(*httpChunk) = c
	case "host.http.stream_close":
		m.closed++
	case "host.stream.emit":
		var p []byte
		_ = json.Unmarshal(q["payload"], &p)
		m.emitted = append(m.emitted, p)
	case "host.stream.close":
	default:
		return fmt.Errorf("unexpected callback %s", method)
	}
	return nil
}
func sse(v string) string { return "data: " + v + "\n\n" }
func withMock(t *testing.T, m *mockHost) {
	old, wait := hostCall, waitRetry
	hostCall = m.call
	waitRetry = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { hostCall = old; waitRetry = wait })
}

const completed = `{"type":"response.completed","response":{"status":"completed","output":[]}}`

func TestStreamRecoversBeforeContent(t *testing.T) {
	m := &mockHost{attempts: []attempt{{429, `{"error":{"code":"rate_limit_exceeded"}}`, nil}, {200, "", []string{sse(`{"type":"response.created"}`)}}, {200, "", []string{sse(`{"type":"response.created"}`), sse(`{"type":"response.output_text.delta","delta":"OK"}`), sse(completed)}}}}
	withMock(t, m)
	runStreaming(context.Background(), execution{StreamID: "down"}, testCredential(), []byte(`{"input":"x"}`))
	if m.started != 3 || m.closed != 3 {
		t.Fatal(m.started, m.closed)
	}
	wire := string(bytes.Join(m.emitted, nil))
	if strings.Count(wire, "response.created") != 1 || strings.Count(wire, "\"delta\":\"OK\"") != 1 || !strings.Contains(wire, "response.completed") {
		t.Fatal(wire)
	}
}
func TestHistoryFailureRetriesOriginalRequestWithinLimit(t *testing.T) {
	m := &mockHost{attempts: []attempt{{400, `{"error":{"message":"The encrypted content for item rs_1 could not be verified."}}`, nil}}}
	m.attempts = append(m.attempts, m.attempts[0], m.attempts[0])
	withMock(t, m)
	runStreaming(context.Background(), execution{StreamID: "down"}, testCredential(), []byte(`{"input":[{"type":"reasoning","id":"rs_1","encrypted_content":"bad"}]}`))
	if m.started != 3 || !bytes.Equal(m.bodies[0], m.bodies[1]) || !bytes.Equal(m.bodies[0], m.bodies[2]) || !bytes.Contains(m.bodies[0], []byte("rs_1")) {
		t.Fatal("history changed or replayed")
	}
	if !bytes.Contains(bytes.Join(m.emitted, nil), []byte("channel_history_verification_failed")) {
		t.Fatal("missing history verification hint")
	}
}

func TestContentFilterFailsFastWithoutReplay(t *testing.T) {
	m := &mockHost{attempts: []attempt{{200, "", []string{
		sse(`{"type":"response.created"}`),
		sse(`{"type":"response.output_text.delta","delta":"I'm sorry"}`),
		sse(`{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"content_filter"}}}`),
	}}}}
	withMock(t, m)
	runStreaming(context.Background(), execution{StreamID: "down"}, testCredential(), []byte(`{}`))
	if m.started != 1 {
		t.Fatalf("content_filter was replayed: %d attempts", m.started)
	}
	wire := string(bytes.Join(m.emitted, nil))
	if !strings.Contains(wire, "channel_content_filter") || !strings.Contains(wire, "content_filter") {
		t.Fatalf("missing explicit content filter failure: %s", wire)
	}
	if strings.Contains(wire, `"type":"response.incomplete"`) {
		t.Fatal("forwarded replayable response.incomplete")
	}
}

func TestContentFilterBeforeOutputFailsWithoutReplay(t *testing.T) {
	m := &mockHost{attempts: []attempt{{200, "", []string{
		sse(`{"type":"response.created"}`),
		sse(`{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"content_filter"}}}`),
	}}}}
	withMock(t, m)
	runStreaming(context.Background(), execution{StreamID: "down"}, testCredential(), []byte(`{}`))
	wire := bytes.Join(m.emitted, nil)
	if m.started != 1 || m.closed != 1 || bytes.Count(wire, []byte(`"type":"response.failed"`)) != 1 || !bytes.Contains(wire, []byte("channel_content_filter")) {
		t.Fatalf("expected one terminal failure and no replay: attempts=%d closed=%d wire=%s", m.started, m.closed, wire)
	}
	if bytes.Contains(wire, []byte("response.incomplete")) {
		t.Fatal("content_filter incomplete event was forwarded")
	}
}

func TestJSONIncompletePolicy(t *testing.T) {
	for _, reason := range []string{"content_filter", "max_output_tokens"} {
		t.Run(reason, func(t *testing.T) {
			raw := `{"status":"incomplete","incomplete_details":{"reason":"` + reason + `"},"output":[]}`
			m := &mockHost{attempts: []attempt{{200, "", []string{raw}}}}
			withMock(t, m)
			got, err := runJSON(context.Background(), execution{}, testCredential(), []byte(`{}`))
			if m.started != 1 || m.closed != 1 {
				t.Fatalf("incomplete response replayed or left open: %+v", m)
			}
			if reason == "content_filter" {
				if err == nil || asRPCError(err).Code != "ar_content_filter" || asRPCError(err).HTTPStatus != 400 || len(got) != 0 {
					t.Fatalf("expected content_filter HTTP 400: %s %v", got, err)
				}
			} else if err != nil || string(got) != raw {
				t.Fatalf("ordinary incomplete response changed: %s %v", got, err)
			}
		})
	}
}

func TestMaxOutputTokensStreamPreserved(t *testing.T) {
	frame := sse(`{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`)
	m := &mockHost{attempts: []attempt{{200, "", []string{frame}}}}
	withMock(t, m)
	runStreaming(context.Background(), execution{StreamID: "down"}, testCredential(), []byte(`{}`))
	if m.started != 1 || m.closed != 1 || string(bytes.Join(m.emitted, nil)) != frame {
		t.Fatal("max_output_tokens event changed or replayed")
	}
}
func TestNeverReplayAfterToolOrText(t *testing.T) {
	for _, event := range []string{`{"type":"response.output_text.delta","delta":"hello"}`, `{"type":"response.output_item.added","item":{"type":"function_call","call_id":"once"}}`, `{"type":"unknown_future_event","payload":"possible effect"}`} {
		t.Run(event, func(t *testing.T) {
			m := &mockHost{attempts: []attempt{{200, "", []string{sse(event)}}}}
			withMock(t, m)
			runStreaming(context.Background(), execution{StreamID: "down"}, testCredential(), []byte(`{}`))
			if m.started != 1 {
				t.Fatal("replayed")
			}
			if !bytes.Contains(bytes.Join(m.emitted, nil), []byte("channel_stream_interrupted")) {
				t.Fatal("missing failure")
			}
		})
	}
}
func TestAuthFailuresNotRetried(t *testing.T) {
	for _, status := range []int{401, 403} {
		m := &mockHost{attempts: []attempt{{status, `{"error":{"message":"denied"}}`, nil}}}
		withMock(t, m)
		runStreaming(context.Background(), execution{StreamID: "down"}, testCredential(), []byte(`{}`))
		if m.started != 1 {
			t.Fatal(status, m.started)
		}
	}
}
func TestBudgetAndCancellation(t *testing.T) {
	c := testCredential()
	n := 2
	c.MaxRetries = &n
	b := newBudget(c)
	old := waitRetry
	waitRetry = func(context.Context, time.Duration) error { return nil }
	defer func() { waitRetry = old }()
	for i := 0; i < 2; i++ {
		if !b.retry(context.Background(), "rate", nil) {
			t.Fatal(i)
		}
	}
	if b.retry(context.Background(), "rate", nil) {
		t.Fatal("budget bypass")
	}
	b = newBudget(c)
	b.end = time.Now().Add(time.Second)
	if b.retry(context.Background(), "rate", http.Header{"Retry-After": {"30"}}) {
		t.Fatal("deadline bypass")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if b.retry(ctx, "transport", nil) {
		t.Fatal("retried canceled request")
	}
}

func TestModelAliasRestrictedToCredential(t *testing.T) {
	c := testCredential()
	c.Models = []sdk.ModelInfo{{ID: "ar/gpt-test", Name: "gpt-test"}}
	if model, e := modelForCredential(c, "ar/gpt-test"); e != nil || model != "gpt-test" {
		t.Fatal(model, e)
	}
	if _, e := modelForCredential(c, "gpt-test"); e == nil {
		t.Fatal("unqualified model unexpectedly accepted")
	}
}
