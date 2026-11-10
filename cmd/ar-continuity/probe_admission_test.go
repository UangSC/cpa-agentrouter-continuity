package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/UangSC/cpa-agentrouter-continuity/internal/sdk"
)

func TestAnonymousProbeUsesTemporaryReservationWithoutOrigin(t *testing.T) {
	routeFixture(t)
	ar := arCandidate("ar", "mock-secret", 10)
	for _, id := range []string{"probe-one", "probe-two"} {
		picked, q := routeRequest(id, "", true, ar)
		if picked.Reject || picked.AuthID != ar.ID {
			t.Fatal(picked)
		}
		v, _ := admissionRequests.Load(id)
		r := v.(admissionRequest)
		if !r.Transient || r.RawSession != "" {
			t.Fatal("anonymous probe became a persistent identity")
		}
		release, err := acquireExecution(q, testCredential())
		if err != nil {
			t.Fatal(err)
		}
		if err = claimExecutionSlot(q); err != nil {
			t.Fatal(err)
		}
		_ = withRoutingLedger(func(s *routingLedger) error {
			if len(s.Origins) != 0 || len(s.Leases) != 1 {
				t.Fatal("probe persisted an origin")
			}
			return nil
		})
		release()
		completeAdmission(sdk.RequestCompletion{RequestID: id})
		_ = withRoutingLedger(func(s *routingLedger) error {
			if len(s.Origins) != 0 || len(s.Leases) != 0 {
				t.Fatal("probe retained state")
			}
			return nil
		})
	}
}

func TestInvalidIdentityAndHistoryCannotUseProbeAdmission(t *testing.T) {
	routeFixture(t)
	ar := arCandidate("ar", "mock-secret", 10)
	for _, tc := range []struct {
		headers http.Header
		body    string
	}{
		{http.Header{}, `{"input":[{"role":"assistant","content":"old"}]}`},
		{http.Header{}, `{"previous_response_id":"old","input":"continue"}`},
		{http.Header{"Session-Id": {"bad\nidentity"}}, `{"input":"hello"}`},
		{http.Header{"User-Agent": {"deepseek-harness/test"}, "X-Client-Request-Id": {"a"}}, `{"input":"hello","prompt_cache_key":"b"}`},
		{http.Header{"X-Parent-Session-Id": {"parent"}}, `{"input":"hello"}`},
	} {
		p := routeChild("invalid", tc.headers, []byte(tc.body), map[string]any{"caller_scope": "test"}, ar)
		if !p.Reject {
			t.Fatal("unsafe request admitted as probe", p)
		}
		completeAdmission(sdk.RequestCompletion{RequestID: "invalid"})
	}
}

func TestBodyMetadataCarriesStableConversationAndExecutionIDDoesNot(t *testing.T) {
	hash, raw := stableSession(nil, []byte(`{"input":"test","metadata":{"session_id":"conversation"}}`), nil)
	if raw != "conversation" || hash == "" {
		t.Fatal("explicit metadata session ignored")
	}
	hash, raw = stableSession(nil, []byte(`{"input":"test"}`), map[string]any{"canonical_session_id": "execution:one-request", "execution_session_id": "one-request"})
	if hash != "" || raw != "" {
		t.Fatal("request identifier treated as conversation")
	}
}

func TestJSONErrorEnvelopeAndMalformedBodyAreNotSuccessful(t *testing.T) {
	for _, raw := range []string{`{"error":{"code":"invalid_api_key","message":"mock-secret rejected"}}`, `{"status":"failed","error":{"code":"custom_error"}}`, `<html>gateway</html>`, `null`, `{}`} {
		t.Run(raw, func(t *testing.T) {
			m := &mockHost{attempts: []attempt{{200, "", []string{raw}}}}
			withMock(t, m)
			c := testCredential()
			zero := 0
			c.MaxRetries = &zero
			result, err := runJSON(context.Background(), execution{}, c, []byte(`{}`))
			if err == nil || len(result) != 0 || m.started != 1 {
				t.Fatal("failed JSON counted as success", err)
			}
			if strings.Contains(err.Error(), "mock-secret") {
				t.Fatal("error leaked credential")
			}
		})
	}
	e := safeError(500, []byte(`{"error":{"code":"mock-secret","message":"failed"}}`), "mock-secret")
	if strings.Contains(e.Code, "mock-secret") {
		t.Fatal("error code leaked credential")
	}
}

func TestCancelClosesBlockedJSONUpstreamOnce(t *testing.T) {
	started, closed := make(chan struct{}), make(chan struct{})
	var closeOnce sync.Once
	var calls atomic.Int32
	old := hostCall
	hostCall = func(method string, value any, out any) error {
		switch method {
		case "host.http.do_stream":
			*out.(*httpStart) = httpStart{Status: 200, StreamID: "blocked"}
		case "host.http.stream_read":
			close(started)
			<-closed
			return context.Canceled
		case "host.http.stream_close":
			calls.Add(1)
			closeOnce.Do(func() { close(closed) })
		default:
			t.Errorf("unexpected callback %s", method)
		}
		return nil
	}
	t.Cleanup(func() { hostCall = old })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := runJSON(ctx, execution{}, testCredential(), []byte(`{}`)); done <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("upstream not started")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not release reader")
	}
	if calls.Load() != 1 {
		t.Fatal("upstream closed multiple times", calls.Load())
	}
}

func TestLifecycleCompletionCancelsQueuedNonStreamingRequest(t *testing.T) {
	routeFixture(t)
	a, releaseA := reserveQueued(t, "a", "a", "mock-secret", 1)
	defer releaseA()
	if err := claimExecutionSlot(a); err != nil {
		t.Fatal(err)
	}
	b, releaseB := reserveQueued(t, "b", "b", "mock-secret", 1)
	defer releaseB()
	ctx, finish := executionContext(b)
	defer finish()
	completeAdmission(sdk.RequestCompletion{RequestID: "b"})
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("completion did not cancel request")
	}
	if err := waitExecutionSlot(ctx, b, testCredential()); err == nil {
		t.Fatal("completed request acquired a slot")
	}
	var activeCount int
	_ = withRoutingLedger(func(s *routingLedger) error { activeCount = len(s.Leases); return nil })
	if activeCount != 1 {
		t.Fatal("completion released unrelated lease")
	}
	encoded, _ := json.Marshal(b)
	if strings.Contains(string(encoded), "ctx") {
		t.Fatal("internal context serialized")
	}
}
