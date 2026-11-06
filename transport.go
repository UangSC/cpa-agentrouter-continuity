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
			return b.Bytes(), e
		}
		if c.Done {
			return b.Bytes(), nil
		}
	}
}

func safeError(status int, raw []byte, secrets ...string) *rpcError {
	return safeErrorWithCause(status, raw, nil, secrets...)
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
		if e := pricingFailure(s.Status, raw, err, c.APIKey); e != nil {
			failures.Add(1)
			reason = e.Code
			return nil, e
		}
		if err == nil && s.Status >= 200 && s.Status < 300 && classify(s.Status, raw) == "" {
			if e := incompleteError(raw); e != nil {
				failures.Add(1)
				reason = e.Code
				return nil, e
			}
			if b.used > 0 {
				recovered.Add(1)
			}
			outcome = "completed"
			reason = ""
			if q.toolNamespaceAliased {
				raw, _ = rewriteToolNames(raw, collaborationAlias, "collaboration")
			}
			return raw, nil
		}
		kind := failureKind(s.Status, raw, err)
		if !b.retry(ctx, kind, s.Headers) {
			failures.Add(1)
			e := safeErrorWithCause(s.Status, raw, err, c.APIKey)
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
	payload, q.toolNamespaceAliased, e = aliasRequestTools(payload)
	if e != nil {
		return nil, e
	}
	s, e := startHTTP(q, c, payload, "responses/compact")
	if e != nil {
		if failure := pricingFailure(s.Status, nil, e, c.APIKey); failure != nil {
			return nil, failure
		}
		return nil, e
	}
	raw, e := readAll(s.StreamID)
	if failure := pricingFailure(s.Status, raw, e, c.APIKey); failure != nil {
		return nil, failure
	}
	if e != nil {
		return nil, e
	}
	if s.Status >= 400 {
		return nil, safeError(s.Status, raw, c.APIKey)
	}
	if q.toolNamespaceAliased {
		raw, _ = rewriteToolNames(raw, collaborationAlias, "collaboration")
	}
	return sdk.ExecutorResponse{Payload: raw}, nil
}

// frameReader reconstructs SSE frames without assuming TCP chunk boundaries.
