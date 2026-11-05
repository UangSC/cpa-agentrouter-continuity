//go:build !linux && !darwin && !freebsd

package main

import (
	"fmt"
	"os"
)

func lockRoutingFile(*os.File) error {
	return fmt.Errorf("cross-instance routing locks unsupported on this platform")
}
func unlockRoutingFile(*os.File) {}
