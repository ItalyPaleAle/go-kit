// Package servicerunner manages multiple services (functions that implement [Service]) running concurrently in the background.
// When any service returns, whether with an error or not, the others are canceled via the context so the group shuts down together (unless the service runner is configured with WaitAll = true).
// The runner waits for all services to complete and returns any errors joined together.
package servicerunner

import (
	"context"
	"errors"
	"fmt"
)

// Service is a background service
type Service func(ctx context.Context) error

// ServiceRunner oversees a number of services running in background
type ServiceRunner struct {
	// WaitAll when true makes the service runner not cancel the context when the first service stops
	// This can be used for shutdown services when each service needs to run till completion
	WaitAll bool

	services []Service
}

// NewServiceRunner creates a new ServiceRunner
func NewServiceRunner(services ...Service) *ServiceRunner {
	return &ServiceRunner{
		services: services,
	}
}

// Run all background services
func (r *ServiceRunner) Run(parentCtx context.Context) error {
	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	// Snapshot "WaitAll" so it can't be changed later
	waitAll := r.WaitAll

	errCh := make(chan error)
	for _, service := range r.services {
		go func(service Service) {
			// Convert any panic into an error so the drain loop always receives exactly one value per goroutine
			defer func() {
				p := recover()
				if p != nil {
					if !waitAll {
						cancel()
					}
					errCh <- fmt.Errorf("service panicked: %v", p)
				}
			}()

			// Run the service
			rErr := service(ctx)

			// Ignore context canceled errors here as they generally indicate that the service is stopping.
			if rErr != nil && !errors.Is(rErr, context.Canceled) {
				errCh <- rErr
				return
			}
			errCh <- nil
		}(service)
	}

	// Wait for all services to return
	// As soon as the first service returns (with an error or cleanly) cancel the context so the remaining services stop too (unless WaitAll is true)
	errs := make([]error, 0)
	for range len(r.services) {
		err := <-errCh
		if !waitAll {
			cancel()
		}
		if err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}
