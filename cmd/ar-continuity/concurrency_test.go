package main

import (
	"encoding/json"
	"github.com/UangSC/cpa-agentrouter-continuity/internal/sdk"
	"testing"
)

func TestRequestLimitMigrationAndValidation(t *testing.T) {
	c := testCredential()
	if requestLimit(c) != 2 {
		t.Fatal("default cap")
	}
	old, explicit := 3, 5
	c.MaxConcurrentSessions = &old
	if requestLimit(c) != 3 {
		t.Fatal("legacy cap not migrated")
	}
	c.MaxConcurrentRequests = &explicit
	if requestLimit(c) != 5 {
		t.Fatal("explicit request cap did not win")
	}
	for _, n := range []int{-1, 0, 1, 100, 101} {
		c.MaxConcurrentRequests = &n
		if (validateCredential(c) == nil) != (n >= 1 && n <= 100) {
			t.Fatal("validation", n)
		}
	}
}

func TestManagementMigratesLegacyLimitsWithoutChangingKey(t *testing.T) {
	m := setupManagementMock(t)
	var stored map[string]any
	_ = json.Unmarshal(m.document, &stored)
	stored["max_concurrent_sessions"] = 3
	stored["max_concurrent_requests_per_session"] = 1
	m.document, _ = json.Marshal(stored)
	edit := credentialEdit{Index: "ar-index", Models: []sdk.ModelInfo{{ID: "model"}}}
	raw, _ := json.Marshal(edit)
	result := saveManagedCredential(managementRequest{ManagementRequest: sdk.ManagementRequest{Method: "PUT", Body: raw}})
	if result.StatusCode != 200 {
		t.Fatal(string(result.Body))
	}
	stored = map[string]any{}
	_ = json.Unmarshal(m.document, &stored)
	if stored["max_concurrent_requests"] != float64(3) || stored["api_key"] != "mock-secret" {
		t.Fatal("migration lost cap or key")
	}
	if _, ok := stored["max_concurrent_sessions"]; ok {
		t.Fatal("obsolete session cap persisted")
	}
	if _, ok := stored["max_concurrent_requests_per_session"]; ok {
		t.Fatal("obsolete per-session cap persisted")
	}
}
