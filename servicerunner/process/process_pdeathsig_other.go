//go:build unix && !linux && !freebsd

package process

import (
	"syscall"
)

// The platform can't signal the process when the application dies, so WithParentDeathSignal is ignored
const parentDeathSignalSupported = false

func setParentDeathSignal(_ *syscall.SysProcAttr, _ syscall.Signal) {
	// Empty
}
