package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

const historyErrorCode = "ar_history_verification_failed"

type upstreamErrorDetail struct{ Code, Type, Message, TraceID string }

func upstreamErrorDetails(raw []byte) upstreamErrorDetail {
	var root map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil {
		return upstreamErrorDetail{}
	}
	read := func(obj map[string]json.RawMessage, key string) string {
		var v string
		_ = json.Unmarshal(obj[key], &v)
		return v
	}
	trace := ""
	setTrace := func(value string) {
		value = strings.TrimSpace(value)
		if pricingTraceValue.MatchString(value) {
			trace = value
		}
	}
	setTrace(read(root, "trace_id"))
	var response map[string]json.RawMessage
	if json.Unmarshal(root["response"], &response) == nil && response != nil {
		root = response
		setTrace(read(root, "trace_id"))
	}
	var detail map[string]json.RawMessage
	if json.Unmarshal(root["error"], &detail) == nil && detail != nil {
		root = detail
		setTrace(read(root, "trace_id"))
	} else if message := read(root, "error"); message != "" {
		return upstreamErrorDetail{Message: message, TraceID: trace}
	} else if len(root["output"]) > 0 || read(root, "status") == "completed" {
		return upstreamErrorDetail{}
	}
	d := upstreamErrorDetail{Code: read(root, "code"), Type: read(root, "type"), Message: read(root, "message"), TraceID: trace}
	if d.TraceID == "" {
		if match := pricingTraceID.FindStringSubmatch(d.Message); len(match) > 1 {
			d.TraceID = match[1]
		}
	}
	return d
}

func historyValidationFailure(code, message string) bool {
	code, message = strings.ToLower(code), strings.ToLower(message)
	return strings.Contains(code+" "+message, "invalid_encrypted_content") || strings.Contains(message, "different azure openai resource") ||
		(strings.Contains(message, "encrypted content") && strings.Contains(message, "item rs_") &&
			(strings.Contains(message, "could not be verified") || strings.Contains(message, "could not be decrypted")))
}

func failureKind(status int, raw []byte, cause error) string {
	kind := classify(status, raw)
	if kind == "history" || kind == pricingErrorKind || status == 401 || status == 403 {
		return kind
	}
	if cause != nil {
		if historyValidationFailure("", cause.Error()) {
			return "history"
		}
		return transportKind(cause)
	}
	return kind
}

func safeErrorWithCause(status int, raw []byte, cause error, secrets ...string) *rpcError {
	if pricing := pricingFailure(status, raw, cause, secrets...); pricing != nil {
		return pricing
	}
	d := upstreamErrorDetails(raw)
	if d.Message == "" && cause != nil {
		d.Message = cause.Error()
	}
	if d.TraceID == "" {
		if match := pricingTraceID.FindStringSubmatch(d.Message); len(match) > 1 {
			d.TraceID = match[1]
		}
	}
	history := historyValidationFailure(d.Code, d.Message)
	if status < 400 {
		status = 502
	}
	code := d.Code
	if history {
		code = historyErrorCode
		status = 400
	}
	if code == "" {
		code = "ar_upstream_error"
		if cause != nil {
			v := strings.ToLower(cause.Error())
			switch {
			case strings.Contains(v, "timeout"), strings.Contains(v, "deadline exceeded"):
				code = "ar_upstream_timeout"
			case strings.Contains(v, "eof"), strings.Contains(v, "sse"), strings.Contains(v, "content-type"):
				code = "ar_upstream_response_invalid"
			default:
				code = "ar_upstream_connection_error"
			}
		} else if len(raw) > 0 && d.Message == "" {
			code = "ar_upstream_response_invalid"
		}
	}
	redact := func(value string) string {
		for _, secret := range secrets {
			if secret != "" {
				encoded, _ := json.Marshal(secret)
				value = strings.ReplaceAll(value, string(encoded[1:len(encoded)-1]), "[REDACTED]")
				value = strings.ReplaceAll(value, secret, "[REDACTED]")
			}
		}
		return value
	}
	prefix := fmt.Sprintf("AgentRouter 上游请求失败（HTTP %d）。", status)
	if history {
		prefix = "上游暂时无法校验本会话的加密历史。原历史已保留；可稍后再试，持续失败再考虑新建会话。"
	}
	if d.TraceID != "" {
		prefix += " [trace_id=" + redact(d.TraceID) + "]"
	}
	message := strings.Join(strings.Fields(redact(d.Message)), " ")
	if message == "" {
		message = "上游未返回可读的错误详情"
	}
	runes := []rune(message)
	if len(runes) > 350 {
		message = string(runes[:350]) + "…"
	}
	return &rpcError{Code: code, Message: prefix + " 原因：" + message, HTTPStatus: status}
}
