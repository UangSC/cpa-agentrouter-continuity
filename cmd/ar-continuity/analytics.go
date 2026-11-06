package main

import (
	"encoding/json"
	"sort"
	"time"
)

// Only numeric usage is retained. No response text, tool arguments or credentials.
type tokenUsage struct {
	Input  int64 `json:"input_tokens"`
	Output int64 `json:"output_tokens"`
	Cached int64 `json:"cached_tokens"`
}

type analyticsBucket struct {
	StartedAt          time.Time  `json:"started_at"`
	Model              string     `json:"model"`
	CredentialID       string     `json:"credential_id"`
	Requests           int64      `json:"requests"`
	Completed          int64      `json:"completed"`
	Failed             int64      `json:"failed"`
	Canceled           int64      `json:"canceled"`
	Incomplete         int64      `json:"incomplete"`
	Recovered          int64      `json:"recovered"`
	Retries            int64      `json:"retries"`
	UsageCount         int64      `json:"usage_count"`
	FirstOutputCount   int64      `json:"first_output_count"`
	FirstOutputTotalMS int64      `json:"first_output_total_ms"`
	Tokens             tokenUsage `json:"tokens"`
}

type analyticsKey struct {
	Time         int64
	Model        string
	CredentialID string
}

var analyticsStarted = time.Now().UTC()
var analyticsBuckets = map[analyticsKey]*analyticsBucket{}
var analyticsNextPrune time.Time

func pruneAnalytics(now time.Time) {
	if now.Before(analyticsNextPrune) {
		return
	}
	cutoff := now.Add(-24 * time.Hour).Truncate(5 * time.Minute)
	for key, b := range analyticsBuckets {
		if b.StartedAt.Before(cutoff) {
			delete(analyticsBuckets, key)
		}
	}
	analyticsNextPrune = now.Add(5 * time.Minute)
}

// All aggregate access shares monitor.mu with the request lifecycle.
func analyticsBucketFor(r *requestRecord) *analyticsBucket {
	pruneAnalytics(time.Now().UTC())
	t := r.StartedAt.Truncate(5 * time.Minute)
	key := analyticsKey{Time: t.Unix(), Model: r.Model, CredentialID: r.CredentialID}
	b := analyticsBuckets[key]
	if b == nil {
		b = &analyticsBucket{StartedAt: t, Model: r.Model, CredentialID: r.CredentialID}
		analyticsBuckets[key] = b
	}
	return b
}

func finishAnalytics(r *requestRecord) {
	b := analyticsBucketFor(r)
	switch r.Status {
	case "completed":
		b.Completed++
	case "failed":
		b.Failed++
	case "canceled":
		b.Canceled++
	case "incomplete":
		b.Incomplete++
	}
	if r.Attempts > 1 {
		b.Retries += int64(r.Attempts - 1)
		if r.Status == "completed" {
			b.Recovered++
		}
	}
	if r.FirstOutputMS >= 0 {
		b.FirstOutputCount++
		b.FirstOutputTotalMS += r.FirstOutputMS
	}
	if r.Usage != nil {
		b.UsageCount++
		b.Tokens.Input += r.Usage.Input
		b.Tokens.Output += r.Usage.Output
		b.Tokens.Cached += r.Usage.Cached
	}
}

func analyticsSnapshot(now time.Time) map[string]any {
	cutoff := now.Add(-24 * time.Hour).Truncate(5 * time.Minute)
	buckets := []analyticsBucket{}
	for key, b := range analyticsBuckets {
		if b.StartedAt.Before(cutoff) {
			delete(analyticsBuckets, key)
			continue
		}
		buckets = append(buckets, *b)
	}
	sort.Slice(buckets, func(i, j int) bool {
		if buckets[i].StartedAt.Equal(buckets[j].StartedAt) {
			if buckets[i].Model == buckets[j].Model {
				return buckets[i].CredentialID < buckets[j].CredentialID
			}
			return buckets[i].Model < buckets[j].Model
		}
		return buckets[i].StartedAt.Before(buckets[j].StartedAt)
	})
	return map[string]any{"started_at": analyticsStarted, "now": now, "bucket_minutes": 5, "credential_scoped": true, "buckets": buckets}
}

func recordUsage(r *requestRecord, raw []byte) {
	type usage struct {
		Input   *int64 `json:"input_tokens"`
		Output  *int64 `json:"output_tokens"`
		Details struct {
			Cached int64 `json:"cached_tokens"`
		} `json:"input_tokens_details"`
	}
	var envelope struct {
		Usage    *usage `json:"usage"`
		Response *struct {
			Usage *usage `json:"usage"`
		} `json:"response"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return
	}
	u := envelope.Usage
	if envelope.Response != nil {
		u = envelope.Response.Usage
	}
	if u == nil || u.Input == nil || u.Output == nil || *u.Input < 0 || *u.Output < 0 || u.Details.Cached < 0 || u.Details.Cached > *u.Input {
		return
	}
	monitor.mu.Lock()
	r.Usage = &tokenUsage{Input: *u.Input, Output: *u.Output, Cached: u.Details.Cached}
	monitor.mu.Unlock()
}
