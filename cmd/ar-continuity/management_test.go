package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/UangSC/cpa-agentrouter-continuity/internal/sdk"
)

type managementMock struct {
	document json.RawMessage
	saves    int
	lastName string
	gets     int
}

func (m *managementMock) call(method string, value any, out any) error {
	raw, _ := json.Marshal(value)
	var q map[string]json.RawMessage
	_ = json.Unmarshal(raw, &q)
	switch method {
	case "host.auth.list":
		data := []byte(`{"files":[{"auth_index":"ar-index","id":"ar-id","name":"ar-existing.json","type":"cpa-agentrouter-continuity","provider":"cpa-agentrouter-continuity"},{"auth_index":"other-index","name":"codex-other.json","provider":"codex"}]}`)
		return json.Unmarshal(data, out)
	case "host.auth.get":
		m.gets++
		var index string
		_ = json.Unmarshal(q["auth_index"], &index)
		if index != "ar-index" {
			return fmt.Errorf("unrelated credential access")
		}
		*(out.(*sdk.HostAuthGetResponse)) = sdk.HostAuthGetResponse{AuthIndex: index, Name: "ar-existing.json", JSON: m.document}
	case "host.auth.save":
		m.saves++
		_ = json.Unmarshal(q["name"], &m.lastName)
		m.document = q["json"]
		*(out.(*sdk.HostAuthSaveResponse)) = sdk.HostAuthSaveResponse{Name: m.lastName}
	default:
		return fmt.Errorf("unexpected callback")
	}
	return nil
}
func setupManagementMock(t *testing.T) *managementMock {
	c := testCredential()
	c.Label = "<script>test</script>"
	c.Models = []sdk.ModelInfo{{ID: "model", Name: "model"}}
	raw, _ := json.Marshal(c)
	var v map[string]any
	_ = json.Unmarshal(raw, &v)
	v["priority"] = 7
	v["custom_preserved"] = true
	raw, _ = json.Marshal(v)
	m := &managementMock{document: raw}
	old := hostCall
	hostCall = m.call
	t.Cleanup(func() { hostCall = old })
	return m
}
func TestManagementOnlyListsMaskedARCredentials(t *testing.T) {
	setupManagementMock(t)
	raw, _ := json.Marshal(managementRequest{ManagementRequest: sdk.ManagementRequest{Method: "GET", Path: apiPrefix + "/credentials"}})
	result, e := managementHandle(raw)
	if e != nil {
		t.Fatal(e)
	}
	body := result.(sdk.ManagementResponse).Body
	if strings.Contains(string(body), "mock-secret") || strings.Contains(string(body), "codex-other") {
		t.Fatal("credential leaked")
	}
	var v struct{ Credentials []credentialView }
	if json.Unmarshal(body, &v) != nil || len(v.Credentials) != 1 {
		t.Fatal(string(body))
	}
	if v.Credentials[0].MaskedKey == "" {
		t.Fatal("mask absent")
	}
}
func TestManagementRejectsOtherProviderBeforeReading(t *testing.T) {
	m := setupManagementMock(t)
	if _, _, e := getOwnCredential("", "other-index"); e == nil {
		t.Fatal("unrelated auth accepted")
	}
	if m.gets != 0 {
		t.Fatal("unrelated credential read")
	}
}
func TestCredentialUpdatePreservesKeyAndHostFields(t *testing.T) {
	m := setupManagementMock(t)
	edit := credentialEdit{Index: "ar-index", Label: "Updated", Models: []sdk.ModelInfo{{ID: "new-model", Name: "upstream"}}, MaxRetries: 3, RetryBudgetSeconds: 120}
	raw, _ := json.Marshal(edit)
	r := saveManagedCredential(managementRequest{ManagementRequest: sdk.ManagementRequest{Method: "PUT", Body: raw}})
	if r.StatusCode != 200 {
		t.Fatal(string(r.Body))
	}
	var v map[string]any
	_ = json.Unmarshal(m.document, &v)
	if v["api_key"] != "mock-secret" || v["priority"] != float64(7) || v["custom_preserved"] != true {
		t.Fatal("preserved fields changed")
	}
	if m.lastName != "ar-existing.json" {
		t.Fatal("unexpected save target")
	}
	if strings.Contains(string(r.Body), "mock-secret") {
		t.Fatal("key in response")
	}
}
func TestCredentialCreationDoesNotAcceptCallerFilename(t *testing.T) {
	m := setupManagementMock(t)
	raw := []byte(`{"label":"New","api_key":"mock-new-only","name":"codex-other.json","id":"codex","models":[{"ID":"test"}],"max_retries":4,"retry_budget_seconds":180,"disabled":true}`)
	r := saveManagedCredential(managementRequest{ManagementRequest: sdk.ManagementRequest{Method: "POST", Body: raw}})
	if r.StatusCode != 200 {
		t.Fatal(string(r.Body))
	}
	if !strings.HasPrefix(m.lastName, "cpa-agentrouter-continuity-") || m.lastName == "codex-other.json" {
		t.Fatal("filename scope violation")
	}
	var c credential
	_ = json.Unmarshal(m.document, &c)
	if c.Type != pluginID || !c.Disabled {
		t.Fatal("scope changed")
	}
}
func TestInvalidCredentialUpdateNeverSaves(t *testing.T) {
	cases := []credentialEdit{{Index: "other-index", Models: []sdk.ModelInfo{{ID: "x"}}}, {Index: "ar-index", Models: nil}, {Index: "ar-index", Models: []sdk.ModelInfo{{ID: "x"}, {ID: "x"}}}, {Index: "ar-index", Models: []sdk.ModelInfo{{ID: "x"}}, UserAgent: "codex\r\nX-Evil: true"}, {Index: "ar-index", Models: []sdk.ModelInfo{{ID: "x"}}, MaxRetries: 100}}
	for i, c := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			m := setupManagementMock(t)
			raw, _ := json.Marshal(c)
			r := saveManagedCredential(managementRequest{ManagementRequest: sdk.ManagementRequest{Method: "PUT", Body: raw}})
			if r.StatusCode < 400 || m.saves != 0 {
				t.Fatal("invalid edit persisted")
			}
		})
	}
}
func TestPublicResourcesContainNoRuntimeData(t *testing.T) {
	old := hostCall
	hostCall = func(string, any, any) error { t.Fatal("public resource read credentials"); return nil }
	defer func() { hostCall = old }()
	for _, name := range []string{"ui", "status", "app.js", "style.css"} {
		raw, _ := json.Marshal(managementRequest{ManagementRequest: sdk.ManagementRequest{Method: "GET", Path: resourcePrefix + "/" + name}})
		result, e := managementHandle(raw)
		if e != nil || result.(sdk.ManagementResponse).StatusCode != 200 {
			t.Fatal(name, e)
		}
	}
}
