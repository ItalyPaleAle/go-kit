// Package process runs an external process as a servicerunner.Service, making the application its process manager.
// The process runs until the service's context is canceled, and it's restarted according to the restart policy when it exits on its own.
//
// To stop the process, the service sends it the shutdown signal (SIGTERM by default) and, if the process is still running after the grace timeout, kills it.
//
// On Unix, the process runs in its own process group: signals sent to stop it also reach any process it spawned, while signals from the terminal (like Ctrl+C) reach only the application, which stays in charge of stopping the process.
// Because of that, the process can't read from the controlling terminal.
//
// On Windows, processes can't be sent signals, so the process is killed right away when the service stops.
package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"time"

	"github.com/italypaleale/go-kit/servicerunner"
)

// NewService returns a service that runs an external process until the service's context is canceled
// The command must be set with WithCommand
//
// The service returns when:
//   - The context is canceled: the process is stopped, and the service returns nil, or an error if the process didn't stop gracefully and had to be killed
//   - The process exits on its own and the restart policy doesn't restart it: the service returns nil if the process exited successfully, or an error wrapping *exec.ExitError otherwise
//   - The process exits again after reaching the restart limit: the service returns an error wrapping ErrRestartLimitReached, and the *exec.ExitError if the process failed
//   - The process can't be started (for example, because the command doesn't exist): the service returns the error right away, without restarting the process
//   - The start hook panics: the process is stopped, and the service returns an error
func NewService(opts ...Option) (servicerunner.Service, error) {
	p, err := newProcess(opts...)
	if err != nil {
		return nil, err
	}

	return p.run, nil
}

type process struct {
	options

	// Credential to run the process as, or nil to run it as the current user
	cred *credential
	// Variables set with WithEnv, in the "key=value" format
	env []string
	log *slog.Logger
}

func newProcess(opts ...Option) (*process, error) {
	o := defaultOptions()
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}

	err := o.validate()
	if err != nil {
		return nil, err
	}

	cred, err := resolveCredential(o.user, o.group)
	if err != nil {
		return nil, err
	}

	log := o.logger
	if log == nil {
		log = slog.Default()
	}

	return &process{
		options: o,
		cred:    cred,
		env:     o.envList(),
		log:     log.With(slog.String("command", o.command)),
	}, nil
}

func (o *options) envList() []string {
	env := make([]string, 0, len(o.env))
	for _, v := range o.env {
		env = append(env, v.key+"="+v.value)
	}
	return env
}

// environ returns the environment for the process, in the format of exec.Cmd.Env
// A nil slice makes the process inherit the environment of the current process, while an empty non-nil one gives it no variables at all
func (p *process) environ() []string {
	switch {
	case !p.parentEnv:
		return p.env
	case len(p.env) == 0:
		return nil
	default:
		// The variables set with WithEnv come last, so they take precedence over inherited ones with the same name
		return append(os.Environ(), p.env...)
	}
}

// runResult is the outcome of a single run of the process
type runResult struct {
	// State of the process after it exited, or nil if it couldn't be waited for
	state *os.ProcessState
	// Error returned by exec.Cmd.Wait
	waitErr error
	// True if the process was stopped because the context was canceled
	stopped bool
	// True if the process was killed because it didn't exit within the grace timeout
	killed bool
}

// exitError returns the reason the process failed, or nil if it exited successfully
// Errors copying the output after a successful exit are not counted as failures of the process
func (r runResult) exitError() error {
	if r.state != nil && r.state.Success() {
		return nil
	}
	return r.waitErr
}

func (p *process) run(ctx context.Context) error {
	// A stream that isn't a file is read by a single pump shared by every run of the process, so restarts don't race over its data
	var pump *stdinPump
	if p.stdin != nil {
		_, isFile := p.stdin.(*os.File)
		if !isFile {
			pump = newStdinPump(p.stdin, p.log)
			defer pump.close()
		}
	}

	delay := p.restartDelayInitial
	restarts := 0
	for {
		// Don't start (or restart) the process if the service is already stopping
		if ctx.Err() != nil {
			return nil
		}

		startedAt := time.Now()
		res, err := p.runOnce(ctx, pump)
		if err != nil {
			return err
		}

		if res.stopped {
			if res.killed {
				return fmt.Errorf("process '%s' did not stop gracefully and was killed", p.command)
			}
			return nil
		}

		// The process exited on its own
		exitErr := res.exitError()
		if exitErr != nil {
			p.log.WarnContext(ctx, "Process exited with an error", slog.Any("error", exitErr))
		} else {
			p.log.InfoContext(ctx, "Process exited")
			if res.waitErr != nil {
				p.log.WarnContext(ctx, "Failed to copy the output of the process", slog.Any("error", res.waitErr))
			}
		}

		if !p.shouldRestart(exitErr) {
			if exitErr != nil {
				return fmt.Errorf("process '%s' exited: %w", p.command, exitErr)
			}

			return nil
		}

		// A run that lasted long enough means the process was healthy, so this restart doesn't need to back off, nor count toward the limit
		if time.Since(startedAt) >= p.restartResetAfter {
			delay = p.restartDelayInitial
			restarts = 0
		}

		if p.restartLimit > 0 && restarts >= p.restartLimit {
			if exitErr != nil {
				return fmt.Errorf("process '%s' exited after being restarted %d times in a row: %w: %w", p.command, restarts, ErrRestartLimitReached, exitErr)
			}

			return fmt.Errorf("process '%s' exited after being restarted %d times in a row: %w", p.command, restarts, ErrRestartLimitReached)
		}

		restarts++

		p.log.InfoContext(ctx, "Restarting process", slog.Duration("delay", delay), slog.Int("restart", restarts))
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}

		delay = min(delay*2, p.restartDelayMax)
	}
}

func (p *process) shouldRestart(exitErr error) bool {
	switch p.restartPolicy {
	case RestartAlways:
		return true
	case RestartOnFailure:
		return exitErr != nil
	default:
		return false
	}
}

// runOnce starts the process and waits for it to exit, stopping it if the context is canceled
// It returns an error only when the service must return right away: if the process couldn't be started, or if the start hook panicked
func (p *process) runOnce(ctx context.Context, pump *stdinPump) (runResult, error) {
	cmd := exec.Command(p.command, p.args...) //nolint:gosec // Running a command chosen by the caller is the purpose of this package
	cmd.Dir = p.workingDirectory
	cmd.Env = p.environ()
	cmd.Stdout = p.stdout
	cmd.Stderr = p.stderr
	cmd.SysProcAttr = sysProcAttr(p.cred)

	// After the process exits, Wait waits for its output to be copied, which never completes if a process it spawned keeps the output streams open
	cmd.WaitDelay = p.shutdownGraceTimeout

	var (
		stdinPipe io.WriteCloser
		err       error
	)
	if pump != nil {
		stdinPipe, err = cmd.StdinPipe()
		if err != nil {
			return runResult{}, fmt.Errorf("failed to start process '%s': %w", p.command, err)
		}
	} else {
		cmd.Stdin = p.stdin
	}

	err = cmd.Start()
	if err != nil {
		return runResult{}, fmt.Errorf("failed to start process '%s': %w", p.command, err)
	}

	log := p.log.With(slog.Int("pid", cmd.Process.Pid))
	log.InfoContext(ctx, "Process started")

	var waitErr error
	exited := make(chan struct{})
	go func() {
		waitErr = cmd.Wait()
		close(exited)
	}()

	// The next run must not start reading from the pump until this run is done with it
	feedDone := make(chan struct{})
	if pump != nil {
		go func() {
			defer close(feedDone)
			pump.feed(stdinPipe, exited)
		}()
	} else {
		close(feedDone)
	}

	// The hook's context ends with this run of the process
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	hookErr := p.notifyStart(runCtx, cmd.Process.Pid)

	var res runResult
	if hookErr != nil {
		// The service is about to return, so the process must not outlive it
		res.stopped = true
		res.killed = p.stop(ctx, cmd.Process, exited, log)
	} else {
		select {
		case <-exited:
			// The process exited on its own
		case <-ctx.Done():
			res.stopped = true
			res.killed = p.stop(ctx, cmd.Process, exited, log)
		}
	}

	<-feedDone
	res.state = cmd.ProcessState
	res.waitErr = waitErr
	return res, hookErr
}

// notifyStart calls the start hook, if any
// A panic in the hook is returned as an error, so the process can be stopped before the service returns
func (p *process) notifyStart(ctx context.Context, pid int) (err error) {
	if p.onStart == nil {
		return nil
	}

	defer func() {
		r := recover()
		if r != nil {
			err = fmt.Errorf("start hook for process '%s' panicked: %v", p.command, r)
		}
	}()

	p.onStart(ctx, pid)

	return nil
}

// stop sends the shutdown signal to the process and waits for it to exit, killing it if it's still running after the grace timeout
// It returns true if the process didn't stop gracefully and was killed
func (p *process) stop(ctx context.Context, proc *os.Process, exited <-chan struct{}, log *slog.Logger) bool {
	// The context is already canceled, but it still carries values the logger may use
	ctx = context.WithoutCancel(ctx)

	log.InfoContext(ctx, "Stopping process", slog.String("signal", p.shutdownSignal.String()))
	err := signalProcess(proc, p.shutdownSignal)
	switch {
	case err == nil:
		timer := time.NewTimer(p.shutdownGraceTimeout)
		select {
		case <-exited:
			timer.Stop()
			log.InfoContext(ctx, "Process stopped")
			return false
		case <-timer.C:
			log.WarnContext(ctx, "Process did not exit within the shutdown grace timeout, killing it", slog.Duration("timeout", p.shutdownGraceTimeout))
		}
	case errors.Is(err, os.ErrProcessDone):
		// The process exited at the same time as the context was canceled
		<-exited
		return false
	case errors.Is(err, errors.ErrUnsupported):
		// The platform can't deliver the shutdown signal, so killing the process is the only way to stop it
		log.DebugContext(ctx, "Shutdown signal not supported on this platform, killing the process")
		killAndWait(ctx, proc, exited, log)
		return false
	default:
		log.WarnContext(ctx, "Failed to send the shutdown signal to the process, killing it", slog.Any("error", err))
	}

	killAndWait(ctx, proc, exited, log)
	return true
}

func killAndWait(ctx context.Context, proc *os.Process, exited <-chan struct{}, log *slog.Logger) {
	err := killProcess(proc)
	if err != nil {
		log.ErrorContext(ctx, "Failed to kill the process", slog.Any("error", err))
	}

	<-exited
}
