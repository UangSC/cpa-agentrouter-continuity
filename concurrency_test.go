package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"testing"

	"github.com/UangSC/cpa-agentrouter-continuity/internal/sdk"
)

func TestSessionLimitDefaultsAndValidation(t *testing.T) {
	c := testCredential()
	if sessionLimit(c) != 2 {
		t.Fatal("legacy credentials must retain two sessions")
	}
	for _, n := range []int{-1, 0, 1, 3, 100, 101} {
		c.MaxConcurrentSessions = &n
		err := validateCredential(c)
		valid := n >= 1 && n <= 100
		if (err == nil) != valid {
			t.Fatalf("limit %d validation: %v", n, err)
		}
		if valid && authData(c, nil, "").Attributes["ar_max_concurrent_sessions"] != strconv.Itoa(n) {
			t.Fatal("configured limit was not delivered to the scheduler")
		}
	}
}

func TestConfiguredLimitsUnderConcurrentAdmission(t *testing.T) {
	for _, limit := range []int{1, 3, 7} {
		t.Run(strconv.Itoa(limit), func(t *testing.T) {
			routeFixture(t)
			candidate := arCandidate("ar", "mock-secret", 10)
			candidate.Attributes["ar_max_concurrent_sessions"] = strconv.Itoa(limit)
			var wg sync.WaitGroup
			results := make(chan bool, 20)
			for i := 0; i < 20; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					id := fmt.Sprintf("request-%d", i)
					picked, _ := routeRequest(id, id, true, candidate, otherCandidate())
					results <- picked.AuthID == "ar"
				}(i)
			}
			wg.Wait()
			close(results)
			count := 0
			for admitted := range results {
				if admitted {
					count++
				}
			}
			if count != limit {
				t.Fatalf("admitted %d, expected limit %d", count, limit)
			}
		})
	}
}

func TestLimitChangePreservesExistingSessions(t *testing.T) {
	routeFixture(t)
	ar := arCandidate("ar", "mock-secret", 10)
	ar.Attributes["ar_max_concurrent_sessions"] = "1"
	_, original := routeRequest("first", "session-1", true, ar)
	release, err := acquireExecution(original, testCredential())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if picked, _ := routeRequest("blocked", "session-blocked", true, ar); !picked.Reject {
		t.Fatal("limit one admitted a second session")
	}
	ar.Attributes["ar_max_concurrent_sessions"] = "3"
	for i := 2; i <= 3; i++ {
		id := fmt.Sprintf("session-%d", i)
		if picked, _ := routeRequest(id, id, true, ar); picked.AuthID != "ar" {
			t.Fatal("raising the limit did not admit the next session")
		}
	}
	ar.Attributes["ar_max_concurrent_sessions"] = "1"
	if picked, _ := routeRequest("new-blocked", "session-4", true, ar); !picked.Reject {
		t.Fatal("lowering the limit admitted a new session")
	}
	if picked, _ := routeRequest("continuation", "session-1", false, ar); picked.AuthID != "ar" {
		t.Fatal("lowering the limit interrupted an existing active session")
	}
}

func TestDuplicateKeyUsesSharedCountWithConfiguredLimit(t *testing.T) {
	routeFixture(t)
	a := arCandidate("a", "mock-secret", 10)
	b := arCandidate("b", "mock-secret", 10)
	a.Attributes["ar_max_concurrent_sessions"] = "1"
	b.Attributes["ar_max_concurrent_sessions"] = "3"
	routeRequest("one", "one", true, a)
	for _, id := range []string{"two", "three"} {
		if picked, _ := routeRequest(id, id, true, b); picked.AuthID != "b" {
			t.Fatal(picked)
		}
	}
	if picked, _ := routeRequest("four", "four", true, b); !picked.Reject {
		t.Fatal("duplicate configuration got a separate session pool")
	}
	if picked, _ := routeRequest("other", "other", true, a); !picked.Reject {
		t.Fatal("smaller per-credential limit was ignored")
	}
}

func TestManagementSessionLimitSaveAndOmittedPreservation(t *testing.T) {
	m := setupManagementMock(t)
	edit := credentialEdit{Index: "ar-index", Label: "custom limit", Models: []sdk.ModelInfo{{ID: "model"}}, MaxRetries: 2, RetryBudgetSeconds: 180}
	limit := 5
	edit.MaxConcurrentSessions = &limit
	save := func() sdk.ManagementResponse {
		raw, _ := json.Marshal(edit)
		return saveManagedCredential(managementRequest{ManagementRequest: sdk.ManagementRequest{Method: "PUT", Body: raw}})
	}
	if result := save(); result.StatusCode != 200 {
		t.Fatalf("save: %s", result.Body)
	}
	var stored credential
	_ = json.Unmarshal(m.document, &stored)
	if sessionLimit(stored) != 5 || stored.APIKey != "mock-secret" {
		t.Fatal("limit or key not preserved")
	}
	edit.MaxConcurrentSessions = nil
	edit.Disabled = true
	if result := save(); result.StatusCode != 200 {
		t.Fatalf("legacy edit: %s", result.Body)
	}
	_ = json.Unmarshal(m.document, &stored)
	if sessionLimit(stored) != 5 || !stored.Disabled {
		t.Fatal("omitted setting reset the configured limit")
	}
	for _, invalid := range []int{0, -1, 101} {
		edit.MaxConcurrentSessions = &invalid
		before := m.saves
		if result := save(); result.StatusCode != 400 || m.saves != before {
			t.Fatal("invalid limit was saved")
		}
	}
}
