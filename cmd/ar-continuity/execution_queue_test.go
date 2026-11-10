package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/UangSC/cpa-agentrouter-continuity/internal/sdk"
)

func reserveQueued(t *testing.T, id, session, key string, total int) (execution, func()) {
	t.Helper()
	c := testCredential()
	c.MaxConcurrentRequests = &total
	picked, q := routeRequest(id, session, true, arCandidate("ar", key, 10))
	if picked.AuthID != "ar" {
		t.Fatal(picked)
	}
	c.APIKey = key
	release, err := acquireExecution(q, c)
	if err != nil {
		t.Fatal(err)
	}
	q.admitted = true
	return q, release
}

func TestExecutionQueueFIFOAndKeyIsolation(t *testing.T) {
	routeFixture(t)
	a, releaseA := reserveQueued(t, "a", "same", "mock-secret", 2)
	b, releaseB := reserveQueued(t, "b", "same", "mock-secret", 2)
	defer releaseB()
	c, releaseC := reserveQueued(t, "c", "other", "mock-secret", 2)
	defer releaseC()
	if err := claimExecutionSlot(b); !errors.Is(err, errExecutionBusy) {
		t.Fatal("FIFO", err)
	}
	if err := claimExecutionSlot(a); err != nil {
		t.Fatal(err)
	}
	if err := claimExecutionSlot(b); err != nil {
		t.Fatal("same-session requests should share the Key cap", err)
	}
	if err := claimExecutionSlot(c); !errors.Is(err, errExecutionBusy) {
		t.Fatal("Key cap bypassed", err)
	}
	d, releaseD := reserveQueued(t, "d", "independent", "mock-second", 1)
	defer releaseD()
	if err := claimExecutionSlot(d); err != nil {
		t.Fatal("Key isolation", err)
	}
	releaseA()
	if err := claimExecutionSlot(c); err != nil {
		t.Fatal("release did not wake next", err)
	}
}

func TestExecutionQueueConcurrentClaimsRespectKeyCap(t *testing.T) {
	routeFixture(t)
	var queue []execution
	for i := 0; i < 20; i++ {
		q, release := reserveQueued(t, fmt.Sprint(i), "same", "mock-secret", 3)
		defer release()
		queue = append(queue, q)
	}
	var wg sync.WaitGroup
	for i := range queue {
		wg.Add(1)
		go func(q execution) { defer wg.Done(); _ = claimExecutionSlot(q) }(queue[i])
	}
	wg.Wait()
	for _, q := range queue {
		_ = claimExecutionSlot(q)
	}
	if err := withExecutionLedger(func(s *routingLedger) error {
		n := 0
		for _, lease := range s.Leases {
			if upstreamActive(lease) {
				n++
			}
		}
		if n != 3 {
			t.Fatalf("active %d, want 3", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionQueueCanceledWaitDoesNotStartAndReleases(t *testing.T) {
	routeFixture(t)
	a, releaseA := reserveQueued(t, "a", "same", "mock-secret", 1)
	defer releaseA()
	if err := claimExecutionSlot(a); err != nil {
		t.Fatal(err)
	}
	b, releaseB := reserveQueued(t, "b", "same", "mock-secret", 1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := waitExecutionSlot(ctx, b, testCredential()); asRPCError(err).Code != "ar_concurrency_wait_timeout" {
		t.Fatal(err)
	}
	releaseB()
	if err := withRoutingLedger(func(s *routingLedger) error {
		if len(s.Leases) != 1 {
			t.Fatal("leaked queue reservation")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionQueueLegacyGenerationCountsAndMissingStateFailsClosed(t *testing.T) {
	routeFixture(t)
	a, releaseA := reserveQueued(t, "a", "same", "mock-secret", 1)
	defer releaseA()
	b, releaseB := reserveQueued(t, "b", "same", "mock-secret", 1)
	defer releaseB()
	_ = withExecutionLedger(func(s *routingLedger) error { delete(s.Leases, "a"); return nil })
	if err := claimExecutionSlot(b); !errors.Is(err, errExecutionBusy) {
		t.Fatal("legacy request not counted", err)
	}
	if err := claimExecutionSlot(a); asRPCError(err).Code != "ar_session_ineligible" {
		t.Fatal(err)
	}
}

func TestNoCapacityRecoveryPreservesHistoryAndNoReplayAfterOutput(t *testing.T) {
	failure := sse(`{"type":"response.failed","response":{"error":{"code":"no_capacity","message":"No capacity available"}}}`)
	for _, afterOutput := range []bool{false, true} {
		t.Run(fmt.Sprint(afterOutput), func(t *testing.T) {
			frames := []string{sse(`{"type":"response.created"}`)}
			if afterOutput {
				frames = append(frames, sse(`{"type":"response.output_text.delta","delta":"Keep"}`))
			}
			frames = append(frames, failure)
			m := &mockHost{attempts: []attempt{{200, "", frames}, {200, "", []string{sse(completed)}}}}
			withMock(t, m)
			body := []byte(`{"input":[{"type":"reasoning","encrypted_content":"unchanged"}]}`)
			runStreaming(context.Background(), execution{StreamID: "down"}, testCredential(), body)
			if afterOutput {
				if m.started != 1 {
					t.Fatal("output replayed")
				}
			} else if m.started != 2 || !bytes.Equal(m.bodies[0], m.bodies[1]) || !bytes.Contains(bytes.Join(m.emitted, nil), []byte("response.completed")) {
				t.Fatal("capacity recovery failed")
			}
		})
	}
	if classify(0, []byte(`{"type":"error","error":{"code":"no_capacity"}}`)) != "capacity" {
		t.Fatal("SSE classification")
	}
	if safeError(502, []byte(`{"error":{"code":"no_capacity"}}`)).HTTPStatus != 503 {
		t.Fatal("capacity status")
	}
	if retryDelay("capacity", 0, nil) < 5*time.Second {
		t.Fatal("capacity backoff too short")
	}
}

func TestRequestLimitsManagementPreservationAndValidation(t *testing.T) {
	m := setupManagementMock(t)
	edit := credentialEdit{Index: "ar-index", Models: []sdk.ModelInfo{{ID: "model"}}}
	total := 3
	edit.MaxConcurrentRequests = &total
	save := func() sdk.ManagementResponse {
		raw, _ := json.Marshal(edit)
		return saveManagedCredential(managementRequest{ManagementRequest: sdk.ManagementRequest{Method: "PUT", Body: raw}})
	}
	if save().StatusCode != 200 {
		t.Fatal("save failed")
	}
	edit.MaxConcurrentRequests = nil
	if save().StatusCode != 200 {
		t.Fatal("legacy update failed")
	}
	c, err := decodeCredential(m.document)
	if err != nil || requestLimit(c) != 3 {
		t.Fatal("settings reset", err)
	}
	for _, n := range []int{0, -1, 101} {
		edit.MaxConcurrentRequests = &n
		if save().StatusCode != 400 {
			t.Fatal("invalid Key limit")
		}
	}
}

func TestNoCapacityJSONRecoveryAndRetryBoundaries(t *testing.T) {
	for _, status := range []int{0, 200, 500, 503} {
		if kind := classify(status, []byte(`{"error":{"code":"no_capacity"}}`)); kind != "capacity" {
			t.Fatalf("status %d: %s", status, kind)
		}
	}
	if classify(401, []byte(`{"error":{"code":"no_capacity"}}`)) != "" || classify(500, []byte(`{"error":{"code":"no_capacity","message":"insufficient_quota"}}`)) != "" {
		t.Fatal("authentication/quota must remain terminal")
	}
	for _, maxRetries := range []int{0, 1} {
		t.Run(fmt.Sprint(maxRetries), func(t *testing.T) {
			m := &mockHost{attempts: []attempt{{200, "", []string{`{"error":{"code":"no_capacity"}}`}}, {200, "", []string{`{"status":"completed","output":[]}`}}}}
			withMock(t, m)
			c := testCredential()
			c.MaxRetries = &maxRetries
			raw, err := runJSON(context.Background(), execution{}, c, []byte(`{"input":"same"}`))
			if maxRetries == 0 {
				if err == nil || asRPCError(err).HTTPStatus != 503 || m.started != 1 {
					t.Fatal("zero retries ignored", err)
				}
			} else if err != nil || m.started != 2 || !bytes.Contains(raw, []byte("completed")) {
				t.Fatal("JSON capacity not recovered", err)
			}
		})
	}
}

func TestJSONExecutionWaitsBeforeSendingUpstream(t *testing.T) {
	routeFixture(t)
	a, releaseA := reserveQueued(t, "a", "same", "mock-secret", 1)
	if err := claimExecutionSlot(a); err != nil {
		t.Fatal(err)
	}
	b, releaseB := reserveQueued(t, "b", "same", "mock-secret", 1)
	defer releaseB()
	m := &mockHost{attempts: []attempt{{200, "", []string{`{"status":"completed","output":[]}`}}}}
	withMock(t, m)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := runJSON(ctx, b, testCredential(), []byte(`{}`)); err == nil || m.started != 0 {
		t.Fatal("queued JSON reached upstream", err)
	}
	releaseA()
	if _, err := runJSON(context.Background(), b, testCredential(), []byte(`{}`)); err != nil || m.started != 1 {
		t.Fatal("released JSON did not start", err)
	}
}

func TestDuplicateCredentialsShareRequestCap(t *testing.T) {
	routeFixture(t)
	a, releaseA := reserveQueued(t, "a", "same", "mock-secret", 1)
	defer releaseA()
	b, releaseB := reserveQueued(t, "b", "other", "mock-secret", 3)
	defer releaseB()
	if err := withRoutingLedger(func(s *routingLedger) error {
		own := s.Leases["b"]
		own.AuthID = "duplicate"
		s.Leases["b"] = own
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_ = withExecutionLedger(func(s *routingLedger) error {
		own := s.Leases["b"]
		own.AuthID = "duplicate"
		s.Leases["b"] = own
		return nil
	})
	b.AuthID = "duplicate"
	if err := claimExecutionSlot(a); err != nil {
		t.Fatal(err)
	}
	if err := claimExecutionSlot(b); !errors.Is(err, errExecutionBusy) {
		t.Fatal("duplicate credential multiplied Key capacity", err)
	}
	releaseA()
	if err := claimExecutionSlot(b); err != nil {
		t.Fatal("duplicate did not wake", err)
	}
}

func TestQueuedChildKeepsKeyAfterCompletionAndRepeatedRelease(t *testing.T) {
	routeFixture(t)
	a, releaseA := reserveQueued(t, "parent-request", "child-session", "mock-secret", 1)
	b, releaseB := reserveQueued(t, "child-request", "child-session", "mock-secret", 1)
	if err := claimExecutionSlot(a); err != nil {
		t.Fatal(err)
	}
	if err := claimExecutionSlot(b); !errors.Is(err, errExecutionBusy) {
		t.Fatal(err)
	}
	releaseA()
	releaseA()
	if err := claimExecutionSlot(b); err != nil {
		t.Fatal(err)
	}
	releaseB()
	releaseB()
	for _, ledger := range []func(func(*routingLedger) error) error{withRoutingLedger, withExecutionLedger} {
		if err := ledger(func(s *routingLedger) error {
			if len(s.Leases) != 0 {
				t.Fatal("request slots retained after completion")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Idle sessions retain only their original Key identity, never a slot.
	first := arCandidate("ar", "mock-secret", 10)
	second := arCandidate("other-key", "mock-second", 100)
	picked, _ := routeRequest("resume-child", "child-session", false, first, second)
	if picked.AuthID != "ar" {
		t.Fatal("child changed Key after becoming idle", picked)
	}
	completeAdmission(sdk.RequestCompletion{RequestID: "resume-child"})
}
