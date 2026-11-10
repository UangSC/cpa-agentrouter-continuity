package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAdmissionDiagnosticsBoundedPrivateAndIsolated(t *testing.T) {
	admissionDiagnostics.Lock()
	admissionDiagnostics.entries = nil
	admissionDiagnostics.Unlock()
	r := admissionRequest{ID: "private-request", CallerScope: "private-caller", Session: digest("private-session")}
	refs := []responseReference{{"call", "private-call"}, {"encrypted", "private-encrypted"}}
	var workers sync.WaitGroup
	for i := 0; i < admissionDiagnosticLimit+20; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); recordAdmissionDiagnostic("input", r, false, refs) }()
	}
	workers.Wait()
	entries := admissionDiagnosticSnapshot()
	if len(entries) != admissionDiagnosticLimit {
		t.Fatalf("unbounded diagnostics: %d", len(entries))
	}
	raw, _ := json.Marshal(entries)
	if strings.Contains(string(raw), "private-") {
		t.Fatal("raw identity or encrypted value retained")
	}
	entries[0].References[0].Hash = "changed"
	if admissionDiagnosticSnapshot()[0].References[0].Hash == "changed" {
		t.Fatal("snapshot aliases internal state")
	}
	refs = nil
	for i := 0; i < 100; i++ {
		refs = append(refs, responseReference{"call", fmt.Sprint(i)})
	}
	recordAdmissionDiagnostic("output", r, false, refs)
	entries = admissionDiagnosticSnapshot()
	last := entries[len(entries)-1]
	if len(last.References) != diagnosticReferenceLimit || !last.Truncated {
		t.Fatal("reference bound missing")
	}
	admissionDiagnostics.Lock()
	for i := range admissionDiagnostics.entries {
		admissionDiagnostics.entries[i].At = time.Now().Add(-25 * time.Hour)
	}
	admissionDiagnostics.Unlock()
	if len(admissionDiagnosticSnapshot()) != 0 {
		t.Fatal("expired diagnostics exposed")
	}
}
