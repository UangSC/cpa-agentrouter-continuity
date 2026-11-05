package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/UangSC/cpa-agentrouter-continuity/internal/sdk"
	"net/http"
)

type httpStart struct {
	Status   int         `json:"status_code"`
	Headers  http.Header `json:"headers"`
	StreamID string      `json:"stream_id"`
}

type httpChunk struct {
	Payload []byte `json:"payload"`
	Error   string `json:"error"`
	Done    bool   `json:"done"`
}

func upstreamHeaders(c credential, q execution) http.Header {
	h := http.Header{"Content-Type": {"application/json"}, "Accept-Encoding": {"identity"}}
	for _, k := range []string{"Session_id", "Conversation_id", "X-Codex-Turn-Metadata", "X-Codex-Session-Id"} {
		if v := q.Headers.Get(k); v != "" {
			h.Set(k, v)
		}
	}
	for k, values := range configuredHeaders(c) {
		for _, v := range values {
			h.Set(k, v)
		}
	}
	// Credentials and transport framing cannot be replaced by custom headers.
	h.Del("Host")
	h.Del("Content-Length")
	h.Del("Transfer-Encoding")
	h.Del("Proxy-Authorization")
	h.Set("Content-Type", "application/json")
	h.Set("Accept-Encoding", "identity")
	h.Set("Authorization", "Bearer "+c.APIKey)
	return h
}

func startHTTP(q execution, c credential, body []byte, path string) (httpStart, error) {
	var s httpStart
	err := hostCall("host.http.do_stream", map[string]any{"host_callback_id": q.CallbackID, "method": "POST", "url": "https://agentrouter.org/v1/" + path, "headers": upstreamHeaders(c, q), "body": body}, &s)
	return s, err
}

func closeHTTP(id string) {
	if id != "" {
		_ = hostCall("host.http.stream_close", map[string]string{"stream_id": id}, nil)
	}
}

func readHTTP(id string) (httpChunk, error) {
	var c httpChunk
	err := hostCall("host.http.stream_read", map[string]string{"stream_id": id}, &c)
	if err == nil && c.Error != "" {
		err = errors.New(c.Error)
	}
	return c, err
}

func readAll(id string) ([]byte, error) {
	defer closeHTTP(id)
	var b bytes.Buffer
	for {
		c, e := readHTTP(id)
		b.Write(c.Payload)
		if b.Len() > maxBuffer {
			return nil, fmt.Errorf("response exceeds buffer limit")
		}
		if e != nil {
			return nil, e
		}
		if c.Done {
			return b.Bytes(), nil
		}
	}
}

func safeError(status int, raw []byte, secrets ...string) *rpcError {
	for _, secret := range secrets {
		if secret != "" {
			raw = bytes.ReplaceAll(raw, []byte(secret), []byte("[REDACTED]"))
		}
	}
	// Only upstream error objects are exposed; never echo a request or credential.
	if classify(status, raw) == "history" {
		return &rpcError{"ar_new_session_required", "AgentRouter cannot verify encrypted reasoning from this conversation. Start a new session; history was preserved and this request was not retried.", 400}
	}
	var o map[string]json.RawMessage
	_ = json.Unmarshal(raw, &o)
	var e struct {
		Message string
		Code    string
	}
	if nested := o["response"]; len(nested) > 0 {
		_ = json.Unmarshal(nested, &o)
	}
	_ = json.Unmarshal(o["error"], &e)
	if e.Message == "" {
		e.Message = "AgentRouter request failed"
	}
	if len(e.Message) > 1500 {
		e.Message = e.Message[:1500]
	}
	if e.Code == "" {
		e.Code = "ar_upstream_error"
	}
	if status < 400 {
		status = 502
	}
	return &rpcError{e.Code, e.Message, status}
}

func runJSON(ctx context.Context, q execution, c credential, body []byte) ([]byte, error) {
	requests.Add(1)
	ctx, record := beginRecord(ctx, q, c)
	outcome, reason := "failed", "upstream_error"
	defer func() { finishRecord(record, outcome, reason) }()
	b := newBudget(c)
	for {
		recordAttempt(record)
		s, err := startHTTP(q, c, body, "responses")
		var raw []byte
		if err == nil {
			raw, err = readAll(s.StreamID)
		}
		if err == nil && s.Status >= 200 && s.Status < 300 && classify(s.Status, raw) == "" {
			if b.used > 0 {
				recovered.Add(1)
			}
			outcome = "completed"
			reason = ""
			return raw, nil
		}
		kind := classify(s.Status, raw)
		if err != nil {
			kind = transportKind(err)
		}
		if !b.retry(ctx, kind, s.Headers) {
			failures.Add(1)
			e := safeError(s.Status, raw, c.APIKey)
			reason = e.Code
			return nil, e
		}
	}
}

func compact(q execution, c credential) (any, error) {
	model, e := modelForCredential(c, q.Model)
	if e != nil {
		return nil, e
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(q.Payload, &body) != nil || body == nil {
		return nil, &rpcError{"invalid_request", "Expected compact JSON object", 400}
	}
	body["model"], _ = json.Marshal(model)
	payload, e := json.Marshal(body)
	if e != nil {
		return nil, e
	}
	s, e := startHTTP(q, c, payload, "responses/compact")
	if e != nil {
		return nil, e
	}
	raw, e := readAll(s.StreamID)
	if e != nil {
		return nil, e
	}
	if s.Status >= 400 {
		return nil, safeError(s.Status, raw, c.APIKey)
	}
	return sdk.ExecutorResponse{Payload: raw}, nil
}

// frameReader reconstructs SSE frames without assuming TCP chunk boundaries.
