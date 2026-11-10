package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The state contains only hashed identities and routing decisions, never payloads or keys.
type sessionOrigin struct {
	Provider string `json:"provider"`
	AuthID   string `json:"auth_id"`
	KeyHash  string `json:"key_hash,omitempty"`
	Foreign  bool   `json:"foreign"`
}
type admissionLease struct {
	Session   string    `json:"session"`
	KeyHash   string    `json:"key_hash"`
	AuthID    string    `json:"auth_id"`
	RequestID string    `json:"request_id"`
	Created   time.Time `json:"created"`
	Running   bool      `json:"running"`
	// Execution-only fields live in a separate file. Older loaded libraries
	// rewrite routing records without retaining fields they do not understand.
	Upstream            *bool `json:"upstream,omitempty"`
	RequestLimit        int   `json:"request_limit,omitempty"`
	SessionRequestLimit int   `json:"session_request_limit,omitempty"`
}
type routingLedger struct {
	Process     string                    `json:"process"`
	Origins     map[string]sessionOrigin  `json:"origins"`
	Leases      map[string]admissionLease `json:"leases"`
	LastKeyHash string                    `json:"last_key_hash,omitempty"`
}

var routingPath = filepath.Join("plugins", "cpa-agentrouter-continuity-sessions.json")
var routingStoreMu sync.Mutex
var executionStoreMu sync.Mutex

func withRoutingLedger(fn func(*routingLedger) error) error {
	routingStoreMu.Lock()
	defer routingStoreMu.Unlock()
	return withLedgerFile(routingPath, fn)
}

func withExecutionLedger(fn func(*routingLedger) error) error {
	executionStoreMu.Lock()
	defer executionStoreMu.Unlock()
	return withLedgerFile(routingPath+".executions", fn)
}

func withLedgerFile(path string, fn func(*routingLedger) error) error {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	lock, e := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer lock.Close()
	if e = lockRoutingFile(lock); e != nil {
		return e
	}
	defer unlockRoutingFile(lock)
	ledger := routingLedger{Origins: map[string]sessionOrigin{}, Leases: map[string]admissionLease{}}
	raw, e := os.ReadFile(path)
	if e == nil {
		if json.Unmarshal(raw, &ledger) != nil {
			return fmt.Errorf("invalid routing state")
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	if ledger.Origins == nil {
		ledger.Origins = map[string]sessionOrigin{}
	}
	if ledger.Leases == nil || ledger.Process != processIdentity() {
		ledger.Leases = map[string]admissionLease{}
	}
	ledger.Process = processIdentity()
	for id, lease := range ledger.Leases {
		if !lease.Running && time.Since(lease.Created) > time.Minute {
			delete(ledger.Leases, id)
		}
	}
	if e = fn(&ledger); e != nil {
		return e
	}
	data, e := json.Marshal(ledger)
	if e != nil {
		return e
	}
	tmp, e := os.CreateTemp(filepath.Dir(path), ".ar-session-*")
	if e != nil {
		return e
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, e = tmp.Write(data); e == nil {
		e = tmp.Sync()
	}
	closeErr := tmp.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, path)
}
