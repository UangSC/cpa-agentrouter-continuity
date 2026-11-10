package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
)

type httpStart struct {
	Status   int         `json:"status_code"`
	Headers  http.Header `json:"headers"`
	StreamID string      `json:"stream_id"`
	closeFn  func()
}

func (s httpStart) close() {
	if s.closeFn != nil {
		s.closeFn()
	} else {
		closeHTTP(s.StreamID)
	}
}

func readResponse(s httpStart) ([]byte, error) {
	defer s.close()
	return readPayload(s.StreamID)
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
	if err == nil && s.StreamID != "" {
		id := s.StreamID
		var once sync.Once
		closeStream := func() { once.Do(func() { closeHTTP(id) }) }
		s.closeFn = closeStream
		if q.ctx != nil {
			stop := context.AfterFunc(q.ctx, closeStream)
			s.closeFn = func() { stop(); closeStream() }
		}
	}
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
	return readPayload(id)
}

func readPayload(id string) ([]byte, error) {
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
