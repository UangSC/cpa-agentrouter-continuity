package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/UangSC/cpa-agentrouter-continuity/internal/sdk"
)

func TestMain(m *testing.M) {
	root, e := os.MkdirTemp("", "ar-routing-tests-")
	if e != nil {
		panic(e)
	}
	routingPath = filepath.Join(root, "sessions.json")
	code := m.Run()
	_ = os.RemoveAll(root)
	os.Exit(code)
}
func routeFixture(t *testing.T) {
	old := routingPath
	routingPath = filepath.Join(t.TempDir(), "sessions.json")
	oldHost := hostCall
	hostCall = func(_ string, _ any, out any) error {
		if v, ok := out.(*sdk.HostAffinityLookupResponse); ok {
			v.Status = "unbound"
		}
		return nil
	}
	t.Cleanup(func() {
		routingPath = old
		hostCall = oldHost
		admissionRequests.Range(func(k, v any) bool { admissionRequests.Delete(k); return true })
	})
}
func arCandidate(id, key string, priority int) sdk.SchedulerAuthCandidate {
	return sdk.SchedulerAuthCandidate{ID: id, Provider: pluginID, Priority: priority, Attributes: map[string]string{"ar_key_hash": digest(key)}}
}
func otherCandidate() sdk.SchedulerAuthCandidate {
	return sdk.SchedulerAuthCandidate{ID: "other", Provider: "codex", Priority: 0}
}
func routeRequest(requestID, session string, fresh bool, candidates ...sdk.SchedulerAuthCandidate) (sdk.SchedulerPickResponse, execution) {
	body := []byte(`{"input":[{"role":"user","content":"hello"}]}`)
	if !fresh {
		body = []byte(`{"input":[{"role":"user","content":"hello"},{"role":"assistant","content":"history"},{"role":"user","content":"continue"}]}`)
	}
	headers := http.Header{}
	if session != "" {
		headers.Set("Session_id", session)
	}
	metadata := map[string]any{"caller_scope": "test-caller"}
	response := beforeAdmission(sdk.RequestInterceptRequest{RequestID: requestID, Headers: headers, Body: body, Metadata: metadata})
	headers.Set(admissionHeader, response.Headers.Get(admissionHeader))
	picked := pickAdmission(sdk.SchedulerPickRequest{Model: "test-model", Providers: []string{pluginID, "codex"}, Options: sdk.SchedulerOptions{Headers: headers, Metadata: metadata}, Candidates: candidates})
	return picked, execution{ExecutorRequest: sdk.ExecutorRequest{AuthID: picked.AuthID, AuthProvider: pluginID, Headers: headers, Metadata: metadata, OriginalRequest: body}}
}
func TestNewConversationAdmittedAndContinuationKeepsOrigin(t *testing.T) {
	routeFixture(t)
	ar := arCandidate("ar", "mock-secret", 10)
	picked, q := routeRequest("r1", "s1", true, ar, otherCandidate())
	if picked.AuthID != "ar" {
		t.Fatal(picked)
	}
	release, e := acquireExecution(q, testCredential())
	if e != nil {
		t.Fatal(e)
	}
	release()
	picked, _ = routeRequest("r2", "s1", false, ar, otherCandidate())
	if picked.AuthID != "ar" {
		t.Fatal("AR continuation denied", picked)
	}
}

func TestHarnessResponsesSessionIdentity(t *testing.T) {
	const session = "session-cfc252cf-ab7a-401e-ac5f-a13434c2a4f6"
	const userAgent = "deepseek-harness/0.2.2-rc.1 (+https://github.com/deepseek-ai/deepseek-harness)"
	for _, test := range []struct {
		name, agent, requestID, cacheKey, explicit, want string
	}{
		{"both SDK fields", userAgent, session, session, "", session},
		{"header only", userAgent, session, "", "", session},
		{"body only", userAgent, "", session, "", session},
		{"conflicting SDK fields", userAgent, session, "different-session", "", ""},
		{"ordinary request ID", "generic-client", session, "", "", ""},
		{"ordinary cache partition", "generic-client", "", session, "", ""},
		{"explicit session wins", userAgent, "request", "cache", session, session},
		{"invalid session", userAgent, "", "invalid\nidentity", "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			headers := http.Header{}
			headers.Set("User-Agent", test.agent)
			headers.Set("X-Client-Request-Id", test.requestID)
			headers.Set("Session-Id", test.explicit)
			body, _ := json.Marshal(map[string]any{"input": "hello", "prompt_cache_key": test.cacheKey})
			hash, raw := stableSession(headers, body, map[string]any{"caller_scope": "test-caller"})
			if raw != test.want || (test.want == "" && hash != "") || (test.want != "" && hash != digest("test-caller\x00"+test.want)) {
				t.Fatalf("identity = (%q, %q), want session %q", hash, raw, test.want)
			}
		})
	}
}

func TestHarnessAdmissionContinuationAndForeignProtection(t *testing.T) {
	routeFixture(t)
	ar := arCandidate("ar", "mock-secret", 10)
	route := func(id, session string, history bool, candidates ...sdk.SchedulerAuthCandidate) (sdk.SchedulerPickResponse, execution) {
		input := []map[string]any{{"role": "user", "content": "hello"}}
		if history {
			input = append(input, map[string]any{"role": "assistant", "content": "previous reply"}, map[string]any{"role": "user", "content": "continue"})
		}
		body, _ := json.Marshal(map[string]any{"input": input, "prompt_cache_key": session, "store": false})
		headers := http.Header{"User-Agent": {"deepseek-harness/0.2.2-rc.1"}, "X-Client-Request-Id": {session}}
		metadata := map[string]any{"caller_scope": "test-caller"}
		before := beforeAdmission(sdk.RequestInterceptRequest{RequestID: id, Headers: headers, Body: body, Metadata: metadata})
		headers.Set(admissionHeader, before.Headers.Get(admissionHeader))
		picked := pickAdmission(sdk.SchedulerPickRequest{Model: "test-model", Options: sdk.SchedulerOptions{Headers: headers, Metadata: metadata}, Candidates: candidates})
		return picked, execution{ExecutorRequest: sdk.ExecutorRequest{AuthID: picked.AuthID, AuthProvider: pluginID, Headers: headers, Metadata: metadata, OriginalRequest: body}}
	}
	for i, history := range []bool{false, true} {
		picked, q := route([]string{"first", "continue"}[i], "harness-new", history, ar)
		if picked.AuthID != ar.ID || picked.Reject {
			t.Fatalf("DSH turn %d denied: %+v", i, picked)
		}
		release, err := acquireExecution(q, testCredential())
		if err != nil {
			t.Fatal(err)
		}
		release()
	}
	picked, _ := route("old-history", "harness-unknown", true, ar)
	if !picked.Reject {
		t.Fatal("unknown DSH history entered AR")
	}
	// Starting with another provider and then dropping history cannot change origin.
	route("external-first", "harness-foreign", false, otherCandidate())
	picked, _ = route("external-replay", "harness-foreign", false, ar)
	if !picked.Reject {
		t.Fatal("foreign DSH conversation entered AR")
	}
	for _, session := range []string{"harness-slot-one", "harness-slot-two"} {
		picked, _ = route(session, session, false, ar)
		if picked.AuthID != ar.ID {
			t.Fatal(picked)
		}
	}
	picked, _ = route("slot-third", "harness-slot-three", false, ar)
	if picked.Reject || picked.AuthID != ar.ID {
		t.Fatal("identified DSH session should queue, not be rejected", picked)
	}
}
func TestExistingForeignAndUnidentifiedHistoryNeverEnterAR(t *testing.T) {
	routeFixture(t)
	ar := arCandidate("ar", "mock-secret", 10)
	for _, s := range []string{"old1", "old2"} {
		picked, _ := routeRequest(s, s, false, ar, otherCandidate())
		if picked.AuthID != "other" {
			t.Fatal(picked)
		}
	}
	picked, _ := routeRequest("missing", "", true, ar, otherCandidate())
	if picked.AuthID != "ar" {
		t.Fatal("clean anonymous first turn should use a transient reservation")
	}
	completeAdmission(sdk.RequestCompletion{RequestID: "missing"})
	picked, _ = routeRequest("alone", "old3", false, ar)
	if !picked.Reject {
		t.Fatal("AR-only old request should reject")
	}
	picked, _ = routeRequest("replay", "old1", true, ar, otherCandidate())
	if picked.AuthID != "other" {
		t.Fatal("dropping history changed origin")
	}
}
func TestMultipleSessionsQueueWithoutProviderFallback(t *testing.T) {
	routeFixture(t)
	a, ra := reserveQueued(t, "a", "a", "mock-secret", 2)
	defer ra()
	b, rb := reserveQueued(t, "b", "b", "mock-secret", 2)
	defer rb()
	c, rc := reserveQueued(t, "c", "c", "mock-secret", 2)
	defer rc()
	if claimExecutionSlot(a) != nil || claimExecutionSlot(b) != nil {
		t.Fatal("first requests not started")
	}
	if claimExecutionSlot(c) != errExecutionBusy {
		t.Fatal("third request did not queue")
	}
	ra()
	if claimExecutionSlot(c) != nil {
		t.Fatal("request did not resume after release")
	}
}

func TestSameSessionRequestsReleaseOnlyTheirOwnLeases(t *testing.T) {
	routeFixture(t)
	a, ra := reserveQueued(t, "a", "same", "mock-secret", 2)
	defer ra()
	b, rb := reserveQueued(t, "b", "same", "mock-secret", 2)
	defer rb()
	if claimExecutionSlot(a) != nil || claimExecutionSlot(b) != nil {
		t.Fatal("per-session cap is still enforced")
	}
	ra()
	completeAdmission(sdk.RequestCompletion{RequestID: "a"})
	_ = withExecutionLedger(func(s *routingLedger) error {
		if len(s.Leases) != 1 || !upstreamActive(s.Leases["b"]) {
			t.Fatal("released someone else's slot")
		}
		return nil
	})
}

func TestDuplicateCredentialsCanAdmitButShareExecutionQueue(t *testing.T) {
	routeFixture(t)
	a := arCandidate("ar-a", "mock-secret", 10)
	b := arCandidate("ar-b", "mock-secret", 10)
	for _, id := range []string{"one", "two", "three"} {
		picked, _ := routeRequest(id, id, true, a, b, otherCandidate())
		if picked.Reject || picked.AuthID == "other" {
			t.Fatal("request capacity must be enforced at execution", picked)
		}
	}
}

func TestDifferentKeyHasSeparateCapacity(t *testing.T) {
	routeFixture(t)
	a := arCandidate("ar-a", "mock-secret", 10)
	b := arCandidate("ar-b", "mock-second", 10)
	routeRequest("1", "1", true, a)
	routeRequest("2", "2", true, a)
	p, _ := routeRequest("3", "3", true, a, b, otherCandidate())
	if p.AuthID != "ar-b" {
		t.Fatal(p)
	}
}
func TestConcurrentAdmissionKeepsIdentifiedSessionsInAR(t *testing.T) {
	routeFixture(t)
	ar := arCandidate("ar", "mock-secret", 10)
	var wg sync.WaitGroup
	var mu sync.Mutex
	count := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := time.Unix(int64(n), 0).String()
			p, _ := routeRequest(id, id, true, ar, otherCandidate())
			if p.AuthID == "ar" {
				mu.Lock()
				count++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if count != 20 {
		t.Fatalf("admitted %d sessions", count)
	}
}
func TestRestartClearsLeasesButRetainsForeignOrigin(t *testing.T) {
	routeFixture(t)
	a := arCandidate("ar", "mock-secret", 10)
	routeRequest("old", "foreign", false, a, otherCandidate())
	routeRequest("active", "ar-session", true, a)
	e := withRoutingLedger(func(s *routingLedger) error { s.Process = "previous-process"; return nil })
	if e != nil {
		t.Fatal(e)
	}
	p, _ := routeRequest("again", "foreign", true, a, otherCandidate())
	if p.AuthID != "other" {
		t.Fatal("foreign origin lost")
	}
	var count int
	_ = withRoutingLedger(func(s *routingLedger) error { count = len(s.Leases); return nil })
	if count != 0 {
		t.Fatal("stale lease retained")
	}
}
func TestCorruptStateFailsClosedForAR(t *testing.T) {
	routeFixture(t)
	_ = os.WriteFile(routingPath, []byte("invalid"), 0600)
	a := arCandidate("ar", "mock-secret", 10)
	p, _ := routeRequest("x", "x", true, a, otherCandidate())
	if p.AuthID != "other" {
		t.Fatal(p)
	}
	p, _ = routeRequest("y", "y", true, a)
	if !p.Reject {
		t.Fatal(p)
	}
}
func TestNonARPoolsDelegateWithoutChoosing(t *testing.T) {
	routeFixture(t)
	p, _ := routeRequest("other", "other", true, otherCandidate())
	if p.Handled {
		t.Fatal("changed unrelated routing")
	}
	// The same window must stay external even if its next call has no assistant history.
	p, _ = routeRequest("later", "other", true, arCandidate("ar", "mock-secret", 10), otherCandidate())
	if p.AuthID != "other" {
		t.Fatal("external-only origin was not recorded", p)
	}
}
func TestNativeAffinityOverridesARPriority(t *testing.T) {
	routeFixture(t)
	hostCall = func(method string, value any, out any) error {
		switch method {
		case "host.affinity.lookup":
			*out.(*sdk.HostAffinityLookupResponse) = sdk.HostAffinityLookupResponse{Status: "bound", AuthIndex: "foreign-index"}
		case "host.auth.get_runtime":
			*out.(*sdk.HostAuthGetRuntimeResponse) = sdk.HostAuthGetRuntimeResponse{Auth: sdk.HostAuthFileEntry{ID: "other", Provider: "codex"}}
		}
		return nil
	}
	p, _ := routeRequest("r", "existing", true, arCandidate("ar", "mock-secret", 100), otherCandidate())
	if p.AuthID != "other" {
		t.Fatal(p)
	}
}
func TestFreshDetectionIsConservative(t *testing.T) {
	for _, raw := range []string{`{"input":[{"type":"reasoning"}]}`, `{"previous_response_id":"id","input":"hello"}`, `{"input":[{"role":"user","content":"x","tool_calls":[]}]}`, `{"input":[{"role":"tool","content":"output"}]}`, `{"input":[{"type":"compaction","encrypted_content":"opaque"}]}`} {
		if freshConversation([]byte(raw), nil) {
			t.Fatal(raw)
		}
	}
}

func TestExplicitNewInitializationReplacesStaleForeignOrigin(t *testing.T) {
	routeFixture(t)
	// Simulate a previous failed attempt that CPA recorded as native Codex.
	raw := "stale-session"
	digestID := digest("test-caller\x00" + raw)
	if err := withRoutingLedger(func(s *routingLedger) error {
		s.Origins[digestID] = sessionOrigin{Provider: "codex", Foreign: true}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"input":[{"type":"additional_tools","role":"developer","tools":[]},{"role":"user","content":"hello"}]}`)
	headers := http.Header{"Session-Id": {raw}}
	metadata := map[string]any{"caller_scope": "test-caller", "history_initialization": "new"}
	before := beforeAdmission(sdk.RequestInterceptRequest{RequestID: "retry-new", Headers: headers, Body: body, Metadata: metadata})
	headers.Set(admissionHeader, before.Headers.Get(admissionHeader))
	picked := pickAdmission(sdk.SchedulerPickRequest{Model: "test-model", Options: sdk.SchedulerOptions{Headers: headers, Metadata: metadata}, Candidates: []sdk.SchedulerAuthCandidate{arCandidate("ar", "mock-secret", 15), otherCandidate()}})
	if picked.AuthID != "ar" || picked.Reject {
		t.Fatalf("explicit new initialization did not replace stale foreign origin: %+v", picked)
	}
}

func TestColdResumeCannotReplaceForeignOrigin(t *testing.T) {
	routeFixture(t)
	raw := "stale-session"
	digestID := digest("test-caller\x00" + raw)
	_ = withRoutingLedger(func(s *routingLedger) error {
		s.Origins[digestID] = sessionOrigin{Provider: "codex", Foreign: true}
		return nil
	})
	body := []byte(`{"input":[{"role":"user","content":"hello"}]}`)
	headers := http.Header{"Session-Id": {raw}}
	metadata := map[string]any{"caller_scope": "test-caller", "history_initialization": "cold_resume"}
	before := beforeAdmission(sdk.RequestInterceptRequest{RequestID: "resume-old", Headers: headers, Body: body, Metadata: metadata})
	headers.Set(admissionHeader, before.Headers.Get(admissionHeader))
	picked := pickAdmission(sdk.SchedulerPickRequest{Model: "test-model", Options: sdk.SchedulerOptions{Headers: headers, Metadata: metadata}, Candidates: []sdk.SchedulerAuthCandidate{arCandidate("ar", "mock-secret", 15), otherCandidate()}})
	if picked.AuthID != "other" || picked.Reject {
		t.Fatalf("cold resume entered AR: %+v", picked)
	}
}
func TestOriginLedgerContainsNoConversationOrKey(t *testing.T) {
	routeFixture(t)
	routeRequest("r", "sensitive-session-id", true, arCandidate("ar", "mock-secret", 10))
	raw, e := os.ReadFile(routingPath)
	if e != nil {
		t.Fatal(e)
	}
	var s routingLedger
	if json.Unmarshal(raw, &s) != nil {
		t.Fatal("invalid ledger")
	}
	for _, forbidden := range []string{"mock-secret", "sensitive-session-id", "hello"} {
		if contains := string(raw); len(contains) > 0 && stringsContains(contains, forbidden) {
			t.Fatal("private material in state")
		}
	}
}
func stringsContains(a, b string) bool {
	for i := 0; i+len(b) <= len(a); i++ {
		if a[i:i+len(b)] == b {
			return true
		}
	}
	return false
}
