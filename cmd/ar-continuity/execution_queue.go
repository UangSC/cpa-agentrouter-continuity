package main

import (
	"context"
	"errors"
	"time"
)

var errExecutionBusy = errors.New("AR execution slots busy")

func upstreamActive(lease admissionLease) bool {
	return lease.Running && (lease.Upstream == nil || *lease.Upstream)
}

// Both shared-library generations use the same locked ledger during a hot
// update. Never use a process-local semaphore for a persisted Key identity.
func claimExecutionSlot(q execution) error {
	return withRoutingLedger(func(routing *routingLedger) error {
		return withExecutionLedger(func(s *routingLedger) error {
			id := q.Headers.Get(admissionHeader)
			own, ok := s.Leases[id]
			reservation, reserved := routing.Leases[id]
			if !ok || !reserved || !reservation.Running || reservation.AuthID != q.AuthID || !own.Running || own.AuthID != q.AuthID || reservation.KeyHash != own.KeyHash {
				return &rpcError{"ar_session_ineligible", "会话预约已失效，请稍后重试。", 409}
			}
			if own.Upstream == nil || own.RequestLimit < 1 {
				return &rpcError{"ar_concurrency_state_unavailable", "插件更新期间并发预约已变化，请稍后重试。", 503}
			}
			if upstreamActive(own) {
				return nil
			}
			// Prune completed/canceled entries, including requests completed by an
			// older library. Never let an orphan reserve execution capacity.
			for requestID := range s.Leases {
				if _, exists := routing.Leases[requestID]; !exists {
					delete(s.Leases, requestID)
				}
			}
			keyLimit := own.RequestLimit
			active := 0
			for _, lease := range s.Leases {
				if lease.KeyHash != own.KeyHash || !lease.Running {
					continue
				}
				// Duplicate credentials for one Key cannot multiply its capacity.
				if lease.RequestLimit > 0 {
					keyLimit = min(keyLimit, lease.RequestLimit)
				}
				if upstreamActive(lease) {
					active++
				}
			}
			// Pre-0.5.6 executors do not have execution records. Conservatively
			// count their in-flight requests while they drain during hot upgrade.
			for requestID, lease := range routing.Leases {
				if lease.KeyHash == own.KeyHash && lease.Running {
					if _, tracked := s.Leases[requestID]; !tracked {
						active++
					}
				}
			}
			if active >= keyLimit {
				return errExecutionBusy
			}
			for otherID, lease := range s.Leases {
				if otherID == id || lease.KeyHash != own.KeyHash || !lease.Running || upstreamActive(lease) {
					continue
				}
				older := lease.Created.Before(own.Created) || lease.Created.Equal(own.Created) && otherID < id
				// All sessions sharing this Key use one FIFO execution queue.
				if older {
					return errExecutionBusy
				}
			}
			started := true
			own.Upstream = &started
			s.Leases[id] = own
			return nil
		})
	})
}

func waitExecutionSlot(ctx context.Context, q execution, c credential) error {
	if !q.admitted {
		return nil
	}
	seconds := c.RetryBudgetSeconds
	if seconds == 0 {
		seconds = 180
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
	defer cancel()
	for {
		if ctx.Err() != nil {
			return &rpcError{"ar_concurrency_wait_timeout", "请求等待并发名额超时；请降低请求并发量或稍后重试。", 429}
		}
		err := claimExecutionSlot(q)
		if err == nil {
			return nil
		}
		if !errors.Is(err, errExecutionBusy) {
			var rpc *rpcError
			if errors.As(err, &rpc) {
				return rpc
			}
			return &rpcError{"ar_concurrency_state_unavailable", "并发状态暂不可用，请稍后重试。", 503}
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}
