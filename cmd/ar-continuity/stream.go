package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

func runStreaming(ctx context.Context, q execution, c credential, body []byte) {
	continuity := newContinuityRecorder(q, c)
	requests.Add(1)
	ctx, record := beginRecord(ctx, q, c)
	outcome, reason := "failed", "upstream_error"
	defer func() { finishRecord(record, outcome, reason) }()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	q.ctx = ctx
	var emitMu sync.Mutex
	emit := func(p []byte) error {
		emitMu.Lock()
		defer emitMu.Unlock()
		e := hostCall("host.stream.emit", map[string]any{"stream_id": q.StreamID, "payload": p}, nil)
		if e != nil {
			cancel()
		}
		return e
	}
	// Comments are not output events. They keep the connection alive and detect cancellation during backoff.
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if emit([]byte(": cpa-agentrouter-continuity waiting\n\n")) != nil {
					return
				}
			}
		}
	}()
	defer close(done)
	defer func() { _ = hostCall("host.stream.close", map[string]string{"stream_id": q.StreamID}, nil) }()
	terminal := func(e *rpcError) {
		if q.release != nil {
			q.release()
		}
		if ctx.Err() != nil {
			outcome = "canceled"
			reason = "client_disconnected"
			return
		}
		failures.Add(1)
		reason = e.Code
		v := map[string]any{"type": "response.failed", "response": map[string]any{"status": "failed", "error": map[string]any{"code": e.Code, "message": strings.ReplaceAll(e.Message, c.APIKey, "[REDACTED]"), "type": errorType(e.HTTPStatus), "http_status": e.HTTPStatus}}}
		d, _ := json.Marshal(v)
		_ = emit(append(append([]byte("event: response.failed\ndata: "), d...), []byte("\n\n")...))
	}
	if q.admitted {
		recordQueued(record)
		if err := waitExecutionSlot(ctx, q, c); err != nil {
			terminal(asRPCError(err))
			return
		}
		recordDequeued(record)
	}
	b := newBudget(c)
	for ctx.Err() == nil {
		recordAttempt(record)
		s, e := startHTTP(q, c, body, "responses")
		if e != nil {
			if failure := pricingFailure(s.Status, nil, e, c.APIKey); failure != nil {
				terminal(failure)
				return
			}
			if strings.Contains(strings.ToLower(e.Error()), "cancel") {
				outcome = "canceled"
				reason = "client_disconnected"
				return
			}
			if b.retry(ctx, failureKind(s.Status, nil, e), nil) {
				continue
			}
			terminal(safeErrorWithCause(502, nil, e, c.APIKey))
			return
		}
		if s.Status < 200 || s.Status >= 300 {
			raw, err := readResponse(s)
			if failure := pricingFailure(s.Status, raw, err, c.APIKey); failure != nil {
				terminal(failure)
				return
			}
			kind := failureKind(s.Status, raw, err)
			if b.retry(ctx, kind, s.Headers) {
				continue
			}
			terminal(safeErrorWithCause(s.Status, raw, err, c.APIKey))
			return
		}
		if !strings.Contains(strings.ToLower(s.Headers.Get("Content-Type")), "text/event-stream") {
			raw, readErr := readResponse(s)
			if b.retry(ctx, classify(502, raw), s.Headers) {
				continue
			}
			if readErr == nil {
				readErr = fmt.Errorf("unexpected upstream Content-Type: %s", s.Headers.Get("Content-Type"))
			}
			terminal(safeErrorWithCause(502, raw, readErr, c.APIKey))
			return
		}
		committed := false
		completed := false
		var pending bytes.Buffer
		parser := frameReader{}
		kind := ""
		var failure []byte
		var attemptErr error
		var terminalFailure *rpcError
		for ctx.Err() == nil {
			chunk, err := readHTTP(s.StreamID)
			frames, parseErr := parser.feed(chunk.Payload)
			if parseErr != nil {
				err = parseErr
			}
			for _, f := range frames {
				if q.toolNamespaceAliased {
					f = restoreToolFrame(f)
				}
				d := eventData(f)
				t := eventType(d)
				if err := continuity.remember(c, d); err != nil {
					terminalFailure = asRPCError(err)
					attemptErr = err
					break
				}
				// content_filter is a terminal upstream decision.  Do not forward
				// response.incomplete: Codex treats that event as replayable and can
				// submit the same expensive history repeatedly.  Emit one explicit
				// failure instead, while preserving any text already delivered.
				if t == "response.incomplete" {
					if e := incompleteError(d); e != nil {
						failure = d
						terminalFailure = e
						attemptErr = fmt.Errorf("upstream response incomplete: content_filter")
						break
					}
				}
				if t == "response.failed" || t == "error" {
					failure = d
					if e := pricingConfigError(0, d, c.APIKey); e != nil {
						terminalFailure = e
						attemptErr = e
						break
					}
					if committed && classify(0, d) == "history" {
						terminalFailure = safeError(400, d, c.APIKey)
						attemptErr = terminalFailure
						break
					}
					if !committed {
						kind = classify(0, d)
						attemptErr = fmt.Errorf("upstream failed")
						break
					}
					// Even after output, error details can echo runtime credentials.
					// Emit one sanitized terminal failure without replaying content.
					terminal(safeError(502, d, c.APIKey))
					completed = true
					break
				}
				// Terminal frames can reach the client before stream_close returns.
				// Free capacity first so its next request does not observe a stale slot.
				if (t == "response.completed" || t == "response.incomplete") && q.release != nil {
					q.release()
				}
				if !committed {
					pending.Write(f)
					if pending.Len() > maxBuffer {
						attemptErr = fmt.Errorf("SSE prelude too large")
						break
					}
					if meaningful(t, d) {
						committed = true
						recordOutput(record)
						if emit(pending.Bytes()) != nil {
							break
						}
						pending.Reset()
					}
				} else {
					if emit(f) != nil {
						break
					}
				}
				if t == "response.completed" || t == "response.incomplete" {
					recordUsage(record, d)
					outcome = strings.TrimPrefix(t, "response.")
					reason = ""
					completed = true
					if b.used > 0 {
						recovered.Add(1)
					}
					break
				}
			}
			if completed || attemptErr != nil {
				break
			}
			if err != nil {
				attemptErr = err
				break
			}
			if chunk.Done {
				attemptErr = io.ErrUnexpectedEOF
				break
			}
		}
		s.close()
		if completed {
			return
		}
		if ctx.Err() != nil {
			outcome = "canceled"
			reason = "client_disconnected"
			return
		}
		if terminalFailure == nil {
			terminalFailure = pricingFailure(0, failure, attemptErr, c.APIKey)
		}
		if terminalFailure != nil {
			terminal(terminalFailure)
			return
		}
		if committed {
			terminal(&rpcError{"ar_stream_interrupted", "AgentRouter stream interrupted after output; automatic replay suppressed to avoid duplicate tool actions", 502})
			return
		}
		if len(failure) == 0 && attemptErr != nil {
			kind = failureKind(0, nil, attemptErr)
		}
		if b.retry(ctx, kind, s.Headers) {
			continue
		}
		terminal(safeErrorWithCause(502, failure, attemptErr, c.APIKey))
		return
	}
}
