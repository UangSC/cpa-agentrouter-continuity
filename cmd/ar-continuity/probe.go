//go:build probe

package main

// The standalone executable exercises the same recovery engine directly against
// AR. It never contacts CPA, chooses another provider, or prints credentials.
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"
)

func main() {
	if len(os.Args) != 3 || os.Args[1] != "probe" {
		fmt.Fprintln(os.Stderr, "usage: cpa-agentrouter-probe probe /secure/probe.json")
		return
	}
	raw, e := os.ReadFile(os.Args[2])
	if e != nil {
		panic("cannot read probe settings")
	}
	var cfg struct {
		Credential credential      `json:"credential"`
		Payload    json.RawMessage `json:"payload"`
		Proxy      string          `json:"proxy"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		panic("invalid probe settings")
	}
	if e = validateCredential(cfg.Credential); e != nil {
		panic(e)
	}
	proxy, e := url.Parse(cfg.Proxy)
	if e != nil || proxy.Host != "127.0.0.1:7890" {
		panic("probe must use local mihomo port 7890")
	}
	transport := &http.Transport{Proxy: http.ProxyURL(proxy), TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 60 * time.Second}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var mu sync.Mutex
	var body io.ReadCloser
	starts := 0
	events := map[string]int{}
	var errorCodes []string
	statuses := []int{}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	hostCall = func(method string, v any, out any) error {
		raw, _ := json.Marshal(v)
		var q map[string]json.RawMessage
		_ = json.Unmarshal(raw, &q)
		switch method {
		case "host.http.do_stream":
			var target string
			var data []byte
			var h http.Header
			_ = json.Unmarshal(q["url"], &target)
			_ = json.Unmarshal(q["body"], &data)
			_ = json.Unmarshal(q["headers"], &h)
			if target != "https://agentrouter.org/v1/responses" {
				return fmt.Errorf("probe target denied")
			}
			req, e := http.NewRequestWithContext(ctx, "POST", target, bytes.NewReader(data))
			if e != nil {
				return e
			}
			req.Header = h
			starts++
			r, e := client.Do(req)
			if e != nil {
				return e
			}
			mu.Lock()
			body = r.Body
			mu.Unlock()
			statuses = append(statuses, r.StatusCode)
			*out.(*httpStart) = httpStart{Status: r.StatusCode, Headers: r.Header, StreamID: "direct-ar"}
		case "host.http.stream_read":
			mu.Lock()
			b := body
			mu.Unlock()
			buf := make([]byte, 4096)
			n, e := b.Read(buf)
			chunk := httpChunk{Payload: buf[:n], Done: e == io.EOF}
			*out.(*httpChunk) = chunk
			if e != nil && e != io.EOF {
				return e
			}
		case "host.http.stream_close":
			mu.Lock()
			if body != nil {
				_ = body.Close()
				body = nil
			}
			mu.Unlock()
		case "host.stream.emit":
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var p []byte
			_ = json.Unmarshal(q["payload"], &p)
			parser := frameReader{}
			frames, _ := parser.feed(p)
			mu.Lock()
			defer mu.Unlock()
			for _, f := range frames {
				d := eventData(f)
				kind := eventType(d)
				if kind == "" {
					continue
				}
				events[kind]++
				if kind == "response.failed" || kind == "error" {
					var v map[string]any
					_ = json.Unmarshal(d, &v)
					if r, ok := v["response"].(map[string]any); ok {
						v = r
					}
					if r, ok := v["error"].(map[string]any); ok {
						errorCodes = append(errorCodes, fmt.Sprint(r["code"]))
					}
				}
			}
		case "host.stream.close":
		default:
			return fmt.Errorf("unsupported probe callback")
		}
		return nil
	}
	started := time.Now()
	runStreaming(ctx, execution{StreamID: "probe-direct"}, cfg.Credential, cfg.Payload)
	mu.Lock()
	report := map[string]any{"direct_ar": true, "attempts": starts, "http_statuses": statuses, "events": events, "error_codes": errorCodes, "history_policy": "preserve-and-new-session", "retries": retries.Load(), "elapsed_seconds": time.Since(started).Seconds()}
	b, _ := json.Marshal(report)
	fmt.Println(string(b))
	mu.Unlock()
	if events["response.completed"] == 0 && events["response.incomplete"] == 0 {
		os.Exit(1)
	}
}
