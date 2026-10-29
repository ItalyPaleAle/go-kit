//go:build unix

package process

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/italypaleale/go-kit/servicerunner"
)

// syncBuffer is a bytes.Buffer that is safe for concurrent use, since the output of the process is copied by a background goroutine
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	// Time of each write
	writes []time.Time
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.writes = append(b.writes, time.Now())
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) Writes() []time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]time.Time(nil), b.writes...)
}

func (b *syncBuffer) waitForCount(t *testing.T, substr string, count int) {
	t.Helper()
	require.Eventually(t, func() bool {
		return strings.Count(b.String(), substr) >= count
	}, 5*time.Second, 5*time.Millisecond, "timed out waiting for %d occurrences of %q in %q", count, substr, b.String())
}

func shell(script string) Option {
	return WithCommand("sh", "-c", script)
}

// startService runs the service in the background and returns a channel that receives its result
// The restart delay is shortened so tests don't wait, but options can override it
func startService(t *testing.T, ctx context.Context, opts ...Option) <-chan error {
	t.Helper()

	opts = append([]Option{
		WithLogger(slog.New(slog.NewTextHandler(t.Output(), &slog.HandlerOptions{Level: slog.LevelDebug}))),
		WithRestartBackoff(5*time.Millisecond, 20*time.Millisecond),
	}, opts...)
	svc, err := NewService(opts...)
	require.NoError(t, err)

	errCh := make(chan error, 1)
	go func() {
		errCh <- svc(ctx)
	}()
	return errCh
}

func waitForResult(t *testing.T, errCh <-chan error) error {
	t.Helper()
	select {
	case err := <-errCh:
		return err
	case <-time.After(10 * time.Second):
		require.FailNow(t, "service did not return in time")
		return nil
	}
}

func TestStop(t *testing.T) {
	t.Run("stops gracefully when the context is canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		stdout := &syncBuffer{}
		errCh := startService(t, ctx,
			shell(`trap 'echo stopping; exit 0' TERM; echo ready; while :; do sleep 0.05; done`),
			WithStdout(stdout),
		)

		stdout.waitForCount(t, "ready", 1)
		cancel()

		err := waitForResult(t, errCh)
		require.NoError(t, err)
		assert.Equal(t, "ready\nstopping\n", stdout.String())
	})

	t.Run("an exit status after the shutdown signal is not an error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		stdout := &syncBuffer{}
		errCh := startService(t, ctx,
			shell(`trap 'exit 3' TERM; echo ready; while :; do sleep 0.05; done`),
			WithStdout(stdout),
		)

		stdout.waitForCount(t, "ready", 1)
		cancel()

		err := waitForResult(t, errCh)
		require.NoError(t, err)
	})

	t.Run("kills the process after the grace timeout", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		// The ignored signal is inherited by sleep too
		stdout := &syncBuffer{}
		errCh := startService(t, ctx,
			shell(`trap '' TERM; echo ready; while :; do sleep 0.05; done`),
			WithStdout(stdout),
			WithShutdownGraceTimeout(200*time.Millisecond),
		)

		stdout.waitForCount(t, "ready", 1)
		start := time.Now()
		cancel()

		err := waitForResult(t, errCh)
		require.Error(t, err)
		require.ErrorContains(t, err, "did not stop gracefully and was killed")
		assert.GreaterOrEqual(t, time.Since(start), 200*time.Millisecond)
	})

	t.Run("resumes a stopped process so it can handle the shutdown signal", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		stdout := &syncBuffer{}
		errCh := startService(t, ctx,
			shell(`trap 'echo stopping; exit 0' TERM; echo ready; kill -STOP $$; while :; do sleep 0.05; done`),
			WithStdout(stdout),
			WithShutdownGraceTimeout(2*time.Second),
		)

		// Give the shell time to stop itself, which it does right after printing
		stdout.waitForCount(t, "ready", 1)
		time.Sleep(100 * time.Millisecond)
		cancel()

		err := waitForResult(t, errCh)
		require.NoError(t, err)
		assert.Equal(t, "ready\nstopping\n", stdout.String())
	})

	t.Run("sends a custom shutdown signal", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		stdout := &syncBuffer{}
		errCh := startService(t, ctx,
			shell(`trap 'echo got usr1; exit 0' USR1; echo ready; while :; do sleep 0.05; done`),
			WithStdout(stdout),
			WithShutdownSignal(syscall.SIGUSR1),
		)

		stdout.waitForCount(t, "ready", 1)
		cancel()

		err := waitForResult(t, errCh)
		require.NoError(t, err)
		assert.Contains(t, stdout.String(), "got usr1")
	})

	t.Run("the shutdown signal reaches the process group", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		// The background subshell gets the signal only if it's sent to the whole process group, not just to the shell
		stdout := &syncBuffer{}
		errCh := startService(t, ctx,
			shell(`(trap 'echo child stopping; exit 0' TERM; echo ready; while :; do sleep 0.05; done) & wait`),
			WithStdout(stdout),
		)

		stdout.waitForCount(t, "ready", 1)
		cancel()

		err := waitForResult(t, errCh)
		require.NoError(t, err)
		assert.Contains(t, stdout.String(), "child stopping")
	})

	t.Run("does not start when the context is already canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		stdout := &syncBuffer{}
		errCh := startService(t, ctx, shell(`echo ran`), WithStdout(stdout))

		err := waitForResult(t, errCh)
		require.NoError(t, err)
		assert.Empty(t, stdout.String())
	})
}

func TestRestart(t *testing.T) {
	t.Run("never, process succeeds", func(t *testing.T) {
		errCh := startService(t, t.Context(), shell(`exit 0`), WithRestart(RestartNever))

		err := waitForResult(t, errCh)
		require.NoError(t, err)
	})

	t.Run("never, process fails", func(t *testing.T) {
		stdout := &syncBuffer{}
		errCh := startService(t, t.Context(), shell(`echo ran; exit 3`), WithRestart(RestartNever), WithStdout(stdout))

		err := waitForResult(t, errCh)
		require.Error(t, err)
		var exitErr *exec.ExitError
		require.ErrorAs(t, err, &exitErr)
		assert.Equal(t, 3, exitErr.ExitCode())
		assert.Equal(t, "ran\n", stdout.String())
	})

	t.Run("on failure restarts until the process succeeds", func(t *testing.T) {
		// The counter file is in the working directory of the process
		dir := t.TempDir()
		errCh := startService(t, t.Context(),
			shell(`n=$(cat count 2>/dev/null || echo 0); n=$((n+1)); echo $n > count; [ "$n" -ge 3 ] && exit 0; exit 1`),
			WithRestart(RestartOnFailure),
			WithWorkingDirectory(dir),
		)

		err := waitForResult(t, errCh)
		require.NoError(t, err)

		count, err := os.ReadFile(filepath.Join(dir, "count"))
		require.NoError(t, err)
		assert.Equal(t, "3", strings.TrimSpace(string(count)))
	})

	t.Run("always restarts after a successful exit", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		stdout := &syncBuffer{}
		errCh := startService(t, ctx, shell(`echo ran`), WithStdout(stdout), WithRestartLimit(0))

		stdout.waitForCount(t, "ran", 3)
		cancel()

		err := waitForResult(t, errCh)
		require.NoError(t, err)
	})

	t.Run("the restart delay doubles at each restart", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		stdout := &syncBuffer{}
		errCh := startService(t, ctx,
			shell(`echo ran`),
			WithStdout(stdout),
			WithRestartBackoff(20*time.Millisecond, 60*time.Millisecond),
			WithRestartLimit(0),
		)

		stdout.waitForCount(t, "ran", 5)
		cancel()

		err := waitForResult(t, errCh)
		require.NoError(t, err)

		// Only lower bounds are checked, as starting the process can take arbitrarily long on a busy machine
		writes := stdout.Writes()
		expectDelays := []time.Duration{20 * time.Millisecond, 40 * time.Millisecond, 60 * time.Millisecond, 60 * time.Millisecond}
		for i, expect := range expectDelays {
			assert.GreaterOrEqual(t, writes[i+1].Sub(writes[i]), expect, "delay before restart %d", i+1)
		}
	})

	t.Run("cancelling the context during the restart delay", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		stdout := &syncBuffer{}
		errCh := startService(t, ctx, shell(`echo ran`), WithStdout(stdout), WithRestartBackoff(time.Hour, time.Hour))

		stdout.waitForCount(t, "ran", 1)
		cancel()

		err := waitForResult(t, errCh)
		require.NoError(t, err)
		assert.Equal(t, "ran\n", stdout.String())
	})

	t.Run("does not restart when the process can't be started", func(t *testing.T) {
		errCh := startService(t, t.Context(), WithCommand("go-kit-process-test-command-does-not-exist"))

		err := waitForResult(t, errCh)
		require.Error(t, err)
		require.ErrorIs(t, err, exec.ErrNotFound)
		require.ErrorContains(t, err, "failed to start process")
	})
}

func TestRestartLimit(t *testing.T) {
	t.Run("gives up after the limit", func(t *testing.T) {
		stdout := &syncBuffer{}
		errCh := startService(t, t.Context(),
			shell(`echo ran; exit 1`),
			WithStdout(stdout),
			WithRestart(RestartOnFailure),
			WithRestartLimit(2),
		)

		err := waitForResult(t, errCh)
		require.Error(t, err)
		require.ErrorIs(t, err, ErrRestartLimitReached)
		var exitErr *exec.ExitError
		require.ErrorAs(t, err, &exitErr)
		assert.Equal(t, 1, exitErr.ExitCode())
		require.ErrorContains(t, err, "exited after being restarted 2 times in a row")

		// The first run, and then two restarts
		assert.Equal(t, 3, strings.Count(stdout.String(), "ran"))
	})

	t.Run("defaults to 5 restarts", func(t *testing.T) {
		stdout := &syncBuffer{}
		errCh := startService(t, t.Context(), shell(`echo ran; exit 1`), WithStdout(stdout))

		err := waitForResult(t, errCh)
		require.Error(t, err)
		require.ErrorIs(t, err, ErrRestartLimitReached)
		assert.Equal(t, 6, strings.Count(stdout.String(), "ran"))
	})

	t.Run("successful exits count too", func(t *testing.T) {
		stdout := &syncBuffer{}
		errCh := startService(t, t.Context(), shell(`echo ran`), WithStdout(stdout), WithRestartLimit(1))

		err := waitForResult(t, errCh)
		require.Error(t, err)
		require.ErrorIs(t, err, ErrRestartLimitReached)
		assert.NotErrorAs(t, err, new(*exec.ExitError))
		assert.Equal(t, 2, strings.Count(stdout.String(), "ran"))
	})

	t.Run("a healthy run resets the count", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		// Every run counts as healthy
		resetAfterAnyRun := func(o *options) {
			o.restartResetAfter = 0
		}

		stdout := &syncBuffer{}
		errCh := startService(t, ctx,
			shell(`echo ran; exit 1`),
			WithStdout(stdout),
			WithRestartLimit(1),
			resetAfterAnyRun,
		)

		stdout.waitForCount(t, "ran", 4)
		cancel()

		err := waitForResult(t, errCh)
		require.NoError(t, err)
	})

	t.Run("no limit", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		stdout := &syncBuffer{}
		errCh := startService(t, ctx, shell(`echo ran; exit 1`), WithStdout(stdout), WithRestartLimit(0))

		stdout.waitForCount(t, "ran", 10)
		cancel()

		err := waitForResult(t, errCh)
		require.NoError(t, err)
	})
}

func TestOnStart(t *testing.T) {
	t.Run("called with the PID at every start", func(t *testing.T) {
		var (
			mu   sync.Mutex
			pids []string
		)
		stdout := &syncBuffer{}
		errCh := startService(t, t.Context(),
			shell(`echo $$; exit 1`),
			WithStdout(stdout),
			WithRestartLimit(2),
			WithOnStart(func(_ context.Context, pid int) {
				mu.Lock()
				defer mu.Unlock()
				pids = append(pids, strconv.Itoa(pid))
			}),
		)

		err := waitForResult(t, errCh)
		require.ErrorIs(t, err, ErrRestartLimitReached)

		mu.Lock()
		defer mu.Unlock()
		require.Len(t, pids, 3)
		assert.Equal(t, strings.Join(pids, "\n")+"\n", stdout.String())
	})

	t.Run("the context ends with the run", func(t *testing.T) {
		hookCtxCh := make(chan context.Context, 1)
		hookErrCh := make(chan error, 1)
		errCh := startService(t, t.Context(),
			shell(`exit 0`),
			WithRestart(RestartNever),
			WithOnStart(func(ctx context.Context, _ int) {
				hookErrCh <- ctx.Err()
				hookCtxCh <- ctx
			}),
		)

		err := waitForResult(t, errCh)
		require.NoError(t, err)

		// The context was still active while the hook was running, and it's canceled once the process has exited
		err = <-hookErrCh
		require.NoError(t, err)
		hookCtx := <-hookCtxCh
		err = hookCtx.Err()
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("a panic stops the process", func(t *testing.T) {
		pidCh := make(chan int, 1)
		errCh := startService(t, t.Context(),
			shell(`while :; do sleep 0.05; done`),
			WithOnStart(func(_ context.Context, pid int) {
				pidCh <- pid
				panic("boom")
			}),
		)

		err := waitForResult(t, errCh)
		require.Error(t, err)
		require.ErrorContains(t, err, "start hook for process 'sh' panicked: boom")

		// The process has exited and was reaped
		err = syscall.Kill(<-pidCh, 0)
		require.ErrorIs(t, err, syscall.ESRCH)
	})
}

func TestEnvironment(t *testing.T) {
	t.Setenv("PROCESS_TEST_PARENT", "parent")
	t.Setenv("PROCESS_TEST_OVERRIDE", "parent")
	script := shell(`echo "$PROCESS_TEST_PARENT|$PROCESS_TEST_OVERRIDE|$PROCESS_TEST_CHILD"`)

	t.Run("does not inherit the parent environment by default", func(t *testing.T) {
		stdout := &syncBuffer{}
		errCh := startService(t, t.Context(), script,
			WithRestart(RestartNever),
			WithStdout(stdout),
			WithEnv("PROCESS_TEST_CHILD", "child"),
		)

		err := waitForResult(t, errCh)
		require.NoError(t, err)
		assert.Equal(t, "||child\n", stdout.String())
	})

	t.Run("with the parent environment", func(t *testing.T) {
		stdout := &syncBuffer{}
		errCh := startService(t, t.Context(), script, WithRestart(RestartNever), WithStdout(stdout), WithParentEnv())

		err := waitForResult(t, errCh)
		require.NoError(t, err)
		assert.Equal(t, "parent|parent|\n", stdout.String())
	})

	t.Run("with the parent environment, adding and overriding variables", func(t *testing.T) {
		stdout := &syncBuffer{}
		errCh := startService(t, t.Context(), script,
			WithRestart(RestartNever),
			WithStdout(stdout),
			WithParentEnv(),
			WithEnv("PROCESS_TEST_CHILD", "child"),
			WithEnv("PROCESS_TEST_OVERRIDE", "child"),
		)

		err := waitForResult(t, errCh)
		require.NoError(t, err)
		assert.Equal(t, "parent|child|child\n", stdout.String())
	})
}

func TestWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	stdout := &syncBuffer{}
	errCh := startService(t, t.Context(), shell(`pwd -P`), WithRestart(RestartNever), WithStdout(stdout), WithWorkingDirectory(dir))

	err := waitForResult(t, errCh)
	require.NoError(t, err)

	// The temporary directory may be behind a symlink, like on macOS
	expect, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	assert.Equal(t, expect+"\n", stdout.String())
}

func TestOutput(t *testing.T) {
	t.Run("stdout and stderr", func(t *testing.T) {
		stdout := &syncBuffer{}
		stderr := &syncBuffer{}
		errCh := startService(t, t.Context(),
			shell(`echo out; echo err >&2`),
			WithRestart(RestartNever),
			WithStdout(stdout),
			WithStderr(stderr),
		)

		err := waitForResult(t, errCh)
		require.NoError(t, err)
		assert.Equal(t, "out\n", stdout.String())
		assert.Equal(t, "err\n", stderr.String())
	})

	t.Run("output is discarded by default", func(t *testing.T) {
		errCh := startService(t, t.Context(), shell(`echo out; echo err >&2`), WithRestart(RestartNever))

		err := waitForResult(t, errCh)
		require.NoError(t, err)
	})
}

func TestStdin(t *testing.T) {
	t.Run("from a reader", func(t *testing.T) {
		stdout := &syncBuffer{}
		errCh := startService(t, t.Context(),
			WithCommand("cat"),
			WithRestart(RestartNever),
			WithStdin(strings.NewReader("hello\nworld\n")),
			WithStdout(stdout),
		)

		err := waitForResult(t, errCh)
		require.NoError(t, err)
		assert.Equal(t, "hello\nworld\n", stdout.String())
	})

	t.Run("from a file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "input")
		err := os.WriteFile(path, []byte("from file\n"), 0o600)
		require.NoError(t, err)
		f, err := os.Open(path)
		require.NoError(t, err)
		defer f.Close() //nolint:errcheck

		stdout := &syncBuffer{}
		errCh := startService(t, t.Context(), WithCommand("cat"), WithRestart(RestartNever), WithStdin(f), WithStdout(stdout))

		err = waitForResult(t, errCh)
		require.NoError(t, err)
		assert.Equal(t, "from file\n", stdout.String())
	})

	t.Run("empty by default", func(t *testing.T) {
		stdout := &syncBuffer{}
		errCh := startService(t, t.Context(), WithCommand("cat"), WithRestart(RestartNever), WithStdout(stdout))

		err := waitForResult(t, errCh)
		require.NoError(t, err)
		assert.Empty(t, stdout.String())
	})

	t.Run("the end of the stream closes the input", func(t *testing.T) {
		pr, pw := io.Pipe()
		stdout := &syncBuffer{}
		errCh := startService(t, t.Context(), WithCommand("cat"), WithRestart(RestartNever), WithStdin(pr), WithStdout(stdout))

		_, err := pw.Write([]byte("streamed\n"))
		require.NoError(t, err)
		stdout.waitForCount(t, "streamed", 1)
		err = pw.Close()
		require.NoError(t, err)

		err = waitForResult(t, errCh)
		require.NoError(t, err)
		assert.Equal(t, "streamed\n", stdout.String())
	})

	t.Run("the stream is shared across restarts", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		pr, pw := io.Pipe()
		defer pw.Close() //nolint:errcheck

		stdout := &syncBuffer{}
		errCh := startService(t, ctx,
			shell(`echo ready; read line; echo "got $line"; exit 1`),
			WithRestart(RestartOnFailure),
			WithStdin(pr),
			WithStdout(stdout),
		)

		// Each line is written once the run that should read it is up
		for i, line := range []string{"first", "second", "third"} {
			stdout.waitForCount(t, "ready", i+1)
			_, err := pw.Write([]byte(line + "\n"))
			require.NoError(t, err)
			stdout.waitForCount(t, "got "+line, 1)
		}

		cancel()
		err := waitForResult(t, errCh)
		require.NoError(t, err)
	})
}

func TestStdinPumpCarriesUnwrittenData(t *testing.T) {
	pump := newStdinPump(strings.NewReader("data"), slog.New(slog.DiscardHandler))
	defer pump.close()

	// The first run exits before taking its input, so the data goes to the next run
	pr, pw := io.Pipe()
	err := pr.Close()
	require.NoError(t, err)
	exited := make(chan struct{})
	pump.feed(pw, exited)
	assert.Equal(t, []byte("data"), pump.carry)

	pr, pw = io.Pipe()
	received := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(pr)
		received <- data
	}()
	pump.feed(pw, exited)
	assert.Equal(t, []byte("data"), <-received)
	assert.Empty(t, pump.carry)
}

func TestRunAsUser(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("Switching user requires running the tests as root")
	}

	t.Run("user in the database", func(t *testing.T) {
		nobody, err := user.Lookup("nobody")
		if err != nil {
			t.Skip("User 'nobody' doesn't exist")
		}

		stdout := &syncBuffer{}
		errCh := startService(t, t.Context(),
			shell(`id -u; id -g`),
			WithRestart(RestartNever),
			WithStdout(stdout),
			WithUser("nobody"),
			WithWorkingDirectory("/"),
		)

		err = waitForResult(t, errCh)
		require.NoError(t, err)
		assert.Equal(t, nobody.Uid+"\n"+nobody.Gid+"\n", stdout.String())
	})

	t.Run("numeric IDs not in the database", func(t *testing.T) {
		stdout := &syncBuffer{}
		errCh := startService(t, t.Context(),
			shell(`id -u; id -g; id -G`),
			WithRestart(RestartNever),
			WithStdout(stdout),
			WithUser("54321"),
			WithGroup("54322"),
			WithWorkingDirectory("/"),
		)

		err := waitForResult(t, errCh)
		require.NoError(t, err)

		// No supplementary groups are inherited from the current process
		assert.Equal(t, "54321\n54322\n54322\n", stdout.String())
	})
}

func TestResolveCredential(t *testing.T) {
	root, err := user.LookupId("0")
	require.NoError(t, err)
	rootGroups, err := supplementaryGroups(root)
	require.NoError(t, err)
	rootGroup, err := user.LookupGroupId("0")
	require.NoError(t, err)

	tests := []struct {
		name    string
		user    string
		group   string
		want    *credential
		wantErr string
	}{
		{
			name: "no user or group",
			want: nil,
		},
		{
			name: "user by name",
			user: root.Username,
			want: &credential{Uid: 0, Gid: 0, Groups: rootGroups},
		},
		{
			name: "user by ID",
			user: "0",
			want: &credential{Uid: 0, Gid: 0, Groups: rootGroups},
		},
		{
			name:  "user with a different group",
			user:  "0",
			group: "54322",
			want:  &credential{Uid: 0, Gid: 54322, Groups: rootGroups},
		},
		{
			name:  "numeric IDs not in the database",
			user:  "54321",
			group: "54322",
			want:  &credential{Uid: 54321, Gid: 54322},
		},
		{
			name:  "group by name only",
			group: rootGroup.Name,
			want:  &credential{Uid: uint32(os.Getuid()), Gid: 0}, //nolint:gosec
		},
		{
			name:    "numeric user not in the database without a group",
			user:    "54321",
			wantErr: "must be set with WithGroup",
		},
		{
			name:    "unknown user name",
			user:    "go-kit-no-such-user",
			wantErr: `failed to look up user "go-kit-no-such-user"`,
		},
		{
			name:    "unknown group name",
			group:   "go-kit-no-such-group",
			wantErr: `failed to look up group "go-kit-no-such-group"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cred, err := resolveCredential(tc.user, tc.group)
			if tc.wantErr != "" {
				require.Error(t, err)
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, cred)
		})
	}
}

// fakeSignal is an os.Signal that isn't a syscall.Signal
type fakeSignal struct{}

func (fakeSignal) String() string { return "fake" }
func (fakeSignal) Signal()        {}

func TestSignalValidation(t *testing.T) {
	t.Run("unsupported shutdown signal", func(t *testing.T) {
		_, err := NewService(WithCommand("foo"), WithShutdownSignal(fakeSignal{}))
		require.Error(t, err)
		require.ErrorContains(t, err, "unsupported shutdown signal: fake")
	})

	t.Run("unsupported parent death signal", func(t *testing.T) {
		_, err := NewService(WithCommand("foo"), WithParentDeathSignal(fakeSignal{}))
		require.Error(t, err)
		require.ErrorContains(t, err, "unsupported parent death signal: fake")
	})

	t.Run("nil parent death signal disables it", func(t *testing.T) {
		_, err := NewService(WithCommand("foo"), WithParentDeathSignal(nil))
		require.NoError(t, err)
	})
}

func TestWithServiceRunner(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	// The other service starts only once the process is running
	ready := servicerunner.NewReady()
	svc, err := NewService(
		shell(`while :; do sleep 0.05; done`),
		WithOnStart(func(context.Context, int) {
			ready.Signal()
		}),
		WithLogger(slog.New(slog.DiscardHandler)),
	)
	require.NoError(t, err)

	// When the other service returns, the runner cancels the context, which stops the process
	var otherRan atomic.Bool
	other := ready.Await(func(ctx context.Context) error {
		otherRan.Store(true)
		return nil
	})

	err = servicerunner.NewServiceRunner(svc, other).Run(ctx)
	require.NoError(t, err)
	assert.True(t, otherRan.Load())
}
