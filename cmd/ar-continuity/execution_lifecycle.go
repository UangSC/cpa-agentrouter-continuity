package main

import (
	"context"
	"fmt"
	"sync"
)

func acquireExecution(q execution, c credential) (func(), error) {
	session, _ := stableSession(q.Headers, q.OriginalRequest, q.Metadata)
	if value, ok := admissionRequests.Load(q.Headers.Get(admissionHeader)); ok {
		r := value.(admissionRequest)
		if r.Transient && r.Fresh && freshConversation(q.OriginalRequest, q.Metadata) {
			session = r.Session
		}
	}
	// After-auth strips the transport marker; recover the request from its reserved lease.
	requestID := ""
	e := withRoutingLedger(func(s *routingLedger) error {
		id := q.Headers.Get(admissionHeader)
		lease, ok := s.Leases[id]
		if ok && lease.Session == session && lease.AuthID == q.AuthID && lease.KeyHash == digest(c.APIKey) && !lease.Running {
			requestID = id
			lease.Running = true
			if err := withExecutionLedger(func(executions *routingLedger) error {
				waiting := false
				reserved := lease
				reserved.Upstream = &waiting
				reserved.RequestLimit = requestLimit(c)
				reserved.SessionRequestLimit = maximumConcurrentRequests // compatibility with draining 0.5.6 executors
				executions.Leases[id] = reserved
				return nil
			}); err != nil {
				return err
			}
			s.Leases[id] = lease
			return nil
		}
		return fmt.Errorf("no AR session reservation")
	})
	if e != nil {
		return nil, &rpcError{"ar_session_ineligible", "This conversation is not admitted to AR. Start a new conversation or use its original provider.", 409}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = withRoutingLedger(func(s *routingLedger) error { delete(s.Leases, requestID); return nil })
			_ = withExecutionLedger(func(s *routingLedger) error { delete(s.Leases, requestID); return nil })
		})
	}, nil
}

var active sync.Map

func executionContext(q execution) (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	id := q.Headers.Get(admissionHeader)
	active.Store(id, cancel)
	return ctx, func() { cancel(); active.Delete(id) }
}

func cancelExecution(id string) {
	if cancel, ok := active.Load(id); ok {
		cancel.(context.CancelFunc)()
	}
}

func shutdown() { active.Range(func(_, v any) bool { v.(context.CancelFunc)(); return true }) }
