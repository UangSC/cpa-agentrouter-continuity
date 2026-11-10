package main

import (
	"context"
	"encoding/json"
	"github.com/UangSC/cpa-agentrouter-continuity/internal/sdk"
)

func runJSON(ctx context.Context, q execution, c credential, body []byte) ([]byte, error) {
	q.ctx = ctx
	requests.Add(1)
	ctx, record := beginRecord(ctx, q, c)
	outcome, reason := "failed", "upstream_error"
	defer func() { finishRecord(record, outcome, reason) }()
	if q.admitted {
		recordQueued(record)
		if err := waitExecutionSlot(ctx, q, c); err != nil {
			if ctx.Err() != nil {
				outcome, reason = "canceled", "client_disconnected"
				return nil, ctx.Err()
			}
			failures.Add(1)
			reason = asRPCError(err).Code
			return nil, err
		}
		recordDequeued(record)
	}
	b := newBudget(c)
	for {
		if ctx.Err() != nil {
			outcome, reason = "canceled", "client_disconnected"
			return nil, ctx.Err()
		}
		recordAttempt(record)
		s, err := startHTTP(q, c, body, "responses")
		var raw []byte
		if err == nil {
			raw, err = readResponse(s)
		}
		if ctx.Err() != nil {
			outcome, reason = "canceled", "client_disconnected"
			return nil, ctx.Err()
		}
		if e := pricingFailure(s.Status, raw, err, c.APIKey); e != nil {
			failures.Add(1)
			reason = e.Code
			return nil, e
		}
		var responseErr *rpcError
		if err == nil {
			responseErr = jsonResponseError(s.Status, raw, c.APIKey)
		}
		if err == nil && responseErr == nil {
			if b.used > 0 {
				recovered.Add(1)
			}
			outcome = "completed"
			reason = ""
			recordUsage(record, raw)
			if q.toolNamespaceAliased {
				raw, _ = rewriteToolNames(raw, collaborationAlias, "collaboration")
			}
			return raw, nil
		}
		kind := failureKind(s.Status, raw, err)
		if responseErr != nil && responseErr.Code == "ar_upstream_response_invalid" && s.Status >= 200 && s.Status < 300 {
			kind = "transport"
		}
		if !b.retry(ctx, kind, s.Headers) {
			if ctx.Err() != nil {
				outcome, reason = "canceled", "client_disconnected"
				return nil, ctx.Err()
			}
			failures.Add(1)
			e := safeErrorWithCause(s.Status, raw, err, c.APIKey)
			if responseErr != nil {
				e = responseErr
			}
			reason = e.Code
			return nil, e
		}
	}
}

// A successful HTTP status does not make an error envelope a successful
// Responses result. Apply the same rule to generation and compaction.
func jsonResponseError(status int, raw []byte, secrets ...string) *rpcError {
	if status < 200 || status >= 300 {
		return safeError(status, raw, secrets...)
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil || len(root) == 0 {
		return &rpcError{"ar_upstream_response_invalid", "上游返回了无效的 Responses JSON。", 502}
	}
	if failure := incompleteError(raw); failure != nil {
		return failure
	}
	var state, kind string
	_ = json.Unmarshal(root["status"], &state)
	_ = json.Unmarshal(root["type"], &kind)
	errorValue := root["error"]
	if len(errorValue) != 0 && string(errorValue) != "null" || state == "failed" || kind == "error" || kind == "response.failed" {
		return safeError(status, raw, secrets...)
	}
	return nil
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
	raw, e := readResponse(s)
	if failure := pricingFailure(s.Status, raw, e, c.APIKey); failure != nil {
		return nil, failure
	}
	if e != nil {
		return nil, e
	}
	if failure := jsonResponseError(s.Status, raw, c.APIKey); failure != nil {
		return nil, failure
	}
	if q.toolNamespaceAliased {
		raw, _ = rewriteToolNames(raw, collaborationAlias, "collaboration")
	}
	return sdk.ExecutorResponse{Payload: raw}, nil
}
