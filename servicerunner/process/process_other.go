//go:build !unix

package process

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
)

var defaultShutdownSignal = os.Interrupt

// The platform can't signal the process when the application dies
const parentDeathSignalSupported = false

type credential struct{}

func sysProcAttr(_ *credential, _ os.Signal) *syscall.SysProcAttr {
	return nil
}

func isSupportedSignal(_ os.Signal) bool {
	return true
}

// signalProcess sends sig to the process
// On Windows, processes can't be sent signals other than os.Kill, so the caller kills the process right away
func signalProcess(proc *os.Process, sig os.Signal) error {
	if runtime.GOOS == "windows" {
		return errors.ErrUnsupported
	}

	return proc.Signal(sig)
}

func killProcess(proc *os.Process) error {
	err := proc.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}

	return err
}

func resolveCredential(userName string, groupName string) (*credential, error) {
	if userName != "" || groupName != "" {
		return nil, fmt.Errorf("running the process as a different user or group is not supported on %s", runtime.GOOS)
	}

	return nil, nil
}
