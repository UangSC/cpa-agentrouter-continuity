package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/UangSC/cpa-agentrouter-continuity/internal/sdk"
)

const channelPricingURL = "https://agentrouter.org/api/pricing"

type channelPrice struct {
	Input        float64 `json:"input"`
	Cached       float64 `json:"cached"`
	Output       float64 `json:"output"`
	CacheAssumed bool    `json:"cache_assumed"`
}

// Match the public pricing page: model_ratio * 2 * group_ratio, USD / 1M tokens.
// The public endpoint does not reveal account-specific or tiered billing.
func parseChannelPricing(raw []byte) (map[string]channelPrice, int, error) {
	var response struct {
		Success    bool                `json:"success"`
		GroupRatio map[string]*float64 `json:"group_ratio"`
		Data       []json.RawMessage   `json:"data"`
	}
	if json.Unmarshal(raw, &response) != nil || !response.Success || response.Data == nil {
		return nil, 0, fmt.Errorf("invalid pricing catalog")
	}
	group := response.GroupRatio["default"]
	valid := func(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= 1000000 }
	if group == nil || !valid(*group) || len(response.Data) > 10000 {
		return nil, 0, fmt.Errorf("invalid pricing group")
	}
	prices := map[string]channelPrice{}
	skipped := 0
	seen := map[string]bool{}
	for _, item := range response.Data {
		var row struct {
			Model      string   `json:"model_name"`
			QuotaType  *int     `json:"quota_type"`
			Ratio      *float64 `json:"model_ratio"`
			Completion *float64 `json:"completion_ratio"`
			Cache      *float64 `json:"cache_ratio"`
			Groups     []string `json:"enable_groups"`
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(item, &row) != nil || json.Unmarshal(item, &fields) != nil {
			skipped++
			continue
		}
		tiered := false
		for key, value := range fields {
			k := strings.ToLower(key)
			if (strings.Contains(k, "tier") || strings.Contains(k, "expr")) && string(value) != "null" && string(value) != "\"\"" {
				tiered = true
			}
		}
		name := strings.TrimSpace(row.Model)
		enabled := false
		for _, g := range row.Groups {
			if g == "default" {
				enabled = true
			}
		}
		invalidName := name == "" || len(name) > 160 || strings.IndexFunc(name, unicode.IsControl) >= 0
		if invalidName || tiered || !enabled || row.QuotaType == nil || *row.QuotaType != 0 || row.Ratio == nil || row.Completion == nil || !valid(*row.Ratio) || !valid(*row.Completion) {
			skipped++
			continue
		}
		if seen[name] {
			delete(prices, name)
			skipped++
			continue
		}
		seen[name] = true
		p := channelPrice{Input: *row.Ratio * 2 * (*group), CacheAssumed: row.Cache == nil}
		p.Output = p.Input * (*row.Completion)
		p.Cached = p.Input
		if row.Cache != nil {
			if !valid(*row.Cache) {
				skipped++
				continue
			}
			p.Cached = p.Input * (*row.Cache)
		}
		if !valid(p.Input) || !valid(p.Output) || !valid(p.Cached) {
			skipped++
			continue
		}
		prices[name] = p
	}
	if len(prices) == 0 {
		return nil, skipped, fmt.Errorf("no supported model prices")
	}
	return prices, skipped, nil
}

func fetchChannelPricing(q managementRequest) sdk.ManagementResponse {
	var response httpStart
	// Fixed public endpoint, no management key, AR key, account cookie or request body.
	err := hostCall("host.http.do_stream", map[string]any{"host_callback_id": q.CallbackID, "method": "GET", "url": channelPricingURL, "headers": http.Header{"Accept": {"application/json"}, "User-Agent": {defaultUserAgent}, "Accept-Encoding": {"identity"}}}, &response)
	if err != nil {
		closeHTTP(response.StreamID)
		return jsonResponse(502, map[string]string{"error": "无法连接渠道定价接口，请检查 CPA 网络后重试；当前单价未修改。"})
	}
	raw, err := readAll(response.StreamID)
	if err != nil || response.Status < 200 || response.Status >= 300 {
		return jsonResponse(502, map[string]string{"error": "渠道定价接口暂不可用；当前单价未修改。"})
	}
	prices, skipped, err := parseChannelPricing(raw)
	if err != nil {
		return jsonResponse(502, map[string]string{"error": "渠道未返回可识别的默认分组 Token 单价；当前单价未修改。"})
	}
	return jsonResponse(200, map[string]any{"prices": prices, "source": channelPricingURL, "fetched_at": time.Now().UTC(), "currency": "USD", "unit": "million_tokens", "group": "default", "skipped": skipped})
}
