package main

import (
	"context"
	"encoding/json"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// incompleteReason extracts the Responses API terminal reason from either a
// response.incomplete SSE event or a non-stream response object.  The API
// places incomplete_details at the top level for JSON and under response for
// events, so both shapes must be handled.
func incompleteReason(raw []byte) string {
	var root struct {
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Response *struct {
			IncompleteDetails *struct {
				Reason string `json:"reason"`
			} `json:"incomplete_details"`
		} `json:"response"`
	}
	if json.Unmarshal(raw, &root) != nil {
		return ""
	}
	if root.IncompleteDetails != nil && root.IncompleteDetails.Reason != "" {
		return normalizeIncompleteReason(root.IncompleteDetails.Reason)
	}
	if root.Response != nil && root.Response.IncompleteDetails != nil {
		return normalizeIncompleteReason(root.Response.IncompleteDetails.Reason)
	}
	return ""
}

func normalizeIncompleteReason(reason string) string {
	reason = strings.ToLower(strings.TrimSpace(reason))
	return strings.ReplaceAll(reason, "-", "_")
}

func incompleteError(raw []byte) *rpcError {
	if incompleteReason(raw) != "content_filter" {
		return nil
	}
	return &rpcError{
		Code:       "ar_content_filter",
		Message:    "上游返回未完成响应，原因：content_filter。插件未自动重试，以避免重复扣费；请使用空白会话重试。",
		HTTPStatus: 400,
	}
}

func classify(status int, raw []byte) string {
	if pricingConfigError(status, raw) != nil {
		return pricingErrorKind
	}
	var root map[string]any
	_ = json.Unmarshal(raw, &root)
	v := root
	if x, ok := root["response"].(map[string]any); ok {
		v = x
	}
	if x, ok := v["error"].(map[string]any); ok {
		v = x
	}
	detail := upstreamErrorDetails(raw)
	code := strings.ToLower(detail.Code + " " + detail.Type)
	msg := strings.ToLower(detail.Message)
	if status == 401 || status == 403 {
		return ""
	}
	for _, s := range []string{"insufficient_quota", "quota_exceeded", "invalid_api_key", "authentication", "insufficient_balance", "credit", "billing", "余额不足", "额度耗尽"} {
		if strings.Contains(code+" "+msg, s) {
			return ""
		}
	}
	if historyValidationFailure(code, msg) {
		return "history"
	}
	if status == 429 || strings.Contains(code, "rate_limit") || strings.Contains(msg, "exceeded token rate limit") {
		return "rate"
	}
	if status >= 500 || strings.Contains(code, "no_available_channel") || strings.Contains(code, "server_error") || strings.Contains(code, "bad_response") {
		return "transport"
	}
	if root["type"] == "response.failed" && (root["response"] == nil || v["error"] == nil && v["code"] == nil && v["message"] == nil) {
		return "transport"
	}
	return ""
}

func retryDelay(kind string, index int, h http.Header) time.Duration {
	base := []int{1, 2, 4, 8, 15}
	if kind == "rate" {
		base = []int{10, 20, 40, 60, 60}
	} else if kind == "history" {
		base = []int{1, 3}
	}
	d := time.Duration(base[min(index, len(base)-1)])*time.Second + time.Duration(rand.Intn(250))*time.Millisecond
	for _, k := range []string{"retry-after-ms", "x-ms-retry-after-ms"} {
		if n, e := strconv.ParseFloat(h.Get(k), 64); e == nil && n > 0 {
			d = max(d, time.Duration(n*float64(time.Millisecond)))
		}
	}
	if v := h.Get("Retry-After"); v != "" {
		if n, e := strconv.ParseFloat(v, 64); e == nil && n > 0 {
			d = max(d, time.Duration(n*float64(time.Second)))
		} else if t, e := http.ParseTime(v); e == nil {
			d = max(d, time.Until(t))
		}
	}
	return d
}

type budget struct {
	used        int
	historyUsed int
	max         int
	end         time.Time
}

func newBudget(c credential) budget {
	n := 4
	if c.MaxRetries != nil {
		n = *c.MaxRetries
	}
	sec := 180
	if c.RetryBudgetSeconds > 0 {
		sec = c.RetryBudgetSeconds
	}
	return budget{max: n, end: time.Now().Add(time.Duration(sec) * time.Second)}
}

var waitRetry = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (b *budget) retry(ctx context.Context, kind string, h http.Header) bool {
	if kind == "" || b.used >= b.max || ctx.Err() != nil {
		return false
	}
	if kind == pricingErrorKind || kind == "history" && b.historyUsed >= 2 {
		return false
	}
	index := b.used
	if kind == "history" {
		index = b.historyUsed
	}
	delay := retryDelay(kind, index, h)
	if time.Now().Add(delay).After(b.end) {
		return false
	}
	recordRetry(ctx, kind)
	b.used++
	if kind == "history" {
		b.historyUsed++
	}
	retries.Add(1)
	return waitRetry(ctx, delay) == nil
}

func transportKind(err error) string {
	v := strings.ToLower(err.Error())
	for _, s := range []string{"cancel", "not found", "not open", "unavailable", "invalid callback", "closed callback"} {
		if strings.Contains(v, s) {
			return ""
		}
	}
	return "transport"
}

func errorType(status int) string {
	if status == 400 {
		return "invalid_request_error"
	}
	if status == 401 {
		return "authentication_error"
	}
	if status == 403 {
		return "permission_error"
	}
	if status == 429 {
		return "rate_limit_error"
	}
	return "server_error"
}
