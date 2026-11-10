package main

import (
	"fmt"
	"github.com/UangSC/cpa-agentrouter-continuity/internal/sdk"
	"net/http"
	"sync"
)

const admissionHeader = "X-Cpa-Agentrouter-Continuity-Admission"

type admissionRequest struct {
	ID, Session, RawSession string
	Fresh, ExplicitFresh    bool
	ParentSession           string
	ParentConflict          bool
	CallerScope             string
	Transient               bool
	Recovered               *continuityEntry
}

var admissionRequests sync.Map

func beforeAdmission(q sdk.RequestInterceptRequest) sdk.RequestInterceptResponse {
	session, raw := stableSession(q.Headers, q.Body, q.Metadata)
	r := admissionRequest{ID: q.RequestID, Session: session, RawSession: raw, Fresh: freshConversation(q.Body, q.Metadata), ExplicitFresh: stringMeta(q.Metadata, "history_initialization") == "new"}
	r.ParentSession, r.ParentConflict = parentSession(q.Headers, q.Body, q.Metadata, raw)
	r.CallerScope = stringMeta(q.Metadata, "caller_scope")
	if !r.Fresh && r.ParentSession == "" && !r.ParentConflict && (r.Session != "" || !hasSessionHint(q.Headers, q.Body, q.Metadata)) {
		entry, conflict := lookupContinuity(r.CallerScope, q.Body)
		if conflict {
			r.ParentConflict = true
		} else if entry != nil {
			if r.Session == "" {
				r.Session = entry.Session
				r.Transient = true
			}
			r.Recovered = entry
		}
	}
	if r.Session == "" && r.Fresh && r.ParentSession == "" && !r.ParentConflict && q.RequestID != "" && !hasSessionHint(q.Headers, q.Body, q.Metadata) {
		// A model-setup probe does not have a conversation yet. Give only this
		// request a private reservation; never persist it as a sticky origin.
		r.Session = digest("stateless-probe\x00" + r.CallerScope + "\x00" + q.RequestID)
		r.Transient = true
	}
	admissionRequests.Store(q.RequestID, r)
	if r.Transient || !r.Fresh {
		recordAdmissionDiagnostic("input", r, hasSessionHint(q.Headers, q.Body, q.Metadata), protocolReferences(q.Body, false))
	}
	// Always overwrite a client-supplied admission marker; it is never trusted.
	return sdk.RequestInterceptResponse{Headers: http.Header{admissionHeader: {q.RequestID}}}
}
func chosenRuntime(meta map[string]any) (sdk.HostAuthFileEntry, error) {
	var result sdk.HostAuthGetRuntimeResponse
	index := stringMeta(meta, "selected_auth_index")
	if index == "" {
		return result.Auth, fmt.Errorf("selected auth identity unavailable")
	}
	e := hostCall("host.auth.get_runtime", map[string]string{"auth_index": index}, &result)
	return result.Auth, e
}
func afterAdmission(q sdk.RequestInterceptRequest) sdk.RequestInterceptResponse {
	result := sdk.RequestInterceptResponse{ClearHeaders: []string{admissionHeader}}
	v, ok := admissionRequests.Load(q.RequestID)
	if !ok {
		return result
	}
	r := v.(admissionRequest)
	chosen, e := chosenRuntime(q.Metadata)
	if e != nil {
		return result
	}
	if chosen.Provider == pluginID {
		return sdk.RequestInterceptResponse{}
	}
	if chosen.Provider != pluginID && r.Session != "" && !r.Transient {
		_ = withRoutingLedger(func(s *routingLedger) error {
			s.Origins[r.Session] = sessionOrigin{Provider: chosen.Provider, AuthID: chosen.ID, Foreign: true}
			delete(s.Leases, r.ID)
			return nil
		})
	}
	return result
}
func completeAdmission(q sdk.RequestCompletion) {
	cancelExecution(q.RequestID)
	admissionRequests.Delete(q.RequestID)
	_ = withRoutingLedger(func(s *routingLedger) error { delete(s.Leases, q.RequestID); return nil })
	_ = withExecutionLedger(func(s *routingLedger) error { delete(s.Leases, q.RequestID); return nil })
}
