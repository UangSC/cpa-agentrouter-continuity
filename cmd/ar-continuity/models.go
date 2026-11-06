package main

import (
	"encoding/json"
	"sort"
	"strings"
	"unicode"

	"github.com/UangSC/cpa-agentrouter-continuity/internal/sdk"
)

// Fetch only the AR model catalog. Draft keys are never persisted or returned.
func fetchModels(q managementRequest) sdk.ManagementResponse {
	if len(q.Body) > 16<<10 {
		return jsonResponse(413, map[string]string{"error": "请求过大"})
	}
	var input struct {
		Index      string `json:"auth_index"`
		Key        string `json:"api_key"`
		UserAgent  string `json:"user_agent"`
		Originator string `json:"originator"`
	}
	if json.Unmarshal(q.Body, &input) != nil {
		return jsonResponse(400, map[string]string{"error": "请求格式错误"})
	}
	c := credential{Type: pluginID, BaseURL: "https://agentrouter.org/v1", Headers: map[string]string{}}
	if input.Index != "" {
		var e error
		c, _, e = getOwnCredential(q.CallbackID, input.Index)
		if e != nil {
			return managementFailure(e)
		}
	}
	if strings.TrimSpace(input.Key) != "" {
		c.APIKey = strings.TrimSpace(input.Key)
	}
	if c.APIKey == "" {
		return jsonResponse(400, map[string]string{"error": "请先填写 AR API Key，再获取模型。"})
	}
	// Only use the AR identity fields from the form, with the normal built-in defaults.
	if strings.ContainsAny(input.UserAgent+input.Originator, "\r\n") {
		return jsonResponse(400, map[string]string{"error": "请求头不能包含换行"})
	}
	c.Headers = map[string]string{"User-Agent": input.UserAgent, "Originator": input.Originator}
	if validateCredential(c) != nil {
		return jsonResponse(400, map[string]string{"error": "请检查 AR Key 和 Codex/Zcode 请求头"})
	}
	var response httpStart
	err := hostCall("host.http.do_stream", map[string]any{"host_callback_id": q.CallbackID, "method": "GET", "url": "https://agentrouter.org/v1/models", "headers": upstreamHeaders(c, execution{})}, &response)
	if err != nil {
		return jsonResponse(502, map[string]string{"error": "无法连接 AR 模型接口，请稍后重试。"})
	}
	raw, err := readAll(response.StreamID)
	if err != nil {
		return jsonResponse(502, map[string]string{"error": "读取模型列表失败或响应过大，请稍后重试。"})
	}
	if response.Status < 200 || response.Status >= 300 {
		message := "AR 模型接口暂不可用，请稍后重试。"
		if response.Status == 401 || response.Status == 403 {
			message = "AR Key 或请求头未通过上游验证，请检查后重试。"
		}
		if response.Status == 429 {
			message = "AR 模型接口请求过于频繁，请稍后重试。"
		}
		return jsonResponse(502, map[string]any{"error": message, "upstream_status": response.Status})
	}
	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Data == nil {
		return jsonResponse(502, map[string]string{"error": "AR 返回的模型列表格式不正确。"})
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, model := range result.Data {
		id := strings.TrimSpace(model.ID)
		if id == "" || len(id) > 160 || strings.Contains(id, "=") || id == c.APIKey {
			continue
		}
		valid := true
		for _, r := range id {
			if unicode.IsControl(r) {
				valid = false
				break
			}
		}
		if valid && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
		if len(ids) >= 10000 {
			break
		}
	}
	sort.Strings(ids)
	return jsonResponse(200, map[string]any{"models": ids, "count": len(ids)})
}
