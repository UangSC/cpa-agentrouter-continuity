package main

import (
	"encoding/json"
	"fmt"
	"github.com/UangSC/cpa-agentrouter-continuity/internal/sdk"
	"net/http"
)

func executeRequest(method string, raw []byte) (any, error) {
	var q execution
	if err := json.Unmarshal(raw, &q); err != nil {
		return nil, err
	}
	if q.AuthProvider != pluginID {
		return nil, &rpcError{"ar_scope_violation", "当前凭证不属于此渠道", 400}
	}
	c, e := decodeCredential(q.StorageJSON)
	if e != nil {
		return nil, e
	}
	if c.Disabled {
		return nil, &rpcError{"ar_disabled", "渠道凭证已停用", 403}
	}
	if q.Alt != "" && q.Alt != "responses/compact" {
		return nil, &rpcError{"ar_unsupported_route", "渠道不支持此操作", 400}
	}
	if q.StreamID == "" && method == "executor.execute_stream" {
		return nil, fmt.Errorf("stream ID missing")
	}
	if q.Alt == "responses/compact" {
		release, e := acquireExecution(q, c)
		if e != nil {
			return nil, e
		}
		defer release()
		q.admitted = true
		ctx, finish := executionContext(q)
		defer finish()
		q.ctx = ctx
		if e := waitExecutionSlot(ctx, q, c); e != nil {
			return nil, e
		}
		return compact(q, c)
	}
	upstreamModel, e := modelForCredential(c, q.Model)
	if e != nil {
		return nil, e
	}
	body, e := prepareBody(q.Payload, upstreamModel, method == "executor.execute_stream")
	if e != nil {
		return nil, e
	}
	body, q.toolNamespaceAliased, e = aliasRequestTools(body)
	if e != nil {
		return nil, e
	}
	release, e := acquireExecution(q, c)
	if e != nil {
		return nil, e
	}
	q.admitted = true
	q.release = release
	ctx, finish := executionContext(q)
	if method == "executor.execute_stream" {
		go func() {
			defer release()
			defer finish()
			defer func() {
				if recover() != nil {
					_ = hostCall("host.stream.close", map[string]string{"stream_id": q.StreamID, "error": "渠道处理发生内部错误"}, nil)
				}
			}()
			runStreaming(ctx, q, c, body)
		}()
		return map[string]any{"headers": http.Header{"Content-Type": {"text/event-stream"}, "Cache-Control": {"no-cache"}, "X-CPA-AgentRouter-Continuity-Version": {version}}}, nil
	}
	defer release()
	defer finish()
	result, err := runJSON(ctx, q, c, body)
	return sdk.ExecutorResponse{Payload: result}, err

}
