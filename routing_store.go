package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
}
type routingLedger struct {
	Process string                    `json:"process"`
	Origins map[string]sessionOrigin  `json:"origins"`
	Leases  map[string]admissionLease `json:"leases"`
}

var routingPath = filepath.Join("plugins", "cpa-agentrouter-continuity-sessions.json")
var routingStoreMu sync.Mutex

func processIdentity() string {
	raw, e := os.ReadFile("/proc/self/stat")
	if e == nil {
		end := strings.LastIndex(string(raw), ")")
		fields := strings.Fields(string(raw)[end+1:])
		if len(fields) > 19 {
			return strconv.Itoa(os.Getpid()) + ":" + fields[19]
		}
	}
	return strconv.Itoa(os.Getpid())
}
func withRoutingLedger(fn func(*routingLedger) error) error {
	routingStoreMu.Lock()
	defer routingStoreMu.Unlock()
	if e := os.MkdirAll(filepath.Dir(routingPath), 0700); e != nil {
		return e
	}
	lock, e := os.OpenFile(routingPath+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer lock.Close()
	if e = lockRoutingFile(lock); e != nil {
		return e
	}
	defer unlockRoutingFile(lock)
	ledger := routingLedger{Origins: map[string]sessionOrigin{}, Leases: map[string]admissionLease{}}
	raw, e := os.ReadFile(routingPath)
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
	tmp, e := os.CreateTemp(filepath.Dir(routingPath), ".ar-session-*")
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
	return os.Rename(name, routingPath)
}
