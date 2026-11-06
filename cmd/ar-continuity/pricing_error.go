package main

import (
	"encoding/json"
	"regexp"
	"strings"
)

const pricingErrorKind = "pricing_config"
const pricingErrorCode = "ar_upstream_pricing_error"

var pricingUnknownName = regexp.MustCompile("(?i)unknown\\s+name\\s+[`\"']?([A-Za-z_][A-Za-z0-9_]{0,63})\\b")
var pricingTraceID = regexp.MustCompile(`(?i)\btrace_id\s*[:=]\s*([A-Za-z0-9_.:-]{1,128})`)
var pricingTraceValue = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

// Inspect error fields only: successful model output may quote the same diagnostic.
// Text bodies are accepted only on a failed HTTP response or a host callback error.
func pricingConfigError(status int, raw []byte, secrets ...string) *rpcError {
	message, code, trace := "", "", ""
	var root map[string]json.RawMessage
	readString := func(obj map[string]json.RawMessage, name string) string {
		var value string
		_ = json.Unmarshal(obj[name], &value)
		return value
	}
	setTrace := func(value string) {
		value = strings.TrimSpace(value)
		if pricingTraceValue.MatchString(value) {
			trace = value
		}
	}
	if json.Unmarshal(raw, &root) == nil {
		setTrace(readString(root, "trace_id"))
		var response map[string]json.RawMessage
		if json.Unmarshal(root["response"], &response) == nil && response != nil {
			root = response
			setTrace(readString(root, "trace_id"))
		}
		var detail map[string]json.RawMessage
		if json.Unmarshal(root["error"], &detail) == nil && detail != nil {
			message, code = readString(detail, "message"), readString(detail, "code")
			setTrace(readString(detail, "trace_id"))
		} else if status >= 400 || readString(root, "type") == "error" || readString(root, "status") == "failed" {
			message, code = readString(root, "message"), readString(root, "code")
			if message == "" {
				message = readString(root, "error")
			}
		}
	} else if status >= 400 {
		message = strings.TrimSpace(string(raw))
	}
	flat := strings.ToLower(strings.Join(strings.Fields(message), " "))
	if code != pricingErrorCode && !(strings.Contains(flat, "tiered expr run failed") && strings.Contains(flat, "expr compile error")) {
		return nil
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
	// Extract diagnostics before bounding the explanatory excerpt, so a trace ID
	// after a long pricing expression is not truncated away.
	if !pricingTraceValue.MatchString(trace) {
		trace = ""
		if match := pricingTraceID.FindStringSubmatch(message); len(match) > 1 {
			trace = match[1]
		}
	}
	result := "上游模型计费规则异常，请联系上游维护者。插件未自动重试。"
	if match := pricingUnknownName.FindStringSubmatch(message); len(match) > 1 {
		result += " 未知变量：" + redact(match[1]) + "。"
	}
	if trace != "" {
		result += " [trace_id=" + redact(trace) + "]"
	}
	firstLine := strings.SplitN(message, "\n", 2)[0]
	if excerpt := []rune(strings.Join(strings.Fields(redact(firstLine)), " ")); len(excerpt) > 0 {
		if len(excerpt) > 240 {
			excerpt = append(excerpt[:240], '…')
		}
		result += " 原因：" + string(excerpt)
	}
	// Preserve the existing non-retryable HTTP 400 contract even if the upstream
	// wraps this deterministic configuration failure in HTTP 500 or an SSE error.
	return &rpcError{Code: pricingErrorCode, Message: result, HTTPStatus: 400}
}

func pricingFailure(status int, raw []byte, err error, secrets ...string) *rpcError {
	if failure := pricingConfigError(status, raw, secrets...); failure != nil {
		return failure
	}
	if err != nil {
		return pricingConfigError(502, []byte(err.Error()), secrets...)
	}
	return nil
}
