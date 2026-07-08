package servicerunner

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReady(t *testing.T) {
	t.Run("Await runs the service after Signal", func(t *testing.T) {
		r := NewReady()

		var ran atomic.Bool
		svc := r.Await(func(ctx context.Context) error {
			ran.Store(true)
			return nil
		})

		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()

		errCh := make(chan error, 1)
		go func() {
			errCh <- svc(ctx)
		}()

		// The service must not start before the signal
		time.Sleep(50 * time.Millisecond)
		require.False(t, ran.Load(), "service should not run before Signal")

		// After signaling, the service runs and returns
		r.Signal()

		select {
		case err := <-errCh:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for the service to complete")
		}
		require.True(t, ran.Load(), "service should run after Signal")
	})

	t.Run("Signal before Await runs the service immediately", func(t *testing.T) {
		r := NewReady()
		r.Signal()

		var ran atomic.Bool
		svc := r.Await(func(ctx context.Context) error {
			ran.Store(true)
			return nil
		})

		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()

		err := svc(ctx)
		require.NoError(t, err)
		require.True(t, ran.Load())
	})

	t.Run("context canceled before Signal returns the context error", func(t *testing.T) {
		r := NewReady()

		var ran atomic.Bool
		svc := r.Await(func(ctx context.Context) error {
			ran.Store(true)
			return nil
		})

		ctx, cancel := context.WithCancel(t.Context())

		errCh := make(chan error, 1)
		go func() {
			errCh <- svc(ctx)
		}()

		// Cancel before ever signaling: the wrapped service must never start
		cancel()

		select {
		case err := <-errCh:
			require.ErrorIs(t, err, context.Canceled)
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for the service to return")
		}
		require.False(t, ran.Load(), "service should not run when the context is canceled before Signal")
	})

	t.Run("Await propagates the service error", func(t *testing.T) {
		r := NewReady()
		r.Signal()

		expectedErr := errors.New("service failed")
		svc := r.Await(func(ctx context.Context) error {
			return expectedErr
		})

		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()

		err := svc(ctx)
		require.ErrorIs(t, err, expectedErr)
	})

	t.Run("Signal is idempotent", func(t *testing.T) {
		r := NewReady()

		// Multiple signals must not panic
		require.NotPanics(t, func() {
			r.Signal()
			r.Signal()
			r.Signal()
		})

		var ran atomic.Bool
		svc := r.Await(func(ctx context.Context) error {
			ran.Store(true)
			return nil
		})

		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()

		require.NoError(t, svc(ctx))
		require.True(t, ran.Load())
	})

	t.Run("Signal broadcasts to multiple Await waiters", func(t *testing.T) {
		r := NewReady()

		const n = 3
		var ranCount atomic.Int32

		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()

		errCh := make(chan error, n)
		for range n {
			svc := r.Await(func(ctx context.Context) error {
				ranCount.Add(1)
				return nil
			})
			go func() {
				errCh <- svc(ctx)
			}()
		}

		// None should have started yet
		time.Sleep(50 * time.Millisecond)
		require.Zero(t, ranCount.Load(), "no waiter should run before Signal")

		// A single signal must wake every waiter
		r.Signal()

		for range n {
			select {
			case err := <-errCh:
				require.NoError(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for a waiter to complete")
			}
		}
		require.Equal(t, int32(n), ranCount.Load())
	})

	t.Run("integration with ServiceRunner enforces ordering", func(t *testing.T) {
		r := NewReady()

		var dependencyUp atomic.Bool
		var dependentSawDependency atomic.Bool

		// A long-running dependency: it signals readiness once it is up, then runs until canceled
		dependency := func(ctx context.Context) error {
			dependencyUp.Store(true)
			r.Signal()
			<-ctx.Done()
			return ctx.Err()
		}

		// The dependent must only start once the dependency has signaled readiness.
		// When it returns cleanly the runner cancels the context, which stops the dependency too.
		dependent := r.Await(func(ctx context.Context) error {
			dependentSawDependency.Store(dependencyUp.Load())
			return nil
		})

		runner := NewServiceRunner(dependency, dependent)

		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()

		err := runner.Run(ctx)
		require.NoError(t, err)
		require.True(t, dependentSawDependency.Load(), "dependent should observe the dependency as ready before starting")
	})
}
