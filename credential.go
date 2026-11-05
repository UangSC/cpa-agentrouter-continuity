package main

import (
	"encoding/json"
	"fmt"
	"github.com/UangSC/cpa-agentrouter-continuity/internal/sdk"
	"net/http"
	"net/url"
	"strings"
)

type credential struct {
	Label              string            `json:"label,omitempty"`
	Type               string            `json:"type"`
	ID                 string            `json:"id"`
	APIKey             string            `json:"api_key"`
	BaseURL            string            `json:"base_url"`
	Headers            map[string]string `json:"headers"`
	Models             []sdk.ModelInfo   `json:"models"`
	Prefix             string            `json:"model_prefix,omitempty"`
	Disabled           bool              `json:"disabled,omitempty"`
	MaxRetries         *int              `json:"max_retries,omitempty"`
	RetryBudgetSeconds int               `json:"retry_budget_seconds,omitempty"`
}

const defaultUserAgent = "codex_exec/0.159.3 (Windows 10.0.19045; x86_64) dumb (codex_exec; 0.159.3)"

const defaultOriginator = "codex_exec"

// Defaults contain client identification only; authentication is always runtime data.

func configuredHeaders(c credential) http.Header {
	h := http.Header{}
	for k, v := range c.Headers {
		h.Set(k, v)
	}
	if strings.TrimSpace(h.Get("User-Agent")) == "" {
		h.Set("User-Agent", defaultUserAgent)
	}
	if strings.TrimSpace(h.Get("Originator")) == "" {
		h.Set("Originator", defaultOriginator)
	}
	return h
}

func validateCredential(c credential) error {
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Scheme != "https" || u.Host != "agentrouter.org" || strings.TrimRight(u.Path, "/") != "/v1" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return &rpcError{"ar_scope_violation", "Only https://agentrouter.org/v1 is permitted", 400}
	}
	if c.Type != pluginID || strings.TrimSpace(c.APIKey) == "" {
		return &rpcError{"ar_credential_invalid", "A runtime AR API key is required; no key is bundled", 401}
	}
	h := configuredHeaders(c)
	ua := strings.ToLower(h.Get("User-Agent"))
	if !strings.Contains(ua, "codex") && !strings.Contains(ua, "zcode") {
		return &rpcError{"ar_user_agent_required", "AR requires a configured Codex or Zcode User-Agent", 400}
	}
	if c.MaxRetries != nil && (*c.MaxRetries < 0 || *c.MaxRetries > 10) {
		return fmt.Errorf("max_retries must be between 0 and 10")
	}
	if c.RetryBudgetSeconds < 0 || c.RetryBudgetSeconds > 600 {
		return fmt.Errorf("retry budget must be between 0 and 600 seconds")
	}
	return nil
}

func decodeCredential(raw []byte) (credential, error) {
	var c credential
	err := json.Unmarshal(raw, &c)
	if err == nil {
		err = validateCredential(c)
	}
	return c, err
}

func authData(c credential, raw []byte, file string) sdk.AuthData {
	return sdk.AuthData{Provider: pluginID, ID: c.ID, FileName: file, Label: credentialLabel(c), Disabled: c.Disabled, StorageJSON: raw,
		Metadata: map[string]any{"type": pluginID, "request_retry": 0}, Attributes: map[string]string{"ar_base_url": c.BaseURL, "ar_key_hash": digest(c.APIKey)}}
}

func modelForCredential(c credential, requested string) (string, error) {
	for _, m := range c.Models {
		if requested == publicModelID(c, m.ID) {
			if m.Name != "" {
				return m.Name, nil
			}
			return m.ID, nil
		}
	}
	return "", &rpcError{"model_not_found", "Model is not declared by this AR credential", 404}
}

func publicModelID(c credential, id string) string {
	if p := strings.Trim(c.Prefix, " /\t"); p != "" {
		return p + "/" + id
	}
	return id
}

func publicModels(c credential) []sdk.ModelInfo {
	out := append([]sdk.ModelInfo(nil), c.Models...)
	for i := range out {
		out[i].ID = publicModelID(c, out[i].ID)
	}
	return out
}

func credentialLabel(c credential) string {
	if c.Label != "" {
		return c.Label
	}
	return "AgentRouter API Key"
}
