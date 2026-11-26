//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package process

import (
	"errors"
	"os/exec"
	"syscall"
)

// waitLeaderExit blocks until cmd.Process has exited without reaping it, keeping its PID and process group reserved until cmd.Wait runs
func waitLeaderExit(cmd *exec.Cmd) (reaped bool, waitErr error) {
	kq, err := syscall.Kqueue()
	if err != nil {
		return true, cmd.Wait()
	}
	defer syscall.Close(kq) //nolint:errcheck

	var change syscall.Kevent_t
	syscall.SetKevent(&change, cmd.Process.Pid, syscall.EVFILT_PROC, syscall.EV_ADD|syscall.EV_ONESHOT)
	change.Fflags = syscall.NOTE_EXIT

	var (
		events      [1]syscall.Kevent_t
		zeroTimeout syscall.Timespec
	)

	// Register the filter without blocking first, which also returns immediately if the process is already a zombie
	for {
		n, regErr := syscall.Kevent(kq, []syscall.Kevent_t{change}, events[:], &zeroTimeout)
		if errors.Is(regErr, syscall.EINTR) {
			continue
		}
		if errors.Is(regErr, syscall.ESRCH) {
			return false, nil
		}
		if regErr != nil {
			return true, cmd.Wait()
		}
		if n > 0 {
			if (events[0].Flags & syscall.EV_ERROR) != 0 {
				if events[0].Data == int64(syscall.ESRCH) {
					return false, nil
				}
				if events[0].Data != 0 {
					return true, cmd.Wait()
				}
			}
			return false, nil
		}
		break
	}

	// Block until NOTE_EXIT fires
	for {
		_, waitKeventErr := syscall.Kevent(kq, nil, events[:], nil)
		if errors.Is(waitKeventErr, syscall.EINTR) {
			continue
		}
		if waitKeventErr != nil && !errors.Is(waitKeventErr, syscall.ESRCH) {
			return true, cmd.Wait()
		}
		return false, nil
	}
}

// hasLiveGroupMembers reports whether non-zombie members remain in the process group while the leader is an unreaped zombie
func hasLiveGroupMembers(pid int) bool {
	err := syscall.Kill(-pid, 0)
	return err == nil
}
