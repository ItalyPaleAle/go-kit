package process

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewProcessDefaults(t *testing.T) {
	p, err := newProcess(WithCommand("foo", "a", "b"))
	require.NoError(t, err)

	assert.Equal(t, "foo", p.command)
	assert.Equal(t, []string{"a", "b"}, p.args)
	assert.Equal(t, 5*time.Second, p.shutdownGraceTimeout)
	assert.Equal(t, defaultShutdownSignal, p.shutdownSignal)
	assert.Equal(t, RestartAlways, p.restartPolicy)
	assert.Equal(t, time.Second, p.restartDelayInitial)
	assert.Equal(t, 30*time.Second, p.restartDelayMax)
	assert.Equal(t, 5, p.restartLimit)
	assert.Equal(t, 10*time.Second, p.restartResetAfter)
	assert.Empty(t, p.workingDirectory)
	assert.Nil(t, p.cred)
	assert.Nil(t, p.onStart)
	assert.NotNil(t, p.log)

	// The environment must be empty but not nil, or os/exec would inherit the parent environment
	env := p.environ()
	assert.NotNil(t, env)
	assert.Empty(t, env)
}

func TestWithCommandCopiesArgs(t *testing.T) {
	args := []string{"a", "b"}
	p, err := newProcess(WithCommand("foo", args...))
	require.NoError(t, err)

	args[0] = "changed"
	assert.Equal(t, []string{"a", "b"}, p.args)
}

func TestNewProcessValidation(t *testing.T) {
	tests := []struct {
		name    string
		opts    []Option
		wantErr string
	}{
		{
			name:    "missing command",
			opts:    nil,
			wantErr: "command is required",
		},
		{
			name:    "zero grace timeout",
			opts:    []Option{WithCommand("foo"), WithShutdownGraceTimeout(0)},
			wantErr: "shutdown grace timeout must be greater than zero",
		},
		{
			name:    "negative grace timeout",
			opts:    []Option{WithCommand("foo"), WithShutdownGraceTimeout(-time.Second)},
			wantErr: "shutdown grace timeout must be greater than zero",
		},
		{
			name:    "nil shutdown signal",
			opts:    []Option{WithCommand("foo"), WithShutdownSignal(nil)},
			wantErr: "shutdown signal must not be nil",
		},
		{
			name:    "invalid restart policy",
			opts:    []Option{WithCommand("foo"), WithRestart("sometimes")},
			wantErr: `invalid restart policy: "sometimes"`,
		},
		{
			name:    "zero initial restart delay",
			opts:    []Option{WithCommand("foo"), WithRestartBackoff(0, time.Second)},
			wantErr: "initial restart delay must be greater than zero",
		},
		{
			name:    "max restart delay less than initial",
			opts:    []Option{WithCommand("foo"), WithRestartBackoff(time.Second, time.Millisecond)},
			wantErr: "maximum restart delay must not be less than the initial restart delay",
		},
		{
			name:    "negative restart limit",
			opts:    []Option{WithCommand("foo"), WithRestartLimit(-1)},
			wantErr: "restart limit must not be negative",
		},
		{
			name:    "empty env key",
			opts:    []Option{WithCommand("foo"), WithEnv("", "bar")},
			wantErr: `invalid environment variable name: ""`,
		},
		{
			name:    "env key with equal sign",
			opts:    []Option{WithCommand("foo"), WithEnv("A=B", "bar")},
			wantErr: `invalid environment variable name: "A=B"`,
		},
		{
			name:    "env value with NUL",
			opts:    []Option{WithCommand("foo"), WithEnv("A", "b\x00c")},
			wantErr: `value of environment variable "A" contains a NUL character`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, err := NewService(tc.opts...)
			require.Error(t, err)
			require.ErrorContains(t, err, tc.wantErr)
			assert.Nil(t, svc)
		})
	}
}

func TestNewProcessIgnoresNilOptions(t *testing.T) {
	_, err := newProcess(nil, WithCommand("foo"), nil)
	require.NoError(t, err)
}

func TestEnviron(t *testing.T) {
	t.Setenv("PROCESS_TEST_PARENT", "parent")
	t.Setenv("PROCESS_TEST_OVERRIDE", "parent")

	t.Run("does not inherit the parent environment by default", func(t *testing.T) {
		p, err := newProcess(WithCommand("foo"), WithEnv("A", "1"), WithEnv("B", "2"))
		require.NoError(t, err)
		assert.Equal(t, []string{"A=1", "B=2"}, p.environ())
	})

	t.Run("with the parent environment", func(t *testing.T) {
		p, err := newProcess(
			WithCommand("foo"),
			WithParentEnv(),
			WithEnv("PROCESS_TEST_OVERRIDE", "first"),
			WithEnv("PROCESS_TEST_OVERRIDE", "child"),
		)
		require.NoError(t, err)

		env := p.environ()
		assert.Contains(t, env, "PROCESS_TEST_PARENT=parent")

		// Variables set with WithEnv come after the inherited ones, so os/exec keeps them when removing duplicates
		assert.Equal(t, "PROCESS_TEST_OVERRIDE=child", env[len(env)-1])
		assert.Len(t, env, len(os.Environ())+2)
	})

	t.Run("with the parent environment and no variables", func(t *testing.T) {
		p, err := newProcess(WithCommand("foo"), WithParentEnv())
		require.NoError(t, err)

		// A nil slice makes os/exec inherit the parent environment
		assert.Nil(t, p.environ())
	})
}

func TestShouldRestart(t *testing.T) {
	failure := assert.AnError

	tests := []struct {
		policy      RestartPolicy
		exitErr     error
		wantRestart bool
	}{
		{policy: RestartAlways, exitErr: nil, wantRestart: true},
		{policy: RestartAlways, exitErr: failure, wantRestart: true},
		{policy: RestartOnFailure, exitErr: nil, wantRestart: false},
		{policy: RestartOnFailure, exitErr: failure, wantRestart: true},
		{policy: RestartNever, exitErr: nil, wantRestart: false},
		{policy: RestartNever, exitErr: failure, wantRestart: false},
	}

	for _, tc := range tests {
		p, err := newProcess(WithCommand("foo"), WithRestart(tc.policy))
		require.NoError(t, err)
		assert.Equal(t, tc.wantRestart, p.shouldRestart(tc.exitErr), "policy=%s exitErr=%v", tc.policy, tc.exitErr)
	}
}
