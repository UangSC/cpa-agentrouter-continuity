package main

import (
	"sync"
	"time"
)

const admissionDiagnosticLimit = 256
const diagnosticReferenceLimit = 32

type diagnosticReference struct {
	Kind string `json:"kind"`
	Hash string `json:"hash"`
}

type admissionDiagnostic struct {
	At          time.Time             `json:"at"`
	Stage       string                `json:"stage"`
	RequestHash string                `json:"request_hash"`
	CallerHash  string                `json:"caller_hash"`
	SessionHash string                `json:"session_hash,omitempty"`
	Fresh       bool                  `json:"fresh,omitempty"`
	Transient   bool                  `json:"transient,omitempty"`
	Recovered   bool                  `json:"recovered,omitempty"`
	Parent      bool                  `json:"parent,omitempty"`
	Conflict    bool                  `json:"conflict,omitempty"`
	SessionHint bool                  `json:"session_hint,omitempty"`
	References  []diagnosticReference `json:"references"`
	Truncated   bool                  `json:"truncated,omitempty"`
}

var admissionDiagnostics struct {
	sync.Mutex
	entries []admissionDiagnostic
}

// Keep only bounded protocol fingerprints in memory. The management route is
// authenticated by the host; public resources never expose these diagnostics.
func recordAdmissionDiagnostic(stage string, r admissionRequest, hint bool, refs []responseReference) {
	entry := admissionDiagnostic{At: time.Now().UTC(), Stage: stage,
		RequestHash: digest(r.ID), CallerHash: digest(r.CallerScope), SessionHash: r.Session,
		Fresh: r.Fresh, Transient: r.Transient, Recovered: r.Recovered != nil,
		Parent: r.ParentSession != "", Conflict: r.ParentConflict, SessionHint: hint,
		References: []diagnosticReference{}}
	seen := map[diagnosticReference]bool{}
	for _, ref := range refs {
		value := diagnosticReference{Kind: ref.kind, Hash: digest(ref.value)}
		if seen[value] {
			continue
		}
		seen[value] = true
		if len(entry.References) == diagnosticReferenceLimit {
			entry.Truncated = true
			break
		}
		entry.References = append(entry.References, value)
	}
	admissionDiagnostics.Lock()
	defer admissionDiagnostics.Unlock()
	if len(admissionDiagnostics.entries) == admissionDiagnosticLimit {
		copy(admissionDiagnostics.entries, admissionDiagnostics.entries[1:])
		admissionDiagnostics.entries = admissionDiagnostics.entries[:admissionDiagnosticLimit-1]
	}
	admissionDiagnostics.entries = append(admissionDiagnostics.entries, entry)
}

func admissionDiagnosticSnapshot() []admissionDiagnostic {
	admissionDiagnostics.Lock()
	defer admissionDiagnostics.Unlock()
	result := make([]admissionDiagnostic, 0, len(admissionDiagnostics.entries))
	for _, entry := range admissionDiagnostics.entries {
		if time.Since(entry.At) > 24*time.Hour {
			continue
		}
		entry.References = append([]diagnosticReference{}, entry.References...)
		result = append(result, entry)
	}
	return result
}
