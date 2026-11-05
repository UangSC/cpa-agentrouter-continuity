//go:build !linux && !windows

package main

import (
	"os"
	"strconv"
)

func processIdentity() string { return strconv.Itoa(os.Getpid()) }
