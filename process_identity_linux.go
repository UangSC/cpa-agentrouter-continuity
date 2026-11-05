//go:build linux

package main

import (
	"os"
	"strconv"
	"strings"
)

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
