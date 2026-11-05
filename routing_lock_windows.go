//go:build windows

package main

import (
	"os"
	"syscall"
	"unsafe"
)

const lockfileExclusive = 0x00000002

var (
	kernel32       = syscall.NewLazyDLL("kernel32.dll")
	lockFileExProc = kernel32.NewProc("LockFileEx")
	unlockFileProc = kernel32.NewProc("UnlockFileEx")
)

// LockFileEx provides the same blocking, cross-process exclusive lock used by
// flock on Unix. The lock is released automatically when the handle closes.
func lockRoutingFile(f *os.File) error {
	var overlapped syscall.Overlapped
	r, _, err := lockFileExProc.Call(uintptr(f.Fd()), lockfileExclusive, 0, 0xffffffff, 0xffffffff, uintptr(unsafe.Pointer(&overlapped)))
	if r == 0 {
		return err
	}
	return nil
}

func unlockRoutingFile(f *os.File) {
	var overlapped syscall.Overlapped
	_, _, _ = unlockFileProc.Call(uintptr(f.Fd()), 0, 0xffffffff, 0xffffffff, uintptr(unsafe.Pointer(&overlapped)))
}
