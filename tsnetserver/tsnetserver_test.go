package tsnetserver

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/ipn/store/mem"
	"tailscale.com/tsnet"
)

// fakeNode is a tsnetNode that returns the configured errors and counts the calls it receives
type fakeNode struct {
	startErr error
	upErr    error

	upCalls    int
	closeCalls int
}

func (n *fakeNode) Start() error {
	return n.startErr
}

func (n *fakeNode) Up(context.Context) (*ipnstate.Status, error) {
	n.upCalls++
	if n.upErr != nil {
		return nil, n.upErr
	}

	return &ipnstate.Status{}, nil
}

func (n *fakeNode) Close() error {
	n.closeCalls++
	return nil
}

func TestBringUp(t *testing.T) {
	t.Run("closes a node that started but did not come up", func(t *testing.T) {
		upErr := context.Canceled
		node := &fakeNode{upErr: upErr}

		state, err := bringUp(t.Context(), node)
		require.ErrorIs(t, err, upErr)
		assert.Nil(t, state)
		assert.Equal(t, 1, node.closeCalls)
	})

	t.Run("does not close a node that failed to start", func(t *testing.T) {
		startErr := errors.New("start failed")
		node := &fakeNode{startErr: startErr}

		state, err := bringUp(t.Context(), node)
		require.ErrorIs(t, err, startErr)
		assert.Nil(t, state)
		assert.Equal(t, 0, node.upCalls)
		assert.Equal(t, 0, node.closeCalls)
	})

	t.Run("does not close a node that came up", func(t *testing.T) {
		node := &fakeNode{}

		state, err := bringUp(t.Context(), node)
		require.NoError(t, err)
		assert.NotNil(t, state)
		assert.Equal(t, 0, node.closeCalls)
	})

	t.Run("returns the error of a tsnet server that failed to start", func(t *testing.T) {
		// tsnet rejects this configuration before starting anything, so no network is involved
		// Calling Close on this server would panic, as it never finished starting
		srv := &tsnet.Server{
			Dir:   t.TempDir(),
			Store: new(mem.Store),
		}

		state, err := bringUp(t.Context(), srv)
		require.ErrorContains(t, err, "failed to start Tailscale node")
		assert.Nil(t, state)
	})
}
