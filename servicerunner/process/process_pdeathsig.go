//go:build linux || freebsd

package process

import (
	"syscall"
)

const parentDeathSignalSupported = true

// setParentDeathSignal makes the kernel send sig to the process when the application dies
func setParentDeathSignal(attr *syscall.SysProcAttr, sig syscall.Signal) {
	attr.Pdeathsig = sig
}
