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

type frameReader struct{ pending []byte }

func (f *frameReader) feed(p []byte) ([][]byte, error) {
	f.pending = append(f.pending, p...)
	if len(f.pending) > maxBuffer {
		return nil, fmt.Errorf("SSE frame too large")
	}
	var frames [][]byte
	for {
		i := bytes.Index(f.pending, []byte("\n\n"))
		j := bytes.Index(f.pending, []byte("\r\n\r\n"))
		n := 2
		if j >= 0 && (i < 0 || j < i) {
			i = j
			n = 4
		}
		if i < 0 {
			break
		}
		frames = append(frames, bytes.Clone(f.pending[:i+n]))
		f.pending = f.pending[i+n:]
	}
	return frames, nil
}

func eventData(frame []byte) []byte {
	var rows [][]byte
	for _, l := range bytes.Split(frame, []byte("\n")) {
		l = bytes.TrimSuffix(l, []byte("\r"))
		if bytes.HasPrefix(l, []byte("data:")) {
			rows = append(rows, bytes.TrimPrefix(l[5:], []byte(" ")))
		}
	}
	return bytes.Join(rows, []byte("\n"))
}

func eventType(data []byte) string {
	var v struct{ Type string }
	_ = json.Unmarshal(data, &v)
	return v.Type
}

func meaningful(kind string, data []byte) bool {
	switch kind {
	case "response.created", "response.in_progress", "response.queued":
		return false
	}
	// Unknown events may contain content or tool effects: commit conservatively.
	return len(data) > 0 && string(data) != "[DONE]"
}

func runStreaming(ctx context.Context, q execution, c credential, body []byte) {
	requests.Add(1)
	ctx, record := beginRecord(ctx, q, c)
	outcome, reason := "failed", "upstream_error"
	defer func() { finishRecord(record, outcome, reason) }()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
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
		failures.Add(1)
		reason = e.Code
		v := map[string]any{"type": "response.failed", "response": map[string]any{"status": "failed", "error": map[string]any{"code": e.Code, "message": strings.ReplaceAll(e.Message, c.APIKey, "[REDACTED]"), "type": errorType(e.HTTPStatus), "http_status": e.HTTPStatus}}}
		d, _ := json.Marshal(v)
		_ = emit(append(append([]byte("event: response.failed\ndata: "), d...), []byte("\n\n")...))
	}
	b := newBudget(c)
	for ctx.Err() == nil {
		recordAttempt(record)
		s, e := startHTTP(q, c, body, "responses")
		if e != nil {
			if strings.Contains(strings.ToLower(e.Error()), "cancel") {
				outcome = "canceled"
				reason = "client_disconnected"
				return
			}
			if b.retry(ctx, transportKind(e), nil) {
				continue
			}
			terminal(safeError(502, nil))
			return
		}
		if s.Status < 200 || s.Status >= 300 {
			raw, err := readAll(s.StreamID)
			kind := classify(s.Status, raw)
			if err != nil {
				kind = transportKind(err)
			}
			if b.retry(ctx, kind, s.Headers) {
				continue
			}
			terminal(safeError(s.Status, raw, c.APIKey))
			return
		}
		if !strings.Contains(strings.ToLower(s.Headers.Get("Content-Type")), "text/event-stream") {
			raw, _ := readAll(s.StreamID)
			if b.retry(ctx, classify(502, raw), s.Headers) {
				continue
			}
			terminal(safeError(502, raw, c.APIKey))
			return
		}
		committed := false
		completed := false
		var pending bytes.Buffer
		parser := frameReader{}
		kind := ""
		var failure []byte
		var attemptErr error
		for ctx.Err() == nil {
			chunk, err := readHTTP(s.StreamID)
			frames, parseErr := parser.feed(chunk.Payload)
			if parseErr != nil {
				err = parseErr
			}
			for _, f := range frames {
				d := eventData(f)
				t := eventType(d)
				if t == "response.failed" || t == "error" {
					failure = d
					if !committed {
						kind = classify(0, d)
						attemptErr = fmt.Errorf("upstream failed")
						break
					}
					_ = emit(f)
					completed = true
					reason = "upstream_stream_error"
					failures.Add(1)
					break
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
		closeHTTP(s.StreamID)
		if ctx.Err() != nil || completed {
			if ctx.Err() != nil {
				outcome = "canceled"
				reason = "client_disconnected"
			}
			return
		}
		if committed {
			terminal(&rpcError{"ar_stream_interrupted", "AgentRouter stream interrupted after output; automatic replay suppressed to avoid duplicate tool actions", 502})
			return
		}
		if len(failure) == 0 && attemptErr != nil {
			kind = transportKind(attemptErr)
		}
		if b.retry(ctx, kind, s.Headers) {
			continue
		}
		terminal(safeError(502, failure, c.APIKey))
		return
	}
}
