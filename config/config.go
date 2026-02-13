package config

import (
	"go.opentelemetry.io/otel/sdk/resource"
)

// Base includes the list of methods that config objects are expected to implement
type Base interface {
	// GetLoadedConfigPath returns the path to the config file that was loaded
	GetLoadedConfigPath() string
	// SetLoadedConfigPath sets the path to the config file that was loaded.
	SetLoadedConfigPath(path string)
	// GetInstanceID returns the instance ID
	GetInstanceID() string
	// GetOtelResource returns the OpenTelemetry Resource object
	GetOtelResource(name string) (*resource.Resource, error)
}
