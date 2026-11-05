//go:build linux || darwin || freebsd

package main

import (
	"os"
	"syscall"
)

func lockRoutingFile(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX) }
func unlockRoutingFile(f *os.File)     { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
