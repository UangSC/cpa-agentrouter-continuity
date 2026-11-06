package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/UangSC/cpa-agentrouter-continuity/internal/sdk"
)

func catalogMock(t *testing.T, status int, body string) (*managementMock, *string) {
	m := setupManagementMock(t)
	base := hostCall
	auth := ""
	hostCall = func(method string, value any, out any) error {
		raw, _ := json.Marshal(value)
		var q map[string]json.RawMessage
		_ = json.Unmarshal(raw, &q)
		switch method {
		case "host.http.do_stream":
			var target, verb string
			var headers http.Header
			_ = json.Unmarshal(q["url"], &target)
			_ = json.Unmarshal(q["method"], &verb)
			_ = json.Unmarshal(q["headers"], &headers)
			if target != "https://agentrouter.org/v1/models" || verb != "GET" {
				t.Fatal("wrong catalog target")
			}
			if headers.Get("User-Agent") != defaultUserAgent {
				t.Fatal("default UA missing")
			}
			auth = headers.Get("Authorization")
			*out.(*httpStart) = httpStart{Status: status, StreamID: "catalog"}
		case "host.http.stream_read":
			*out.(*httpChunk) = httpChunk{Payload: []byte(body), Done: true}
		case "host.http.stream_close":
		default:
			return base(method, value, out)
		}
		return nil
	}
	return m, &auth
}
func TestCatalogUsesStoredKeyAndNeverSaves(t *testing.T) {
	m, auth := catalogMock(t, 200, `{"data":[{"id":"b"},{"id":"a"},{"id":"a"},{"id":""}]}`)
	r := fetchModels(managementRequest{ManagementRequest: sdk.ManagementRequest{Body: []byte(`{"auth_index":"ar-index"}`)}})
	if r.StatusCode != 200 || *auth != "Bearer mock-secret" || m.saves != 0 {
		t.Fatal(r.StatusCode, *auth, m.saves)
	}
	if string(r.Body) != `{"count":2,"models":["a","b"]}` {
		t.Fatal(string(r.Body))
	}
}
func TestCatalogAcceptsUnsavedKeyWithoutPersisting(t *testing.T) {
	m, auth := catalogMock(t, 200, `{"data":[]}`)
	r := fetchModels(managementRequest{ManagementRequest: sdk.ManagementRequest{Body: []byte(`{"api_key":"mock-draft-only"}`)}})
	if r.StatusCode != 200 || *auth != "Bearer mock-draft-only" || m.saves != 0 {
		t.Fatal(r.StatusCode, *auth)
	}
}
func TestCatalogRejectsOtherProviderAndMissingKey(t *testing.T) {
	m, _ := catalogMock(t, 200, `{"data":[]}`)
	for _, body := range []string{`{}`, `{"auth_index":"other-index","api_key":"mock-key"}`} {
		r := fetchModels(managementRequest{ManagementRequest: sdk.ManagementRequest{Body: []byte(body)}})
		if r.StatusCode < 400 {
			t.Fatal("invalid catalog scope accepted")
		}
	}
	if m.gets != 0 || m.saves != 0 {
		t.Fatal("unrelated credential accessed")
	}
}
func TestCatalogErrorNeverEchoesUpstreamSecret(t *testing.T) {
	_, _ = catalogMock(t, 401, `{"error":{"message":"mock-secret rejected"}}`)
	r := fetchModels(managementRequest{ManagementRequest: sdk.ManagementRequest{Body: []byte(`{"auth_index":"ar-index"}`)}})
	if r.StatusCode != 502 || strings.Contains(string(r.Body), "mock-secret") {
		t.Fatal(string(r.Body))
	}
}
