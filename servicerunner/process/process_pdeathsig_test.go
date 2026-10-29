//go:build linux || freebsd

package process

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const parentDeathHelperEnv = "GO_KIT_PROCESS_PARENT_DEATH_HELPER"

// TestParentDeathSignalHelper runs the service in a separate process, so TestParentDeathSignal can kill it with SIGKILL
// It does nothing unless started by TestParentDeathSignal
func TestParentDeathSignalHelper(t *testing.T) {
	mode := os.Getenv(parentDeathHelperEnv)
	if mode == "" {
		return
	}

	// The process writes to the same pipe as this helper, so the test can read its output after the helper is killed
	started := make(chan struct{})
	opts := []Option{
		shell(`trap 'echo got term; exit 0' TERM; echo "ready $$"; while :; do sleep 0.05; done`),
		WithStdout(os.Stdout),
		WithLogger(slog.New(slog.DiscardHandler)),
		WithOnStart(func(context.Context, int) {
			close(started)
		}),
	}
	if mode == "signal" {
		opts = append(opts, WithParentDeathSignal(syscall.SIGTERM))
	}

	svc, err := NewService(opts...)
	require.NoError(t, err)

	// Go never ends the main thread, which this goroutine is likely running on
	// Keeping this goroutine on its thread makes the service start the process from a different thread, which can end
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// The test kills this process while the service is running
	go func() {
		_ = svc(t.Context())
	}()

	<-started

	// Goroutines that lock their thread and return without unlocking it end their thread
	// Unless the service keeps the thread that started the process to itself, that thread is likely to be one of them, and the process would get the signal while the helper is still running
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			runtime.LockOSThread()
			time.Sleep(10 * time.Millisecond)
		})
	}
	wg.Wait()

	fmt.Println("threads ended")
	select {}
}

func TestParentDeathSignal(t *testing.T) {
	t.Run("the process is signaled when the application dies", func(t *testing.T) {
		h := startParentDeathHelper(t, "signal")

		// Ending threads in the helper must not send the signal
		expectNoOutput(t, h.lines, 300*time.Millisecond)

		h.kill(t)
		assert.Equal(t, "got term", nextLine(t, h.lines))

		// The output ends once the process and the processes it spawned have exited
		select {
		case line, ok := <-h.lines:
			require.False(t, ok, "unexpected output: %q", line)
		case <-time.After(10 * time.Second):
			require.FailNow(t, "process did not exit in time")
		}
	})

	t.Run("without the option, the process is left running", func(t *testing.T) {
		h := startParentDeathHelper(t, "none")
		h.kill(t)

		expectNoOutput(t, h.lines, 300*time.Millisecond)
		err := syscall.Kill(h.pid, 0)
		require.NoError(t, err)
	})
}

// parentDeathHelper is a running TestParentDeathSignalHelper
type parentDeathHelper struct {
	cmd *exec.Cmd
	// Lines written by the helper and by the process it started
	lines <-chan string
	// PID of the process started by the service
	pid int
}

// startParentDeathHelper starts the helper and waits until the service has started the process and the helper has ended its threads
func startParentDeathHelper(t *testing.T, mode string) *parentDeathHelper {
	t.Helper()

	// The process inherits the write end of the pipe, so its output can still be read after the helper is killed
	pr, pw, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = pr.Close()
	})

	cmd := exec.Command(os.Args[0], "-test.run=^TestParentDeathSignalHelper$") //nolint:gosec // Runs the test binary itself
	cmd.Env = append(os.Environ(), parentDeathHelperEnv+"="+mode)
	cmd.Stdout = pw
	cmd.Stderr = os.Stderr
	err = cmd.Start()
	require.NoError(t, err)
	t.Cleanup(func() {
		// The helper is still running if the test failed before killing it
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	err = pw.Close()
	require.NoError(t, err)

	lines := make(chan string, 16)
	go func() {
		defer close(lines)

		// Reading ends when every process holding the write end has exited, or when the pipe is closed at the end of the test
		reader := bufio.NewReader(pr)
		for {
			line, rErr := reader.ReadString('\n')
			if rErr != nil {
				return
			}
			lines <- strings.TrimSuffix(line, "\n")
		}
	}()

	h := &parentDeathHelper{
		cmd:   cmd,
		lines: lines,
	}
	threadsEnded := false
	for h.pid == 0 || !threadsEnded {
		line := nextLine(t, lines)
		switch {
		case line == "threads ended":
			threadsEnded = true
		case strings.HasPrefix(line, "ready "):
			h.pid, err = strconv.Atoi(strings.TrimPrefix(line, "ready "))
			require.NoError(t, err)

			// The process runs in its own process group, which is left running when the helper dies without the option
			pgid := h.pid
			t.Cleanup(func() {
				_ = syscall.Kill(-pgid, syscall.SIGKILL)
			})
		default:
			require.FailNow(t, "unexpected output", "%q", line)
		}
	}

	return h
}

// kill kills the helper with SIGKILL, so the service doesn't get to stop the process
func (h *parentDeathHelper) kill(t *testing.T) {
	t.Helper()

	err := h.cmd.Process.Kill()
	require.NoError(t, err)

	err = h.cmd.Wait()
	require.ErrorAs(t, err, new(*exec.ExitError))
}

func nextLine(t *testing.T, lines <-chan string) string {
	t.Helper()

	select {
	case line, ok := <-lines:
		require.True(t, ok, "output ended unexpectedly")
		return line
	case <-time.After(10 * time.Second):
		require.FailNow(t, "timed out waiting for output")
		return ""
	}
}

func expectNoOutput(t *testing.T, lines <-chan string, wait time.Duration) {
	t.Helper()

	select {
	case line, ok := <-lines:
		if ok {
			require.FailNow(t, "unexpected output", "%q", line)
		}
		require.FailNow(t, "output ended unexpectedly")
	case <-time.After(wait):
	}
}
