package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/UangSC/cpa-agentrouter-continuity/internal/sdk"
)

func TestDefaultHeadersWithoutCredentialHeaders(t *testing.T) {
	c := testCredential()
	c.Headers = nil
	if err := validateCredential(c); err != nil {
		t.Fatal(err)
	}
	got := upstreamHeaders(c, execution{})
	if got.Get("User-Agent") != defaultUserAgent || got.Get("Originator") != defaultOriginator {
		t.Fatal("defaults missing")
	}
	if got.Get("Authorization") != "Bearer "+c.APIKey {
		t.Fatal("runtime key missing")
	}
}

func TestDefaultHeadersWithEmptyValuesAndOverrides(t *testing.T) {
	c := testCredential()
	c.Headers = map[string]string{"user-agent": " ", "ORIGINATOR": ""}
	got := upstreamHeaders(c, execution{})
	if got.Get("User-Agent") != defaultUserAgent || got.Get("Originator") != defaultOriginator {
		t.Fatal("blank fields must use defaults")
	}
	c.Headers = map[string]string{"user-agent": "Zcode/custom", "originator": "custom", "X-Client": "custom"}
	if err := validateCredential(c); err != nil {
		t.Fatal(err)
	}
	got = upstreamHeaders(c, execution{})
	if got.Get("User-Agent") != "Zcode/custom" || got.Get("Originator") != "custom" || got.Get("X-Client") != "custom" {
		t.Fatal("custom overrides lost")
	}
}

func TestRuntimeKeyIsRequiredAndCannotBeOverridden(t *testing.T) {
	c := testCredential()
	c.APIKey = ""
	c.Headers = nil
	if validateCredential(c) == nil {
		t.Fatal("credential-free use accepted")
	}
	c.APIKey = "mock-runtime-only"
	c.Headers = map[string]string{"Authorization": "Bearer mock-wrong", "Content-Length": "123", "Proxy-Authorization": "mock-proxy", "Accept-Encoding": "gzip"}
	got := upstreamHeaders(c, execution{})
	if got.Get("Authorization") != "Bearer mock-runtime-only" || got.Get("Content-Length") != "" || got.Get("Proxy-Authorization") != "" || got.Get("Accept-Encoding") != "identity" {
		t.Fatal("unsafe custom headers accepted")
	}
}

func TestMetadataDoesNotAdvertiseAnotherRepository(t *testing.T) {
	raw, e := json.Marshal(registration())
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(raw), "https://github.com/UangSC/cpa-agentrouter-continuity") {
		t.Fatal("project repository metadata missing")
	}
}

func TestErrorRedactsRuntimeKey(t *testing.T) {
	key := "mock-runtime-only"
	for _, status := range []int{400, 401, 500} {
		e := safeError(status, []byte(`{"error":{"message":"rejected mock-runtime-only","code":"error"}}`), key)
		if strings.Contains(e.Message, key) {
			t.Fatal("runtime credential in error")
		}
	}
}

func TestPrefixIsOptionalAndExclusive(t *testing.T) {
	c := testCredential()
	c.Models = []sdk.ModelInfo{{ID: "test-model", Name: "upstream-model"}}
	if m := publicModels(c); len(m) != 1 || m[0].ID != "test-model" {
		t.Fatal(m)
	}
	c.Prefix = "choice"
	if m := publicModels(c); len(m) != 1 || m[0].ID != "choice/test-model" {
		t.Fatal(m)
	}
	if _, e := modelForCredential(c, "test-model"); e == nil {
		t.Fatal("prefix unexpectedly published bare model")
	}
	if m, e := modelForCredential(c, "choice/test-model"); e != nil || m != "upstream-model" {
		t.Fatal(m, e)
	}
}

func TestDefaultHeadersCredentialParsing(t *testing.T) {
	c := testCredential()
	c.Headers = nil
	raw, _ := json.Marshal(c)
	request, _ := json.Marshal(sdk.AuthParseRequest{RawJSON: raw})
	v, e := handle("auth.parse", request)
	if e != nil || !v.(sdk.AuthParseResponse).Handled {
		t.Fatal(v, e)
	}
	if got := upstreamHeaders(c, execution{ExecutorRequest: sdk.ExecutorRequest{Headers: http.Header{"Authorization": {"Bearer downstream-key"}, "User-Agent": {"unrelated-client"}}}}); got.Get("User-Agent") != defaultUserAgent || got.Get("Authorization") != "Bearer "+c.APIKey {
		t.Fatal("downstream headers replaced AR identity")
	}
}
