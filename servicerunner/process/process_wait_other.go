//go:build !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package process

import (
	"os/exec"
)

func waitLeaderExit(cmd *exec.Cmd) (reaped bool, waitErr error) {
	return true, cmd.Wait()
}

func hasLiveGroupMembers(_ int) bool {
	return false
}
