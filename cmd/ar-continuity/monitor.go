package main

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

type requestRecord struct {
	ID            string      `json:"id"`
	CredentialID  string      `json:"credential_id"`
	Model         string      `json:"model"`
	StartedAt     time.Time   `json:"started_at"`
	DurationMS    int64       `json:"duration_ms"`
	FirstOutputMS int64       `json:"first_output_ms"`
	QueueWaitMS   int64       `json:"queue_wait_ms"`
	Status        string      `json:"status"`
	ErrorCode     string      `json:"error_code,omitempty"`
	Attempts      int         `json:"attempts"`
	RetryReasons  []string    `json:"retry_reasons"`
	Usage         *tokenUsage `json:"-"`
}
type monitorState struct {
	mu       sync.Mutex
	sequence atomic.Uint64
	Active   map[string]*requestRecord
	Recent   []requestRecord
}

var monitor = monitorState{Active: map[string]*requestRecord{}}

type recordContextKey struct{}

func beginRecord(ctx context.Context, q execution, c credential) (context.Context, *requestRecord) {
	r := &requestRecord{ID: fmt.Sprint(monitor.sequence.Add(1)), CredentialID: c.ID, Model: q.Model, StartedAt: time.Now().UTC(), Status: "waiting", FirstOutputMS: -1, RetryReasons: []string{}}
	monitor.mu.Lock()
	monitor.Active[r.ID] = r
	analyticsBucketFor(r).Requests++
	monitor.mu.Unlock()
	return context.WithValue(ctx, recordContextKey{}, r), r
}
func recordAttempt(r *requestRecord) {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	r.Attempts++
	r.Status = "waiting"
}
func recordQueued(r *requestRecord) {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	r.Status = "queued"
}
func recordDequeued(r *requestRecord) {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	r.QueueWaitMS = time.Since(r.StartedAt).Milliseconds()
	r.Status = "waiting"
}
func recordOutput(r *requestRecord) {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	if r.FirstOutputMS < 0 {
		r.FirstOutputMS = time.Since(r.StartedAt).Milliseconds()
	}
	r.Status = "streaming"
}
func recordRetry(ctx context.Context, reason string) {
	r, _ := ctx.Value(recordContextKey{}).(*requestRecord)
	if r == nil {
		return
	}
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	r.Status = "retrying"
	r.RetryReasons = append(r.RetryReasons, reason)
}
func finishRecord(r *requestRecord, status, code string) {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	if r.Status == "queued" {
		r.QueueWaitMS = time.Since(r.StartedAt).Milliseconds()
	}
	r.Status = status
	r.ErrorCode = code
	r.DurationMS = time.Since(r.StartedAt).Milliseconds()
	finishAnalytics(r)
	delete(monitor.Active, r.ID)
	monitor.Recent = append(monitor.Recent, *r)
	if len(monitor.Recent) > 200 {
		monitor.Recent = monitor.Recent[len(monitor.Recent)-200:]
	}
}
func monitorSnapshot() map[string]any {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	recent := append([]requestRecord{}, monitor.Recent...)
	running := make([]requestRecord, 0, len(monitor.Active))
	for _, r := range monitor.Active {
		copy := *r
		copy.RetryReasons = append([]string{}, r.RetryReasons...)
		copy.DurationMS = time.Since(copy.StartedAt).Milliseconds()
		running = append(running, copy)
	}
	for i := range recent {
		recent[i].RetryReasons = append([]string{}, recent[i].RetryReasons...)
	}
	return map[string]any{"active": running, "recent": recent, "retention": 200, "analytics": analyticsSnapshot(time.Now().UTC())}
}
