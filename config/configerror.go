package config

import (
	"errors"
	"fmt"
	"log/slog"

	slogkit "github.com/italypaleale/go-kit/slog"
)

// ConfigError is a configuration error
type ConfigError struct {
	err string
	msg string
}

// NewConfigError returns a new ConfigError.
// The err argument can be a string or an error.
func NewConfigError(err any, msg string) *ConfigError {
	var errStr string
	switch x := err.(type) {
	case error:
		errStr = x.Error()
	case string:
		errStr = x
	case fmt.Stringer:
		errStr = x.String()
	case nil:
		errStr = ""
	default:
		// Indicates a development-time error
		panic("Invalid type for parameter 'err'")
	}
	return &ConfigError{
		err: errStr,
		msg: msg,
	}
}

// Error implements the error interface
func (e ConfigError) Error() string {
	return e.err + ": " + e.msg
}

// LogFatal causes a fatal log
func (e ConfigError) LogFatal(log *slog.Logger) {
	slogkit.FatalError(log, e.msg, errors.New(e.err))
}
