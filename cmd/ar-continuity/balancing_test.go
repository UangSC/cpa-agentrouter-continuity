package main

import (
	"fmt"
	"sync"
	"testing"

	"github.com/UangSC/cpa-agentrouter-continuity/internal/sdk"
)

func TestNewSessionsBalanceRealKeysBeforeFillingSlots(t *testing.T) {
	routeFixture(t)
	a := arCandidate("ar-a", "mock-secret", 10)
	a.Attributes["weight"] = "1000000"
	duplicate := arCandidate("ar-a-duplicate", "mock-secret", 10)
	b := arCandidate("ar-b", "mock-second", 10)
	previous := ""
	counts := map[string]int{}
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("balanced-%d", i)
		picked, _ := routeRequest(id, id, true, a, duplicate, b)
		if picked.Reject {
			t.Fatal(picked)
		}
		key := "a"
		if picked.AuthID == b.ID {
			key = "b"
		}
		if key == previous {
			t.Fatalf("filled Key %s twice before distributing: %+v", key, counts)
		}
		counts[key]++
		previous = key
	}
	if counts["a"] != 2 || counts["b"] != 2 {
		t.Fatal(counts)
	}
}

func TestSequentialSessionsRotateAndContinuationKeepsCursor(t *testing.T) {
	routeFixture(t)
	a := arCandidate("ar-a", "mock-secret", 10)
	b := arCandidate("ar-b", "mock-second", 10)
	previous, first := "", ""
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("sequential-%d", i)
		picked, q := routeRequest(id, id, true, a, b)
		if picked.AuthID == previous || picked.Reject {
			t.Fatalf("idle sessions did not rotate: %+v", picked)
		}
		if i == 0 {
			first = picked.AuthID
		}
		credential := testCredential()
		if picked.AuthID == b.ID {
			credential.APIKey = "mock-second"
		}
		release, err := acquireExecution(q, credential)
		if err != nil {
			t.Fatal(err)
		}
		release()
		previous = picked.AuthID
	}
	continued, _ := routeRequest("continuation", "sequential-0", false, a, b)
	if continued.AuthID != first {
		t.Fatal("continuation changed its Key", continued)
	}
	completeAdmission(sdk.RequestCompletion{RequestID: "continuation"})
	next, _ := routeRequest("after-continuation", "after-continuation", true, a, b)
	if next.AuthID != first {
		t.Fatal("continuation advanced the new-session rotation", next)
	}
}

func TestPinnedSessionNeverFallsBackToAnotherKeyOrProvider(t *testing.T) {
	routeFixture(t)
	a := arCandidate("ar-a", "mock-secret", 10)
	b := arCandidate("ar-b", "mock-second", 100)
	picked, _ := routeRequest("start-pinned", "pinned-session", true, a)
	if picked.AuthID != a.ID {
		t.Fatal(picked)
	}
	for _, candidates := range [][]sdk.SchedulerAuthCandidate{{b}, {b, otherCandidate()}, {otherCandidate()}} {
		picked, _ = routeRequest("missing-original", "pinned-session", false, candidates...)
		if !picked.Reject {
			t.Fatal("original Key unavailable but request changed identity", picked)
		}
	}
	picked, _ = routeRequest("original-returned", "pinned-session", false, a, b, otherCandidate())
	if picked.AuthID != a.ID {
		t.Fatal("lost original Key binding after rejection", picked)
	}
}

func TestConcurrentNewSessionsStayBalancedWithinLimits(t *testing.T) {
	routeFixture(t)
	keys := []sdk.SchedulerAuthCandidate{arCandidate("ar-a", "mock-a", 10), arCandidate("ar-b", "mock-b", 10), arCandidate("ar-c", "mock-c", 10)}
	for i := range keys {
		keys[i].Attributes["ar_max_concurrent_sessions"] = "10"
	}
	keys[0].Attributes["weight"] = "1000000"
	var workers sync.WaitGroup
	var mu sync.Mutex
	counts := map[string]int{}
	for i := 0; i < 25; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			id := fmt.Sprintf("concurrent-balanced-%d", i)
			picked, _ := routeRequest(id, id, true, keys...)
			mu.Lock()
			counts[picked.AuthID]++
			mu.Unlock()
		}(i)
	}
	workers.Wait()
	for _, key := range keys {
		if counts[key.ID] < 8 || counts[key.ID] > 9 {
			t.Fatalf("concurrent distribution is not balanced: %+v", counts)
		}
	}
}
