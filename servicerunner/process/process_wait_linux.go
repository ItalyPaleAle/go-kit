//go:build linux

package process

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// waitLeaderExit blocks until cmd.Process has exited without reaping it, using its pidfd handle on Linux 5.4+
func waitLeaderExit(cmd *exec.Cmd) (reaped bool, waitErr error) {
	var pollErr error
	err := cmd.Process.WithHandle(func(pidfd uintptr) {
		epfd, epErr := syscall.EpollCreate1(syscall.EPOLL_CLOEXEC)
		if epErr != nil {
			pollErr = epErr
			return
		}
		defer syscall.Close(epfd) //nolint:errcheck

		ev := syscall.EpollEvent{
			Events: syscall.EPOLLIN,
			Fd:     int32(pidfd), //nolint:gosec // File descriptors fit in 32 bits on Linux
		}
		epErr = syscall.EpollCtl(epfd, syscall.EPOLL_CTL_ADD, int(pidfd), &ev)
		if epErr != nil {
			pollErr = epErr
			return
		}

		var events [1]syscall.EpollEvent
		for {
			_, wErr := syscall.EpollWait(epfd, events[:], -1)
			if errors.Is(wErr, syscall.EINTR) {
				continue
			}
			pollErr = wErr
			return
		}
	})
	if err != nil || pollErr != nil {
		return true, cmd.Wait()
	}

	return false, nil
}

// hasLiveGroupMembers scans /proc to check whether any non-zombie member other than the leader remains in process group pid
func hasLiveGroupMembers(pid int) bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}

	leaderStr := strconv.Itoa(pid)
	for _, entry := range entries {
		name := entry.Name()
		if name == "" || name[0] < '0' || name[0] > '9' || name == leaderStr {
			continue
		}

		data, readErr := os.ReadFile("/proc/" + name + "/stat") //nolint:gosec // Reads procfs numeric PID entry
		if readErr != nil {
			continue
		}

		// Comm is enclosed in parentheses and may contain spaces or parentheses, so locate the final ')'
		closeParen := bytes.LastIndexByte(data, ')')
		if closeParen < 0 || closeParen+2 >= len(data) {
			continue
		}

		fields := strings.Fields(string(data[closeParen+2:]))
		if len(fields) < 3 {
			continue
		}

		state := fields[0]
		pgrp := fields[2]
		if pgrp == leaderStr && state != "Z" && state != "X" && state != "x" {
			return true
		}
	}

	return false
}
