// Package servicerunner manages multiple services (functions that implement [Service]) running concurrently in the background.
// When any service returns, whether with an error or not, the others are canceled via the context so the group shuts down together.
// The runner waits for all services to complete and returns any errors joined together.
package servicerunner

import (
	"context"
	"errors"
)

// Service is a background service
type Service func(ctx context.Context) error

// ServiceRunner oversees a number of services running in background
type ServiceRunner struct {
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

	errCh := make(chan error)
	for _, service := range r.services {
		go func(service Service) {
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
	// As soon as the first service returns (with an error or cleanly) cancel the context so the remaining services stop too
	errs := make([]error, 0)
	for range len(r.services) {
		err := <-errCh
		cancel()
		if err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}
