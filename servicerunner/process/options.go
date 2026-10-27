package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"
)

// RestartPolicy controls whether the process is started again after it exits on its own
type RestartPolicy string

const (
	// RestartAlways restarts the process whenever it exits, regardless of its exit status
	// This is the default policy
	RestartAlways RestartPolicy = "always"
	// RestartOnFailure restarts the process only when it exits with a non-zero status or is terminated by a signal
	// When the process exits successfully, the service returns
	RestartOnFailure RestartPolicy = "on-failure"
	// RestartNever never restarts the process
	// When the process exits, the service returns
	RestartNever RestartPolicy = "never"
)

// ErrRestartLimitReached is wrapped by the error the service returns when it gives up restarting the process
var ErrRestartLimitReached = errors.New("restart limit reached")

// Option configures the service returned by NewService
type Option func(*options)

type options struct {
	command              string
	args                 []string
	shutdownGraceTimeout time.Duration
	shutdownSignal       os.Signal
	restartPolicy        RestartPolicy
	restartDelayInitial  time.Duration
	restartDelayMax      time.Duration
	restartLimit         int
	restartResetAfter    time.Duration
	user                 string
	group                string
	workingDirectory     string
	env                  []envVar
	parentEnv            bool
	stdin                io.Reader
	stdout               io.Writer
	stderr               io.Writer
	onStart              func(ctx context.Context, pid int)
	logger               *slog.Logger
}

type envVar struct {
	key   string
	value string
}

func defaultOptions() options {
	return options{
		shutdownGraceTimeout: 5 * time.Second,
		shutdownSignal:       defaultShutdownSignal,
		restartPolicy:        RestartAlways,
		restartDelayInitial:  time.Second,
		restartDelayMax:      30 * time.Second,
		restartLimit:         5,
		// Once a run of the process lasts this long, it is considered healthy: the restart delay goes back to its initial value and the restart count is reset
		restartResetAfter: 10 * time.Second,
	}
}

func (o *options) validate() error {
	if o.command == "" {
		return errors.New("command is required: set it with WithCommand")
	}
	if o.shutdownGraceTimeout <= 0 {
		return errors.New("shutdown grace timeout must be greater than zero")
	}
	if o.shutdownSignal == nil {
		return errors.New("shutdown signal must not be nil")
	}

	err := validateSignal(o.shutdownSignal)
	if err != nil {
		return err
	}

	switch o.restartPolicy {
	case RestartAlways, RestartOnFailure, RestartNever:
		// All good
	default:
		return fmt.Errorf("invalid restart policy: %q", o.restartPolicy)
	}

	if o.restartDelayInitial <= 0 {
		return errors.New("initial restart delay must be greater than zero")
	}
	if o.restartDelayMax < o.restartDelayInitial {
		return errors.New("maximum restart delay must not be less than the initial restart delay")
	}
	if o.restartLimit < 0 {
		return errors.New("restart limit must not be negative")
	}

	for _, v := range o.env {
		if v.key == "" || strings.ContainsAny(v.key, "=\x00") {
			return fmt.Errorf("invalid environment variable name: %q", v.key)
		}

		if strings.ContainsRune(v.value, 0) {
			return fmt.Errorf("value of environment variable %q contains a NUL character", v.key)
		}
	}

	return nil
}

// WithCommand sets the command to run and its arguments
// This option is required
// If cmd doesn't contain a path separator, it is looked up in the PATH of the current process
func WithCommand(cmd string, args ...string) Option {
	return func(o *options) {
		o.command = cmd
		o.args = slices.Clone(args)
	}
}

// WithShutdownGraceTimeout sets how long to wait for the process to exit after it's sent the shutdown signal
// When the timeout expires, the process is killed forcefully
// It also bounds how long to wait for the output of the process to be fully copied after the process exits
// Defaults to 5 seconds
func WithShutdownGraceTimeout(timeout time.Duration) Option {
	return func(o *options) {
		o.shutdownGraceTimeout = timeout
	}
}

// WithShutdownSignal sets the signal sent to the process to ask it to shut down
// Defaults to SIGTERM
// Ignored on Windows, where processes can't be sent signals and are killed right away
func WithShutdownSignal(sig os.Signal) Option {
	return func(o *options) {
		o.shutdownSignal = sig
	}
}

// WithRestart sets the restart policy, which controls whether the process is started again after it exits on its own
// Defaults to RestartAlways
// The process is never restarted after the service's context is canceled, or when it can't be started at all (e.g. because the command doesn't exist)
func WithRestart(policy RestartPolicy) Option {
	return func(o *options) {
		o.restartPolicy = policy
	}
}

// WithRestartBackoff sets how long to wait before restarting the process
// The first restart waits for the initial delay, which doubles at each consecutive restart up to the maximum delay, so a process that keeps crashing doesn't spin in a tight loop
// The delay goes back to its initial value once a run of the process lasts at least 10 seconds
// Defaults to an initial delay of 1 second and a maximum delay of 30 seconds
func WithRestartBackoff(initialDelay time.Duration, maxDelay time.Duration) Option {
	return func(o *options) {
		o.restartDelayInitial = initialDelay
		o.restartDelayMax = maxDelay
	}
}

// WithRestartLimit sets how many times in a row the process can be restarted
// When the process exits again after that, the service gives up and returns an error wrapping ErrRestartLimitReached
// The count is reset once a run of the process lasts at least 10 seconds
// Set it to 0 to restart without limit
// Defaults to 5
func WithRestartLimit(limit int) Option {
	return func(o *options) {
		o.restartLimit = limit
	}
}

// WithUser runs the process as the given user, which is a user name or a numeric UID
// If the user is in the user database, the process also gets the user's primary group (unless set with WithGroup) and supplementary groups
// A numeric UID that isn't in the user database is used as-is, but then the group must be set with WithGroup too
// The current process must be allowed to switch user, for example by running as root or with the CAP_SETUID and CAP_SETGID capabilities
// Not supported on Windows
func WithUser(user string) Option {
	return func(o *options) {
		o.user = user
	}
}

// WithGroup runs the process with the given primary group, which is a group name or a numeric GID
// A numeric GID that isn't in the group database is used as-is
// Without WithUser, the process keeps running as the current user, with no supplementary groups
// The current process must be allowed to switch group, for example by running as root or with the CAP_SETGID capability
// Not supported on Windows
func WithGroup(group string) Option {
	return func(o *options) {
		o.group = group
	}
}

// WithWorkingDirectory sets the working directory of the process
// Defaults to the working directory of the current process
func WithWorkingDirectory(dir string) Option {
	return func(o *options) {
		o.workingDirectory = dir
	}
}

// WithEnv sets an environment variable for the process
// It can be passed multiple times
// When the same key is set more than once, the last value wins
func WithEnv(key string, value string) Option {
	return func(o *options) {
		o.env = append(o.env, envVar{key: key, value: value})
	}
}

// WithParentEnv makes the process inherit the environment of the current process, in addition to the variables set with WithEnv, which take precedence
// By default, the process doesn't inherit the environment
func WithParentEnv() Option {
	return func(o *options) {
		o.parentEnv = true
	}
}

// WithStdin sets the stream the process reads its standard input from
// Without it, the standard input of the process is empty
// The stream is shared by every run of the process: after a restart, the process continues reading where the previous run left off, and once the stream ends every later run gets an empty input
// Data the previous run received but didn't read before exiting is lost
// Unless r is an *os.File, it's read by a background goroutine, which can remain blocked on a read after the service returns, until r returns
func WithStdin(r io.Reader) Option {
	return func(o *options) {
		o.stdin = r
	}
}

// WithStdout sets the stream that receives the standard output of the process, from every run
// Without it, the output is discarded
// Unless w is an *os.File, it's written to by a background goroutine
func WithStdout(w io.Writer) Option {
	return func(o *options) {
		o.stdout = w
	}
}

// WithStderr sets the stream that receives the standard error of the process, from every run
// Without it, the output is discarded
// Unless w is an *os.File, it's written to by a background goroutine
// If w is also passed to WithStdout and is comparable, at most one goroutine writes to it at a time
func WithStderr(w io.Writer) Option {
	return func(o *options) {
		o.stderr = w
	}
}

// WithOnStart sets a hook that is called every time the process starts, including after restarts, with the PID of the process
// The hook's context is canceled when that run of the process exits or the service is stopping, so it can be used to stop any work the hook starts in the background, like waiting for the process to accept connections
// The hook is called synchronously, so it must return quickly: the service can't stop the process until it does
// If the hook panics, the process is stopped and the service returns an error
// For example, to start a service that depends on the process once the process is running, signal a servicerunner.Ready from the hook
func WithOnStart(hook func(ctx context.Context, pid int)) Option {
	return func(o *options) {
		o.onStart = hook
	}
}

// WithLogger sets the logger to use instead of the default slog instance
func WithLogger(logger *slog.Logger) Option {
	return func(o *options) {
		o.logger = logger
	}
}
