package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func isolateAnalytics(t *testing.T) {
	t.Helper()
	oldBuckets, oldNext := analyticsBuckets, analyticsNextPrune
	oldActive, oldRecent := monitor.Active, monitor.Recent
	analyticsBuckets = map[analyticsKey]*analyticsBucket{}
	analyticsNextPrune = time.Time{}
	monitor.Active = map[string]*requestRecord{}
	monitor.Recent = nil
	t.Cleanup(func() {
		analyticsBuckets, analyticsNextPrune = oldBuckets, oldNext
		monitor.Active, monitor.Recent = oldActive, oldRecent
	})
}

func TestAnalyticsSeparatesCredentialsInSameModelAndBucket(t *testing.T) {
	isolateAnalytics(t)
	at := time.Now().UTC().Truncate(5 * time.Minute)
	for _, entry := range []struct{ id, status string }{{"key-a", "completed"}, {"key-a", "completed"}, {"key-b", "failed"}} {
		r := &requestRecord{CredentialID: entry.id, Model: "same-model", StartedAt: at, FirstOutputMS: -1, Attempts: 1, Status: entry.status}
		analyticsBucketFor(r).Requests++
		finishAnalytics(r)
	}
	snapshot := analyticsSnapshot(time.Now().UTC())
	if snapshot["credential_scoped"] != true {
		t.Fatal("scope flag missing")
	}
	buckets := snapshot["buckets"].([]analyticsBucket)
	if len(buckets) != 2 || buckets[0].CredentialID != "key-a" || buckets[0].Completed != 2 || buckets[0].Failed != 0 || buckets[1].CredentialID != "key-b" || buckets[1].Failed != 1 {
		t.Fatal(buckets)
	}
}

func TestAnalyticsRetainsTotalsBeyondLogLimit(t *testing.T) {
	isolateAnalytics(t)
	for i := 0; i < 240; i++ {
		q := execution{}
		q.Model = "model-a"
		_, r := beginRecord(context.Background(), q, credential{ID: "private-id"})
		recordAttempt(r)
		if i%3 == 0 {
			recordAttempt(r)
		}
		recordUsage(r, []byte(`{"usage":{"input_tokens":100,"output_tokens":20,"input_tokens_details":{"cached_tokens":80}},"output":[{"text":"DO-NOT-RETAIN"}]}`))
		finishRecord(r, "completed", "")
	}
	if len(monitor.Recent) != 200 {
		t.Fatal("request log retention changed")
	}
	var requests, completed, retries, usage, input, cached int64
	for _, b := range analyticsBuckets {
		requests += b.Requests
		completed += b.Completed
		retries += b.Retries
		usage += b.UsageCount
		input += b.Tokens.Input
		cached += b.Tokens.Cached
	}
	if requests != 240 || completed != 240 || retries != 80 || usage != 240 || input != 24000 || cached != 19200 {
		t.Fatalf("unexpected aggregate %d %d %d %d %d %d", requests, completed, retries, usage, input, cached)
	}
	raw, _ := json.Marshal(monitorSnapshot())
	if strings.Contains(string(raw), "DO-NOT-RETAIN") {
		t.Fatal("response text leaked")
	}
	if strings.Contains(string(raw), `"Usage"`) {
		t.Fatal("usage leaked into existing log fields")
	}
}

func TestAnalyticsUsageZeroMissingAndInvalid(t *testing.T) {
	isolateAnalytics(t)
	for _, raw := range []string{`{}`, `{"usage":null}`, `{"usage":{"input_tokens":5}}`, `{"usage":{"input_tokens":-1,"output_tokens":1}}`, `{"usage":{"input_tokens":5,"output_tokens":1,"input_tokens_details":{"cached_tokens":6}}}`} {
		r := &requestRecord{}
		recordUsage(r, []byte(raw))
		if r.Usage != nil {
			t.Fatalf("invalid usage accepted: %s", raw)
		}
	}
	r := &requestRecord{}
	recordUsage(r, []byte(`{"response":{"usage":{"input_tokens":0,"output_tokens":0}}}`))
	if r.Usage == nil || r.Usage.Input != 0 || r.Usage.Output != 0 {
		t.Fatal("explicit zero usage must be known")
	}
	recordUsage(r, []byte(`{"response":{"usage":{"input_tokens":100,"output_tokens":30,"input_tokens_details":{"cached_tokens":70},"output_tokens_details":{"reasoning_tokens":20}}}}`))
	if r.Usage.Output != 30 || r.Usage.Cached != 70 {
		t.Fatal("reasoning or cache counted twice")
	}
}

func TestAnalyticsPrunesWithoutDashboardReader(t *testing.T) {
	isolateAnalytics(t)
	now := time.Now().UTC()
	old := now.Add(-26 * time.Hour)
	analyticsBuckets[analyticsKey{Time: old.Unix(), Model: "old"}] = &analyticsBucket{StartedAt: old, Model: "old", Requests: 99}
	q := execution{}
	q.Model = "new"
	_, r := beginRecord(context.Background(), q, credential{})
	if len(analyticsBuckets) != 1 {
		t.Fatal("expired buckets survive new requests")
	}
	recordAttempt(r)
	finishRecord(r, "failed", "test")
	b := analyticsBucketFor(r)
	if b.Requests != 1 || b.Failed != 1 || b.UsageCount != 0 {
		t.Fatal("failed request accounting incorrect")
	}
}
