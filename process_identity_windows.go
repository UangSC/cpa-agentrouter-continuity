//go:build windows

package main

import (
	"os"
	"strconv"
	"syscall"
)

func processIdentity() string {
	h, err := syscall.GetCurrentProcess()
	if err == nil {
		var created, exited, kernel, user syscall.Filetime
		if syscall.GetProcessTimes(h, &created, &exited, &kernel, &user) == nil {
			return strconv.Itoa(os.Getpid()) + ":" + strconv.FormatUint(uint64(created.HighDateTime)<<32|uint64(created.LowDateTime), 10)
		}
	}
	return strconv.Itoa(os.Getpid())
}
