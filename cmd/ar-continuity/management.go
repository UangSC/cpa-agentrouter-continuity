package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/UangSC/cpa-agentrouter-continuity/internal/sdk"
	webui "github.com/UangSC/cpa-agentrouter-continuity/web"
)

const apiPrefix = "/v0/management/plugins/cpa-agentrouter-continuity"
const resourcePrefix = "/v0/resource/plugins/cpa-agentrouter-continuity"

type managementRequest struct {
	sdk.ManagementRequest
	CallbackID string `json:"host_callback_id"`
}
type credentialView struct {
	Index                 string            `json:"auth_index"`
	ID                    string            `json:"id"`
	Label                 string            `json:"label"`
	Name                  string            `json:"name"`
	MaskedKey             string            `json:"masked_key"`
	Disabled              bool              `json:"disabled"`
	Prefix                string            `json:"model_prefix"`
	Models                []sdk.ModelInfo   `json:"models"`
	Headers               map[string]string `json:"headers"`
	MaxRetries            int               `json:"max_retries"`
	RetryBudgetSeconds    int               `json:"retry_budget_seconds"`
	MaxConcurrentSessions int               `json:"max_concurrent_sessions"`
	ActiveSessions        int               `json:"active_sessions"`
	Health                string            `json:"health"`
}
type healthResult struct {
	HTTPStatus int       `json:"http_status"`
	CheckedAt  time.Time `json:"checked_at"`
	LatencyMS  int64     `json:"latency_ms"`
	OK         bool      `json:"ok"`
	Message    string    `json:"message"`
}

var healthMu sync.Mutex
var healthChecks = map[string]healthResult{}
var credentialWriteMu sync.Mutex

func managementRegistration() any {
	routes := []map[string]string{}
	for _, item := range [][2]string{{"GET", "/overview"}, {"GET", "/credentials"}, {"POST", "/credentials"}, {"PUT", "/credentials"}, {"POST", "/health"}, {"POST", "/models"}, {"GET", "/pricing"}} {
		routes = append(routes, map[string]string{"method": item[0], "path": apiPrefix + item[1]})
	}
	return map[string]any{"routes": routes, "resources": []map[string]string{{"path": "/ui", "menu": "AR Continuity", "description": "AR API Key、请求日志与健康状态"}, {"path": "/status"}, {"path": "/app.js"}, {"path": "/dashboard.js"}, {"path": "/monitoring.js"}, {"path": "/pricing.js"}, {"path": "/monitoring.css"}, {"path": "/style.css"}, {"path": "/logo.png"}}}
}
func jsonResponse(status int, value any) sdk.ManagementResponse {
	body, _ := json.Marshal(value)
	return sdk.ManagementResponse{StatusCode: status, Headers: http.Header{"Content-Type": {"application/json; charset=utf-8"}, "Cache-Control": {"no-store"}}, Body: body}
}
func managementHandle(raw []byte) (any, error) {
	var q managementRequest
	if json.Unmarshal(raw, &q) != nil {
		return jsonResponse(400, map[string]string{"error": "请求格式错误"}), nil
	}
	if strings.HasPrefix(q.Path, resourcePrefix+"/") {
		asset := path.Base(q.Path)
		mime := "text/html; charset=utf-8"
		switch asset {
		case "ui", "status":
			asset = "index.html"
		case "app.js", "dashboard.js", "monitoring.js", "pricing.js":
			mime = "text/javascript; charset=utf-8"
		case "style.css", "monitoring.css":
			mime = "text/css; charset=utf-8"
		case "logo.png":
			mime = "image/png"
		default:
			return jsonResponse(404, map[string]string{"error": "页面不存在"}), nil
		}
		body, e := webui.ReadFile(asset)
		if e != nil {
			return nil, e
		}
		return sdk.ManagementResponse{StatusCode: 200, Headers: http.Header{"Content-Type": {mime}, "Cache-Control": {"no-store"}, "X-Content-Type-Options": {"nosniff"}, "Referrer-Policy": {"same-origin"}}, Body: body}, nil
	}
	// Only registered management routes reach here; CPA validates the management key.
	if !strings.HasPrefix(q.Path, apiPrefix+"/") {
		return jsonResponse(404, map[string]string{"error": "接口不存在"}), nil
	}
	switch q.Method + " " + strings.TrimPrefix(q.Path, apiPrefix) {
	case "GET /overview":
		healthMu.Lock()
		health := make(map[string]healthResult, len(healthChecks))
		for k, v := range healthChecks {
			health[k] = v
		}
		healthMu.Unlock()
		return jsonResponse(200, map[string]any{"version": version, "requests": requests.Load(), "retries": retries.Load(), "failures": failures.Load(), "recovered": recovered.Load(), "history_policy": "preserve-and-new-session", "logs": monitorSnapshot(), "routing": routingSnapshot(), "health": health, "defaults": map[string]any{"user_agent": defaultUserAgent, "originator": defaultOriginator, "max_retries": 4, "retry_budget_seconds": 180, "max_concurrent_sessions": defaultMaxConcurrentSessions}}), nil
	case "GET /credentials":
		entries, e := listOwnCredentials(q.CallbackID)
		if e != nil {
			return managementFailure(e), nil
		}
		views := []credentialView{}
		activeByKey := map[string]int{}
		stateErr := withRoutingLedger(func(s *routingLedger) error {
			for _, lease := range s.Leases {
				if _, exists := activeByKey[lease.KeyHash]; !exists {
					activeByKey[lease.KeyHash] = len(keySessions(s, lease.KeyHash))
				}
			}
			return nil
		})
		for _, entry := range entries {
			c, _, e := getOwnCredential(q.CallbackID, entry.AuthIndex)
			if e != nil {
				continue
			}
			h := configuredHeaders(c)
			n := 4
			if c.MaxRetries != nil {
				n = *c.MaxRetries
			}
			sec := c.RetryBudgetSeconds
			if sec == 0 {
				sec = 180
			}
			label := c.Label
			if label == "" {
				label = "AgentRouter API Key"
			}
			state := "unknown"
			if entry.Unavailable {
				state = "unavailable"
			}
			active := activeByKey[digest(c.APIKey)]
			if stateErr != nil {
				active = -1
			}
			views = append(views, credentialView{Index: entry.AuthIndex, ID: c.ID, Label: label, Name: entry.Name, MaskedKey: maskKey(c.APIKey), Disabled: entry.Disabled, Prefix: c.Prefix, Models: c.Models, Headers: map[string]string{"User-Agent": h.Get("User-Agent"), "Originator": h.Get("Originator")}, MaxRetries: n, RetryBudgetSeconds: sec, MaxConcurrentSessions: sessionLimit(c), ActiveSessions: active, Health: state})
		}
		return jsonResponse(200, map[string]any{"credentials": views}), nil
	case "POST /credentials", "PUT /credentials":
		return saveManagedCredential(q), nil
	case "POST /models":
		return fetchModels(q), nil
	case "GET /pricing":
		return fetchChannelPricing(q), nil
	case "POST /health":
		return checkHealth(q), nil
	}
	return jsonResponse(404, map[string]string{"error": "接口不存在"}), nil
}
func managementFailure(err error) sdk.ManagementResponse {
	return jsonResponse(400, map[string]string{"error": "操作未完成，请检查凭证和服务权限。"})
}
func listOwnCredentials(callback string) ([]sdk.HostAuthFileEntry, error) {
	var all struct {
		Files []sdk.HostAuthFileEntry `json:"files"`
	}
	if e := hostCall("host.auth.list", map[string]string{"host_callback_id": callback}, &all); e != nil {
		return nil, e
	}
	own := []sdk.HostAuthFileEntry{}
	for _, entry := range all.Files {
		if entry.Provider == pluginID || entry.Type == pluginID {
			own = append(own, entry)
		}
	}
	return own, nil
}
func getOwnCredential(callback, index string) (credential, sdk.HostAuthGetResponse, error) {
	entries, e := listOwnCredentials(callback)
	if e != nil {
		return credential{}, sdk.HostAuthGetResponse{}, e
	}
	found := false
	for _, entry := range entries {
		if entry.AuthIndex == index && index != "" {
			found = true
			break
		}
	}
	if !found {
		return credential{}, sdk.HostAuthGetResponse{}, fmt.Errorf("AR credential not found")
	}
	var stored sdk.HostAuthGetResponse
	e = hostCall("host.auth.get", map[string]string{"host_callback_id": callback, "auth_index": index}, &stored)
	if e != nil {
		return credential{}, stored, e
	}
	c, e := decodeCredential(stored.JSON)
	return c, stored, e
}
func maskKey(key string) string {
	if len(key) < 8 {
		return "••••••••"
	}
	return "••••••••" + key[len(key)-4:]
}

type credentialEdit struct {
	Index                 string          `json:"auth_index"`
	Label                 string          `json:"label"`
	Key                   string          `json:"api_key"`
	Disabled              bool            `json:"disabled"`
	Prefix                string          `json:"model_prefix"`
	Models                []sdk.ModelInfo `json:"models"`
	UserAgent             string          `json:"user_agent"`
	Originator            string          `json:"originator"`
	MaxRetries            int             `json:"max_retries"`
	RetryBudgetSeconds    int             `json:"retry_budget_seconds"`
	MaxConcurrentSessions *int            `json:"max_concurrent_sessions"`
}

func saveManagedCredential(q managementRequest) sdk.ManagementResponse {
	credentialWriteMu.Lock()
	defer credentialWriteMu.Unlock()
	if len(q.Body) > 64<<10 {
		return jsonResponse(413, map[string]string{"error": "凭证配置过大"})
	}
	var edit credentialEdit
	if json.Unmarshal(q.Body, &edit) != nil {
		return jsonResponse(400, map[string]string{"error": "无效的凭证配置"})
	}
	if len(edit.Label) > 120 || len(edit.Prefix) > 64 || strings.ContainsAny(edit.Prefix, "\r\n") {
		return jsonResponse(400, map[string]string{"error": "名称或前缀过长"})
	}
	var c credential
	var stored sdk.HostAuthGetResponse
	var e error
	if q.Method == "PUT" {
		c, stored, e = getOwnCredential(q.CallbackID, edit.Index)
		if e != nil {
			return managementFailure(e)
		}
	} else {
		if edit.Index != "" {
			return jsonResponse(400, map[string]string{"error": "新增凭证不接受现有标识"})
		}
		random := make([]byte, 16)
		if _, e = rand.Read(random); e != nil {
			return managementFailure(e)
		}
		c = credential{Type: pluginID, ID: pluginID + "-" + hex.EncodeToString(random), BaseURL: "https://agentrouter.org/v1"}
		stored.Name = c.ID + ".json"
	}
	if strings.TrimSpace(edit.Key) != "" {
		c.APIKey = strings.TrimSpace(edit.Key)
	}
	c.Label = strings.TrimSpace(edit.Label)
	c.Disabled = edit.Disabled
	c.Prefix = strings.Trim(edit.Prefix, " /\t")
	c.MaxRetries = &edit.MaxRetries
	c.RetryBudgetSeconds = edit.RetryBudgetSeconds
	if edit.MaxConcurrentSessions != nil {
		if *edit.MaxConcurrentSessions < 1 || *edit.MaxConcurrentSessions > maximumConcurrentSessions {
			return jsonResponse(400, map[string]string{"error": "并发会话上限必须为 1–100 的整数"})
		}
		c.MaxConcurrentSessions = edit.MaxConcurrentSessions
	}
	if c.Headers == nil {
		c.Headers = map[string]string{}
	}
	for k := range c.Headers {
		if strings.EqualFold(k, "User-Agent") || strings.EqualFold(k, "Originator") {
			delete(c.Headers, k)
		}
	}
	c.Headers["User-Agent"] = edit.UserAgent
	c.Headers["Originator"] = edit.Originator
	if strings.ContainsAny(edit.UserAgent+edit.Originator, "\r\n") {
		return jsonResponse(400, map[string]string{"error": "请求头包含非法换行"})
	}
	if len(edit.Models) == 0 || len(edit.Models) > 100 {
		return jsonResponse(400, map[string]string{"error": "请配置 1–100 个模型"})
	}
	seen := map[string]bool{}
	for i := range edit.Models {
		m := &edit.Models[i]
		m.ID = strings.TrimSpace(m.ID)
		m.Name = strings.TrimSpace(m.Name)
		if m.ID == "" || len(m.ID) > 160 || len(m.Name) > 160 || seen[m.ID] || strings.ContainsAny(m.ID+m.Name, "\r\n") {
			return jsonResponse(400, map[string]string{"error": "模型名称无效或重复"})
		}
		seen[m.ID] = true
		m.Object = "model"
		m.OwnedBy = pluginID
		m.Type = "codex"
		m.UserDefined = true
		if m.Name == "" {
			m.Name = m.ID
		}
	}
	c.Models = edit.Models
	if e = validateCredential(c); e != nil {
		return jsonResponse(400, map[string]string{"error": "请检查 API Key、Codex/Zcode UA 和重试范围"})
	}
	// Merge edited fields into the existing document to retain host-owned settings.
	merged := map[string]json.RawMessage{}
	if len(stored.JSON) > 0 {
		_ = json.Unmarshal(stored.JSON, &merged)
	}
	encoded, _ := json.Marshal(c)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(encoded, &fields)
	for _, name := range []string{"model_prefix", "label", "disabled", "headers", "max_retries", "retry_budget_seconds", "max_concurrent_sessions"} {
		delete(merged, name)
	}
	for k, v := range fields {
		merged[k] = v
	}
	body, _ := json.Marshal(merged)
	var saved sdk.HostAuthSaveResponse
	e = hostCall("host.auth.save", map[string]any{"host_callback_id": q.CallbackID, "name": stored.Name, "json": json.RawMessage(body)}, &saved)
	if e != nil {
		return managementFailure(e)
	}
	return jsonResponse(200, map[string]any{"saved": true, "name": saved.Name})
}
func checkHealth(q managementRequest) sdk.ManagementResponse {
	var input struct {
		Index string `json:"auth_index"`
	}
	if json.Unmarshal(q.Body, &input) != nil {
		return jsonResponse(400, map[string]string{"error": "请选择 AR 凭证"})
	}
	c, _, e := getOwnCredential(q.CallbackID, input.Index)
	if e != nil {
		return managementFailure(e)
	}
	start := time.Now()
	healthMu.Lock()
	last, exists := healthChecks[c.ID]
	healthMu.Unlock()
	if exists && time.Since(last.CheckedAt) < 10*time.Second {
		return jsonResponse(429, map[string]string{"error": "请等待 10 秒后再次检测"})
	}
	var resp struct {
		StatusCode int
		Body       []byte
	}
	e = hostCall("host.http.do", map[string]any{"host_callback_id": q.CallbackID, "method": "GET", "url": "https://agentrouter.org/v1/models", "headers": upstreamHeaders(c, execution{})}, &resp)
	result := healthResult{CheckedAt: time.Now().UTC(), LatencyMS: time.Since(start).Milliseconds(), HTTPStatus: resp.StatusCode, OK: e == nil && resp.StatusCode == 200, Message: "模型列表接口不可用"}
	if result.OK {
		result.Message = "模型列表接口正常；实际生成状态以请求日志为准"
	} else if resp.StatusCode == 401 || resp.StatusCode == 403 {
		result.Message = "认证或权限未通过，请检查 AR Key 和请求头"
	}
	healthMu.Lock()
	healthChecks[c.ID] = result
	healthMu.Unlock()
	return jsonResponse(200, result)
}
