//go:build unix

package process

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"strconv"
	"syscall"
)

const defaultShutdownSignal = syscall.SIGTERM

type credential = syscall.Credential

// sysProcAttr returns the attributes used to start the process
// The process runs in its own process group, so signals from the terminal reach only the current process, and signals sent to stop the process also reach any process it spawned
func sysProcAttr(cred *credential, parentDeathSignal os.Signal) *syscall.SysProcAttr {
	attr := &syscall.SysProcAttr{
		Setpgid:    true,
		Credential: cred,
	}

	sig, ok := parentDeathSignal.(syscall.Signal)
	if ok {
		setParentDeathSignal(attr, sig)
	}

	return attr
}

func isSupportedSignal(sig os.Signal) bool {
	_, ok := sig.(syscall.Signal)
	return ok
}

// signalProcess sends sig to every process in the process group of proc
// The group is identified by the PID of proc, which can't be reused while the group has members or proc hasn't been reaped yet
func signalProcess(proc *os.Process, sig os.Signal) error {
	s, ok := sig.(syscall.Signal)
	if !ok {
		return fmt.Errorf("unsupported shutdown signal: %v", sig)
	}

	err := syscall.Kill(-proc.Pid, s)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	} else if err != nil {
		return err
	}

	// A stopped process (for example, after reading from the terminal) handles the signal only once it's resumed
	// Errors are ignored because the signal was already delivered
	_ = syscall.Kill(-proc.Pid, syscall.SIGCONT)
	return nil
}

// killProcess kills every process in the process group of proc
func killProcess(proc *os.Process) error {
	errs := make([]error, 0, 2)
	err := syscall.Kill(-proc.Pid, syscall.SIGKILL)
	if err != nil && !errors.Is(err, syscall.ESRCH) {
		errs = append(errs, err)
	}

	// Kill proc directly too, in case it moved itself to a different process group
	err = proc.Kill()
	if err != nil && !errors.Is(err, os.ErrProcessDone) {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

// resolveCredential returns the credential to run the process as, or nil to run it as the current user
func resolveCredential(userName string, groupName string) (*credential, error) {
	if userName == "" && groupName == "" {
		return nil, nil
	}

	// When only the group is set, the process keeps running as the current user
	cred := &credential{
		Uid: uint32(os.Getuid()), //nolint:gosec // UIDs are 32-bit values
		Gid: uint32(os.Getgid()), //nolint:gosec // GIDs are 32-bit values
	}
	hasGroup := false

	if userName != "" {
		u, uid, err := lookupUser(userName)
		if err != nil {
			return nil, err
		}
		cred.Uid = uid

		// A user in the database brings its primary and supplementary groups, like when logging in as that user
		if u != nil {
			cred.Gid, err = parseID(u.Gid)
			if err != nil {
				return nil, fmt.Errorf("invalid primary group ID for user %q: %w", userName, err)
			}
			hasGroup = true

			cred.Groups, err = supplementaryGroups(u)
			if err != nil {
				return nil, fmt.Errorf("failed to get the groups of user %q: %w", userName, err)
			}
		}
	}

	if groupName != "" {
		gid, err := lookupGroup(groupName)
		if err != nil {
			return nil, err
		}
		cred.Gid = gid
		hasGroup = true
	}

	// Falling back to the group of the current process could run the process with the root group
	if !hasGroup {
		return nil, fmt.Errorf("user %q is not in the user database, so its group must be set with WithGroup", userName)
	}

	return cred, nil
}

// lookupUser resolves a user name or numeric UID
// It returns a nil user for a numeric UID that isn't in the user database, as is common in containers
func lookupUser(name string) (*user.User, uint32, error) {
	u, err := user.Lookup(name)
	if err == nil {
		uid, pErr := parseID(u.Uid)
		if pErr != nil {
			return nil, 0, fmt.Errorf("invalid ID for user %q: %w", name, pErr)
		}
		return u, uid, nil
	}

	uid, pErr := parseID(name)
	if pErr != nil {
		return nil, 0, fmt.Errorf("failed to look up user %q: %w", name, err)
	}

	u, err = user.LookupId(name)
	_, unknown := errors.AsType[user.UnknownUserIdError](err)
	if unknown {
		return nil, uid, nil
	} else if err != nil {
		return nil, 0, fmt.Errorf("failed to look up user ID %q: %w", name, err)
	}
	return u, uid, nil
}

// lookupGroup resolves a group name or numeric GID
// A numeric GID that isn't in the group database is used as-is
func lookupGroup(name string) (uint32, error) {
	g, err := user.LookupGroup(name)
	if err == nil {
		gid, pErr := parseID(g.Gid)
		if pErr != nil {
			return 0, fmt.Errorf("invalid ID for group %q: %w", name, pErr)
		}
		return gid, nil
	}

	gid, pErr := parseID(name)
	if pErr != nil {
		return 0, fmt.Errorf("failed to look up group %q: %w", name, err)
	}

	_, err = user.LookupGroupId(name)
	_, unknown := errors.AsType[user.UnknownGroupIdError](err)
	if err != nil && !unknown {
		return 0, fmt.Errorf("failed to look up group ID %q: %w", name, err)
	}
	return gid, nil
}

func supplementaryGroups(u *user.User) ([]uint32, error) {
	ids, err := u.GroupIds()
	if err != nil {
		return nil, err
	}

	groups := make([]uint32, len(ids))
	for i, id := range ids {
		groups[i], err = parseID(id)
		if err != nil {
			return nil, fmt.Errorf("invalid group ID %q: %w", id, err)
		}
	}

	return groups, nil
}

func parseID(id string) (uint32, error) {
	v, err := strconv.ParseUint(id, 10, 32)
	if err != nil {
		return 0, err
	}

	return uint32(v), nil
}
