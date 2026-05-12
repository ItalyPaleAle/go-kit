# go-kit

A collection of utility packages for Go applications.

[![Go Reference](https://pkg.go.dev/badge/github.com/italypaleale/go-kit.svg)](https://pkg.go.dev/github.com/italypaleale/go-kit)

> ⚠️ Packages in this repository are un-versioned do not expose stable APIs. Interfaces may change at any time and for any reason.

## Packages

- **config**: Utilities for loading YAML configuration files, and exposing shared application metadata such as instance IDs and OpenTelemetry resources.
- **emailer**: Send emails using one of the supported providers.
- **eventqueue**: A queue processor for delayed and scheduled events. Uses a binary heap for O(log N) operations, allowing you to enqueue items with a scheduled execution time and have them processed automatically when due.
- **fsnotify**: Watches a filesystem folder for changes and batches notifications. Monitors for file create and write events, batching rapid changes within 500ms to avoid excessive notifications during bulk operations.
- **httpserver**: Utilities for HTTP servers using the standard library. It includes a collection of middlewares and utilities for returning JSON-formatted responses and errors.
- **httpserver/tlsconfig**: Helpers for loading TLS certificates from PEM values or disk and hot-reloading them when certificate files change.
- **iputils**: IP address helpers, including detection of private, loopback, link-local, and other non-routable addresses.
- **observability**: OpenTelemetry setup helpers for logs, metrics, and traces, with integration for the standard library's `log/slog` package.
- **servicerunner**: Manages multiple background services running concurrently. Runs services in parallel goroutines and cancels all services if any one returns an error, collecting and returning all errors together.
- **signals**: Graceful shutdown handling via OS signals. Returns a context that cancels on SIGINT or SIGTERM, with a second signal forcing immediate termination.
- **slog**: Utilities for the standard library's structured logging package (`log/slog`)
- **testutils**: Test helpers and doubles used by the repository's unit tests, including an HTTP round tripper stub.
- **tsnetserver**: A wrapper around Tailscale's `tsnet.Server` for starting listeners, handling Funnel requests, and resolving peer identity.
- **ttlcache**: An efficient generic cache with TTL (time-to-live) expiration. Provides concurrent access via HaxMap with automatic background garbage collection of expired items.
- **utils**: Small general-purpose helpers.
- **webhook**: Webhook client utilities for plain-text and Slack-compatible payloads, with retries, OpenTelemetry transport instrumentation, and SSRF protections.

## Tools

- **gen-config**: Generates sample YAML and Markdown documentation from a config struct and can update the README config table.

## Documentation

For detailed API documentation, see [pkg.go.dev](https://pkg.go.dev/github.com/italypaleale/go-kit).

## License

MIT License - see [LICENSE.txt](LICENSE.txt) for details.
