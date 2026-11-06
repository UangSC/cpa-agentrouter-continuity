package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/UangSC/cpa-agentrouter-continuity/internal/sdk"
	"net/http"
	"sync"
	"sync/atomic"
)

const pluginID = "cpa-agentrouter-continuity"

const version = "0.4.6"

const maxBuffer = 8 << 20

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

func (e *rpcError) Error() string { return e.Message }

func asRPCError(err error) *rpcError {
	var e *rpcError
	if errors.As(err, &e) {
		return e
	}
	return &rpcError{"ar_upstream_error", "AgentRouter request failed", 502}
}

type execution struct {
	sdk.ExecutorRequest
	StreamID             string `json:"stream_id"`
	CallbackID           string `json:"host_callback_id"`
	toolNamespaceAliased bool
}

type hostFunc func(string, any, any) error

var hostCall hostFunc = nativeHost

var active sync.Map

var requests, retries, recovered, failures atomic.Uint64

func registration() any {
	return map[string]any{"schema_version": 6, "metadata": map[string]any{"Name": "AR Continuity", "Version": version, "Author": "UangSC", "GitHubRepository": "https://github.com/UangSC/cpa-agentrouter-continuity", "Logo": resourcePrefix + "/logo.png", "ConfigFields": []any{}}, "capabilities": map[string]any{"auth_provider": true, "model_provider": true, "executor": true, "executor_model_scope": "both", "executor_input_formats": []string{"responses"}, "executor_output_formats": []string{"responses"}, "management_api": true, "scheduler": true, "scheduler_across_priorities": true, "request_interceptor": true, "request_lifecycle_plugin": true}}
}

func handle(method string, raw []byte) (any, error) {
	switch method {
	case "plugin.register", "plugin.reconfigure":
		return registration(), nil
	case "scheduler.pick":
		var q sdk.SchedulerPickRequest
		if e := json.Unmarshal(raw, &q); e != nil {
			return nil, e
		}
		return pickAdmission(q), nil
	case "request.intercept_before":
		var q sdk.RequestInterceptRequest
		if e := json.Unmarshal(raw, &q); e != nil {
			return nil, e
		}
		return beforeAdmission(q), nil
	case "request.intercept_after":
		var q sdk.RequestInterceptRequest
		if e := json.Unmarshal(raw, &q); e != nil {
			return nil, e
		}
		return afterAdmission(q), nil
	case "request.complete":
		var q sdk.RequestCompletion
		if e := json.Unmarshal(raw, &q); e != nil {
			return nil, e
		}
		completeAdmission(q)
		return map[string]any{}, nil
	case "auth.identifier", "executor.identifier":
		return map[string]string{"identifier": pluginID}, nil
	case "auth.parse":
		var q sdk.AuthParseRequest
		if err := json.Unmarshal(raw, &q); err != nil {
			return nil, err
		}
		var c credential
		if json.Unmarshal(q.RawJSON, &c) != nil || c.Type != pluginID {
			return sdk.AuthParseResponse{}, nil
		}
		if err := validateCredential(c); err != nil {
			return nil, err
		}
		return sdk.AuthParseResponse{Handled: true, Auth: authData(c, q.RawJSON, q.FileName)}, nil
	case "model.static":
		return sdk.ModelResponse{Provider: pluginID}, nil
	case "model.for_auth":
		var q sdk.AuthModelRequest
		if err := json.Unmarshal(raw, &q); err != nil {
			return nil, err
		}
		if q.AuthProvider != pluginID {
			return sdk.ModelResponse{}, nil
		}
		c, e := decodeCredential(q.StorageJSON)
		return sdk.ModelResponse{Provider: pluginID, Models: publicModels(c)}, e
	case "auth.refresh":
		var q sdk.AuthRefreshRequest
		if err := json.Unmarshal(raw, &q); err != nil {
			return nil, err
		}
		c, e := decodeCredential(q.StorageJSON)
		if e != nil {
			return nil, e
		}
		return sdk.AuthRefreshResponse{Auth: authData(c, q.StorageJSON, "")}, nil
	case "executor.execute", "executor.execute_stream":
		var q execution
		if err := json.Unmarshal(raw, &q); err != nil {
			return nil, err
		}
		if q.AuthProvider != pluginID {
			return nil, &rpcError{"ar_scope_violation", "Credential is not owned by AR plugin", 400}
		}
		c, e := decodeCredential(q.StorageJSON)
		if e != nil {
			return nil, e
		}
		if c.Disabled {
			return nil, &rpcError{"ar_disabled", "AR credential disabled", 403}
		}
		if q.Alt != "" && q.Alt != "responses/compact" {
			return nil, &rpcError{"ar_unsupported_route", "Unsupported AR operation", 400}
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
		if method == "executor.execute_stream" {
			ctx, cancel := context.WithCancel(context.Background())
			active.Store(q.StreamID, cancel)
			go func() {
				defer release()
				defer active.Delete(q.StreamID)
				defer cancel()
				defer func() {
					if recover() != nil {
						_ = hostCall("host.stream.close", map[string]string{"stream_id": q.StreamID, "error": "AR plugin internal failure"}, nil)
					}
				}()
				runStreaming(ctx, q, c, body)
			}()
			return map[string]any{"headers": http.Header{"Content-Type": {"text/event-stream"}, "Cache-Control": {"no-cache"}, "X-CPA-AgentRouter-Continuity-Version": {version}}}, nil
		}
		defer release()
		result, err := runJSON(context.Background(), q, c, body)
		return sdk.ExecutorResponse{Payload: result}, err
	case "executor.count_tokens", "executor.http_request", "auth.login_start", "auth.login_poll":
		return nil, &rpcError{"ar_unsupported_operation", "Operation not implemented by AR continuity", 400}
	case "management.register":
		return managementRegistration(), nil
	case "management.handle":
		return managementHandle(raw)
	default:
		return nil, &rpcError{"unsupported_method", "Unsupported plugin method", 400}
	}
}

func shutdown() { active.Range(func(_, v any) bool { v.(context.CancelFunc)(); return true }) }

func prepareBody(raw []byte, model string, stream bool) ([]byte, error) {
	var b map[string]json.RawMessage
	if json.Unmarshal(raw, &b) != nil || b == nil {
		return nil, &rpcError{"invalid_request", "Expected Responses JSON object", 400}
	}
	// Preserve unknown fields, compaction, encrypted state, and tool history.
	b["model"], _ = json.Marshal(model)
	b["stream"], _ = json.Marshal(stream)
	b["store"] = json.RawMessage("false")
	return json.Marshal(b)
}
