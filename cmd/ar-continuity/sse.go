package main

import (
	"bytes"
	"encoding/json"
	"fmt"
)

type frameReader struct{ pending []byte }

func (f *frameReader) feed(p []byte) ([][]byte, error) {
	f.pending = append(f.pending, p...)
	if len(f.pending) > maxBuffer {
		return nil, fmt.Errorf("SSE frame too large")
	}
	var frames [][]byte
	for {
		i := bytes.Index(f.pending, []byte("\n\n"))
		j := bytes.Index(f.pending, []byte("\r\n\r\n"))
		n := 2
		if j >= 0 && (i < 0 || j < i) {
			i = j
			n = 4
		}
		if i < 0 {
			break
		}
		frames = append(frames, bytes.Clone(f.pending[:i+n]))
		f.pending = f.pending[i+n:]
	}
	return frames, nil
}

func eventData(frame []byte) []byte {
	var rows [][]byte
	for _, l := range bytes.Split(frame, []byte("\n")) {
		l = bytes.TrimSuffix(l, []byte("\r"))
		if bytes.HasPrefix(l, []byte("data:")) {
			rows = append(rows, bytes.TrimPrefix(l[5:], []byte(" ")))
		}
	}
	return bytes.Join(rows, []byte("\n"))
}

func eventType(data []byte) string {
	var v struct{ Type string }
	_ = json.Unmarshal(data, &v)
	return v.Type
}

func meaningful(kind string, data []byte) bool {
	switch kind {
	case "response.created", "response.in_progress", "response.queued":
		return false
	}
	// Unknown events may contain content or tool effects: commit conservatively.
	return len(data) > 0 && string(data) != "[DONE]"
}
