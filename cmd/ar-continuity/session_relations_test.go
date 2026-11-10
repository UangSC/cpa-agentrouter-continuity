package main

import (
	"github.com/UangSC/cpa-agentrouter-continuity/internal/sdk"
	"net/http"
	"testing"
)

func routeChild(id string, headers http.Header, body []byte, meta map[string]any, candidates ...sdk.SchedulerAuthCandidate) sdk.SchedulerPickResponse {
	before := beforeAdmission(sdk.RequestInterceptRequest{RequestID: id, Headers: headers, Body: body, Metadata: meta})
	headers.Set(admissionHeader, before.Headers.Get(admissionHeader))
	return pickAdmission(sdk.SchedulerPickRequest{Model: "test-model", Options: sdk.SchedulerOptions{Headers: headers, Metadata: meta}, Candidates: candidates})
}

func TestExplicitChildInheritsParentKeyWithHistoryAndKeepsOwnBinding(t *testing.T) {
	for _, carrier := range []string{"header", "body", "host", "codex-thread", "codex-agent"} {
		t.Run(carrier, func(t *testing.T) {
			routeFixture(t)
			a, b := arCandidate("parent-key", "mock-parent", 10), arCandidate("other-key", "mock-other", 100)
			routeRequest("parent-first", "parent", true, a)
			completeAdmission(sdk.RequestCompletion{RequestID: "parent-first"})
			headers := http.Header{"Session-Id": {"child"}}
			meta := map[string]any{"caller_scope": "test-caller"}
			body := []byte(`{"input":[{"type":"reasoning","encrypted_content":"unchanged-parent-history"}]}`)
			switch carrier {
			case "header":
				headers.Set("X-Parent-Session-Id", "parent")
			case "body":
				body = []byte(`{"parent_session_id":"parent","input":[{"type":"reasoning","encrypted_content":"unchanged-parent-history"}]}`)
			case "host":
				meta["parent_session_id"] = "codex:parent"
			case "codex-thread":
				headers.Set("Session-Id", "parent")
				headers.Set("Thread-Id", "child")
			case "codex-agent":
				headers.Set("Session-Id", "parent")
				headers.Set("X-Openai-Subagent", "reviewer")
				headers.Set("X-Codex-Turn-Metadata", `{"agent_name":"/root/reviewer","subagent_kind":"thread_spawn"}`)
			}
			picked := routeChild("child-first", headers, body, meta, a, b, otherCandidate())
			if picked.AuthID != a.ID || picked.Reject {
				t.Fatal("child did not inherit", picked)
			}
			hash, raw := stableSession(headers, body, meta)
			if raw == "parent" || hash == digest("test-caller\x00parent") {
				t.Fatal("parent and child were merged")
			}
			completeAdmission(sdk.RequestCompletion{RequestID: "child-first"})
			picked, _ = routeRequest("child-resume", raw, false, a, b)
			if picked.AuthID != a.ID {
				t.Fatal("idle child lost sticky Key", picked)
			}
			picked, _ = routeRequest("child-no-key", raw, false, b, otherCandidate())
			if !picked.Reject {
				t.Fatal("child changed Key when original unavailable", picked)
			}
		})
	}
}

func TestChildOwnBindingWinsAndParentUnavailableStaysPinned(t *testing.T) {
	routeFixture(t)
	a, b := arCandidate("a", "mock-a", 10), arCandidate("b", "mock-b", 10)
	routeRequest("p", "parent", true, a)
	routeRequest("c", "existing-child", true, b)
	meta := map[string]any{"caller_scope": "test-caller", "parent_session_id": "parent"}
	old := routeChild("old-child", http.Header{"Session-Id": {"existing-child"}}, []byte(`{"input":"continue"}`), meta, a, b)
	if old.AuthID != b.ID {
		t.Fatal("parent replaced existing child binding", old)
	}
	newChild := routeChild("new-child", http.Header{"Session-Id": {"new-child"}}, []byte(`{"input":"start"}`), meta, b, otherCandidate())
	if !newChild.Reject {
		t.Fatal("unavailable parent Key fell back", newChild)
	}
	resumed, _ := routeRequest("new-child-retry", "new-child", false, a, b)
	if resumed.AuthID != a.ID {
		t.Fatal("inherited binding not retained after rejection", resumed)
	}
}

func TestParentReferencesCannotCrossCallerOrForeignOrigin(t *testing.T) {
	routeFixture(t)
	a := arCandidate("ar", "mock-a", 10)
	routeRequest("p", "parent", true, a)
	oldBody := []byte(`{"input":[{"role":"assistant","content":"inherited"}]}`)
	meta := map[string]any{"caller_scope": "different-caller", "parent_session_id": "parent"}
	if p := routeChild("cross-scope", http.Header{"Session-Id": {"child"}}, oldBody, meta, a); !p.Reject {
		t.Fatal("cross-caller inheritance", p)
	}
	routeRequest("foreign-parent", "foreign", true, otherCandidate())
	meta = map[string]any{"caller_scope": "test-caller", "history_initialization": "new"}
	headers := http.Header{"Session-Id": {"foreign-child"}, "X-Parent-Session-Id": {"foreign"}}
	if p := routeChild("foreign-child", headers, []byte(`{"input":"fresh-looking"}`), meta, a); !p.Reject {
		t.Fatal("foreign parent entered AR", p)
	}
	if p := routeChild("foreign-child-retry", headers, []byte(`{"input":"fresh-looking"}`), meta, a); !p.Reject {
		t.Fatal("foreign child bypassed inheritance protection on retry", p)
	}
}

func TestConflictingReferencesAndSimilarTextDoNotInferParent(t *testing.T) {
	routeFixture(t)
	a := arCandidate("ar", "mock-a", 10)
	routeRequest("p", "parent", true, a)
	meta := map[string]any{"caller_scope": "test-caller", "parent_session_id": "different"}
	if p := routeChild("conflict", http.Header{"Session-Id": {"child"}, "X-Parent-Session-Id": {"parent"}}, []byte(`{"input":"start"}`), meta, a); !p.Reject {
		t.Fatal("conflicting parent accepted", p)
	}
	meta = map[string]any{"caller_scope": "test-caller"}
	body := []byte(`{"input":[{"role":"assistant","content":"parent_session_id: parent, copied history"}]}`)
	if p := routeChild("guessed", http.Header{"Session-Id": {"child-unknown"}}, body, meta, a); !p.Reject {
		t.Fatal("text used as parent identity", p)
	}
}

func TestParentResolvedByHostAtSchedulerStage(t *testing.T) {
	routeFixture(t)
	a := arCandidate("ar", "mock-a", 10)
	routeRequest("parent-start", "parent", true, a)
	headers := http.Header{"Session-Id": {"late-child"}}
	meta := map[string]any{"caller_scope": "test-caller"}
	before := beforeAdmission(sdk.RequestInterceptRequest{RequestID: "late-child", Headers: headers, Body: []byte(`{"input":[{"role":"assistant","content":"history"}]}`), Metadata: meta})
	headers.Set(admissionHeader, before.Headers.Get(admissionHeader))
	meta["parent_session_id"] = "codex:parent"
	picked := pickAdmission(sdk.SchedulerPickRequest{Model: "test-model", Options: sdk.SchedulerOptions{Headers: headers, Metadata: meta}, Candidates: []sdk.SchedulerAuthCandidate{a}})
	if picked.AuthID != a.ID || picked.Reject {
		t.Fatal("host-resolved parent not inherited", picked)
	}
}
