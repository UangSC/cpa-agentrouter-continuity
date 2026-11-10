package main

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/UangSC/cpa-agentrouter-continuity/internal/sdk"
)

func TestAnonymousToolContinuationUsesVerifiedOriginalKey(t *testing.T) {
	routeFixture(t)
	a, b := arCandidate("ar", "mock-secret", 10), arCandidate("other", "mock-other", 100)
	p, q := routeRequest("probe", "", true, a)
	if p.AuthID != a.ID {
		t.Fatal(p)
	}
	c := testCredential()
	c.ID = a.ID
	callback := newContinuityRecorder(q, c)
	if callback == nil {
		t.Fatal("probe recorder unavailable")
	}
	raw := []byte(`{"type":"response.completed","response":{"id":"resp_observed","output":[{"type":"function_call","id":"fc_observed","call_id":"call_observed","name":"shell","arguments":"sensitive-command"},{"type":"reasoning","encrypted_content":"sensitive-encrypted"}]}}`)
	if err := callback.remember(c, raw); err != nil {
		t.Fatal(err)
	}
	completeAdmission(sdk.RequestCompletion{RequestID: "probe"})
	for _, body := range []string{
		`{"input":[{"type":"function_call_output","call_id":"call_observed","output":"time result"}]}`,
		`{"previous_response_id":"resp_observed","input":"continue"}`,
		`{"input":[{"type":"reasoning","encrypted_content":"sensitive-encrypted"},{"role":"user","content":"continue"}]}`,
	} {
		headers := http.Header{}
		meta := map[string]any{"caller_scope": "test-caller"}
		before := beforeAdmission(sdk.RequestInterceptRequest{RequestID: "continue", Headers: headers, Body: []byte(body), Metadata: meta})
		headers.Set(admissionHeader, before.Headers.Get(admissionHeader))
		pick := pickAdmission(sdk.SchedulerPickRequest{Model: "test-model", Options: sdk.SchedulerOptions{Headers: headers, Metadata: meta}, Candidates: []sdk.SchedulerAuthCandidate{a, b}})
		if pick.AuthID != a.ID || pick.Reject {
			t.Fatal("continuation switched Key", pick)
		}
		q := execution{ExecutorRequest: sdk.ExecutorRequest{AuthID: a.ID, Headers: headers, Metadata: meta, OriginalRequest: []byte(body)}}
		release, err := acquireExecution(q, c)
		if err != nil {
			t.Fatal(err)
		}
		release()
		completeAdmission(sdk.RequestCompletion{RequestID: "continue"})
	}
	data, err := os.ReadFile(routingPath + ".continuity")
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"mock-secret", "sensitive-command", "sensitive-encrypted", "call_observed", "resp_observed", "test-caller"} {
		if strings.Contains(string(data), secret) {
			t.Fatal("raw continuity material persisted", secret)
		}
	}
}

func TestContinuityIsolationUnknownConflictsAndExpiry(t *testing.T) {
	routeFixture(t)
	c := testCredential()
	c.ID = "ar"
	recorder := &continuityRecorder{scope: "caller", session: "one", seen: map[string]bool{}}
	if err := recorder.remember(c, []byte(`{"id":"resp_one","output":[{"type":"function_call","call_id":"call_one"}]}`)); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"input":[{"type":"function_call_output","call_id":"call_one","output":"done"}]}`)
	if entry, conflict := lookupContinuity("other-caller", body); entry != nil || conflict {
		t.Fatal("cross-caller match")
	}
	if entry, _ := lookupContinuity("caller", []byte(`{"input":[{"type":"function_call_output","call_id":"unknown"}]}`)); entry != nil {
		t.Fatal("unknown output accepted")
	}
	if entry, _ := lookupContinuity("caller", []byte(`{"input":[{"role":"user","content":"call_one"}]}`)); entry != nil {
		t.Fatal("text matched")
	}
	second := &continuityRecorder{scope: "caller", session: "two", seen: map[string]bool{}}
	c.APIKey = "mock-other"
	if err := second.remember(c, []byte(`{"id":"resp_two","output":[{"type":"function_call","call_id":"call_two"}]}`)); err != nil {
		t.Fatal(err)
	}
	mixed := []byte(`{"input":[{"type":"function_call_output","call_id":"call_one"},{"type":"function_call_output","call_id":"call_two"}]}`)
	if _, conflict := lookupContinuity("caller", mixed); !conflict {
		t.Fatal("mixed sessions accepted")
	}
	if err := second.remember(c, []byte(`{"output":[{"type":"function_call","call_id":"call_one"}]}`)); err != nil {
		t.Fatal(err)
	}
	if _, conflict := lookupContinuity("caller", body); !conflict {
		t.Fatal("ambiguous call ID overwritten")
	}
	_ = withContinuity(func(s *routingLedger) error {
		for key, entry := range s.References {
			entry.Expires = time.Now().Add(-time.Second)
			s.References[key] = entry
		}
		return nil
	})
	if entry, conflict := lookupContinuity("caller", body); entry != nil || conflict {
		t.Fatal("expired reference matched")
	}
}

func TestContinuityCapAndUnavailableOriginalKey(t *testing.T) {
	routeFixture(t)
	_ = withContinuity(func(s *routingLedger) error {
		for i := 0; i < continuityLimit; i++ {
			key, _ := json.Marshal(i)
			s.References[string(key)] = continuityEntry{Expires: time.Now().Add(time.Hour)}
		}
		return nil
	})
	c := testCredential()
	c.ID = "original"
	r := &continuityRecorder{scope: "test-caller", session: "chain", seen: map[string]bool{}}
	if err := r.remember(c, []byte(`{"output":[{"call_id":"call_retained"}]}`)); err != nil {
		t.Fatal(err)
	}
	_ = withContinuity(func(s *routingLedger) error {
		if len(s.References) != continuityLimit {
			t.Fatal("reference cache unbounded")
		}
		return nil
	})
	body := []byte(`{"input":[{"type":"function_call_output","call_id":"call_retained","output":"done"}]}`)
	p := routeChild("unavailable", http.Header{}, body, map[string]any{"caller_scope": "test-caller"}, arCandidate("different", "mock-other", 100), otherCandidate())
	if !p.Reject {
		t.Fatal("unavailable original Key changed", p)
	}
}

func TestChangingRequestIdentityUsesObservedOutputWithoutOverridingKnownOrigin(t *testing.T) {
	routeFixture(t)
	a, b := arCandidate("ar", "mock-secret", 10), arCandidate("other", "mock-other", 100)
	_, q := routeRequest("first", "request-generated-id", true, a)
	c := testCredential()
	c.ID = a.ID
	if err := newContinuityRecorder(q, c).remember(c, []byte(`{"output":[{"type":"function_call","call_id":"call_observed"}]}`)); err != nil {
		t.Fatal(err)
	}
	completeAdmission(sdk.RequestCompletion{RequestID: "first"})
	body := []byte(`{"input":[{"type":"function_call_output","call_id":"call_observed","output":"done"}]}`)
	p := routeChild("new-request", http.Header{"Session-Id": {"different-generated-id"}}, body, map[string]any{"caller_scope": "test-caller"}, a, b)
	if p.AuthID != a.ID || p.Reject {
		t.Fatal("changed execution identity lost original Key", p)
	}
	completeAdmission(sdk.RequestCompletion{RequestID: "new-request"})
	p, _ = routeRequest("later", "different-generated-id", false, a, b)
	if p.AuthID != a.ID {
		t.Fatal("recovered identity not made sticky", p)
	}
	routeRequest("foreign", "foreign-id", true, otherCandidate())
	p = routeChild("foreign-replay", http.Header{"Session-Id": {"foreign-id"}}, body, map[string]any{"caller_scope": "test-caller"}, a, otherCandidate())
	if p.AuthID == a.ID {
		t.Fatal("observed token overrode external session origin")
	}
}
