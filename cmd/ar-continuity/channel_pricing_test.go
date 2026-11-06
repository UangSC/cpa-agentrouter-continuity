package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestChannelPricingConversionAndCacheBoundary(t *testing.T) {
	prices, skipped, err := parseChannelPricing([]byte(`{"success":true,"group_ratio":{"default":1.5},"data":[
 {"model_name":"standard","quota_type":0,"model_ratio":2,"completion_ratio":5,"enable_groups":["default"]},
 {"model_name":"cached","quota_type":0,"model_ratio":5,"completion_ratio":5,"cache_ratio":0.1,"enable_groups":["default"]},
 {"model_name":"free","quota_type":0,"model_ratio":0,"completion_ratio":5,"cache_ratio":0,"enable_groups":["default"]},
 {"model_name":"request-priced","quota_type":1,"model_price":2,"enable_groups":["default"]},
 {"model_name":"tiered","quota_type":0,"model_ratio":1,"completion_ratio":2,"pricing_expression":"tiered","enable_groups":["default"]},
 {"model_name":"vip-only","quota_type":0,"model_ratio":1,"completion_ratio":2,"enable_groups":["svip"]},
 {"model_name":"missing","quota_type":0,"completion_ratio":2,"enable_groups":["default"]}]}`))
	if err != nil || skipped != 4 || len(prices) != 3 {
		t.Fatalf("unexpected parse: %v %d %v", prices, skipped, err)
	}
	if p := prices["standard"]; p.Input != 6 || p.Output != 30 || p.Cached != 6 || !p.CacheAssumed {
		t.Fatal(p)
	}
	if p := prices["cached"]; p.Input != 15 || p.Output != 75 || p.Cached != 1.5 || p.CacheAssumed {
		t.Fatal(p)
	}
	if p := prices["free"]; p.Input != 0 || p.Cached != 0 || p.CacheAssumed {
		t.Fatal(p)
	}
}

func TestChannelPricingRejectsInvalidOrUnsupportedCatalog(t *testing.T) {
	for _, raw := range []string{`<html>error</html>`, `{"success":false,"data":[]}`, `{"success":true,"data":[]}`, `{"success":true,"group_ratio":{"default":null},"data":[]}`, `{"success":true,"group_ratio":{"default":1},"data":[{"model_name":"a","quota_type":0,"model_ratio":-1,"completion_ratio":1,"enable_groups":["default"]}]}`} {
		if _, _, err := parseChannelPricing([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestChannelPricingUsesFixedPublicEndpointWithoutCredentials(t *testing.T) {
	old := hostCall
	t.Cleanup(func() { hostCall = old })
	closed := 0
	hostCall = func(method string, value any, out any) error {
		raw, _ := json.Marshal(value)
		switch method {
		case "host.http.do_stream":
			var q struct {
				URL     string      `json:"url"`
				Method  string      `json:"method"`
				Headers http.Header `json:"headers"`
			}
			_ = json.Unmarshal(raw, &q)
			if q.URL != channelPricingURL || q.Method != "GET" || q.Headers.Get("Authorization") != "" || q.Headers.Get("Cookie") != "" {
				t.Fatal("pricing sent private data or wrong target")
			}
			*out.(*httpStart) = httpStart{Status: 200, StreamID: "prices"}
		case "host.http.stream_read":
			*out.(*httpChunk) = httpChunk{Payload: []byte(`{"success":true,"group_ratio":{"default":1},"data":[{"model_name":"m","quota_type":0,"model_ratio":2,"completion_ratio":5,"enable_groups":["default"]}]}`), Done: true}
		case "host.http.stream_close":
			closed++
		default:
			t.Fatal("unexpected host access: " + method)
		}
		return nil
	}
	r := fetchChannelPricing(managementRequest{})
	if r.StatusCode != 200 || closed != 1 || !strings.Contains(string(r.Body), `"cache_assumed":true`) {
		t.Fatal(r.StatusCode, string(r.Body), closed)
	}
	hostCall = func(string, any, any) error { return errors.New("private upstream diagnostic") }
	r = fetchChannelPricing(managementRequest{})
	if r.StatusCode != 502 || strings.Contains(string(r.Body), "private upstream") {
		t.Fatal(string(r.Body))
	}
}
