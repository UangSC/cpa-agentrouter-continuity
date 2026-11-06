//go:build linux

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestLinuxRoutingLockAcrossProcesses(t *testing.T) {
	if path := os.Getenv("AR_TEST_LOCK_PATH"); path != "" {
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if os.Getenv("AR_TEST_LOCK_BLOCKED") == "1" {
			if !errors.Is(err, syscall.EWOULDBLOCK) {
				t.Fatalf("another process acquired the held lock: %v", err)
			}
		} else if err != nil {
			t.Fatalf("released lock remained unavailable: %v", err)
		}
		return
	}
	path := filepath.Join(t.TempDir(), "routing.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err = lockRoutingFile(f); err != nil {
		t.Fatal(err)
	}
	defer unlockRoutingFile(f)
	check := func(blocked string) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestLinuxRoutingLockAcrossProcesses$")
		cmd.Env = append(os.Environ(), "AR_TEST_LOCK_PATH="+path, "AR_TEST_LOCK_BLOCKED="+blocked)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("child lock check: %v\n%s", err, out)
		}
	}
	check("1")
	unlockRoutingFile(f)
	check("0")
}

func TestLinuxRoutingLedgerPermissionsAndProcessIdentity(t *testing.T) {
	routeFixture(t)
	if err := withRoutingLedger(func(*routingLedger) error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{routingPath, routingPath + ".lock"} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("ledger permissions %s: %o", path, info.Mode().Perm())
		}
	}
	identity := processIdentity()
	if !strings.Contains(identity, ":") || identity != processIdentity() {
		t.Fatalf("missing or unstable Linux process start time: %s", identity)
	}
}
