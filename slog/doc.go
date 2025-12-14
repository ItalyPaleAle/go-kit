// Package slog provides utilities for working with the standard library's log/slog package.
//
// The package exports the following main components:
//
//   - FatalError: Logs an error message and terminates the application with exit code 1.
//     Useful for unrecoverable errors during startup or critical failures.
//   - LogFanoutHandler: A slog.Handler implementation that sends log records to multiple handlers simultaneously.
//     This enables logging to multiple destinations (eg. stdout and a file or to an OpenTelemetry exporter) with a single logger instance.
package slog
