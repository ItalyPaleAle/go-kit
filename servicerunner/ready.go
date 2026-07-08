package servicerunner

import (
	"context"
	"sync"
)

// Ready allows making a Service wait for a signal before it can be started.
// It is used to make a service depend on another one, still managed by ServiceRunner.
// The dependency calls `r.Signal()` once it's up.
type Ready struct {
	ch   chan struct{}
	once sync.Once
}

func NewReady() *Ready {
	return &Ready{
		ch: make(chan struct{}),
	}
}

func (r *Ready) Signal() {
	// Close the channel to signal the service can start
	r.once.Do(func() {
		close(r.ch)
	})
}

func (r *Ready) Await(svc Service) Service {
	return func(ctx context.Context) error {
		// Wait for the ready signal
		select {
		case <-r.ch:
			// All good, fallthrough
		case <-ctx.Done():
			return ctx.Err()
		}

		// We can invoke the service now
		return svc(ctx)
	}
}
