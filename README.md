# go-kit

A collection of utility packages for Go applications.

[![Go Reference](https://pkg.go.dev/badge/github.com/italypaleale/go-kit.svg)](https://pkg.go.dev/github.com/italypaleale/go-kit)

## Packages

- **eventqueue**: A queue processor for delayed and scheduled events. Uses a binary heap for O(log N) operations, allowing you to enqueue items with a scheduled execution time and have them processed automatically when due.
- **fsnotify**: Watches a filesystem folder for changes and batches notifications. Monitors for file create and write events, batching rapid changes within 500ms to avoid excessive notifications during bulk operations.
- **httpserver**: Utilities for HTTP servers using the standard library. It includes a collection of middlewares and utilities for returning JSON-formatted responses and errors.
- **servicerunner**: Manages multiple background services running concurrently. Runs services in parallel goroutines and cancels all services if any one returns an error, collecting and returning all errors together.
- **signals**: Graceful shutdown handling via OS signals. Returns a context that cancels on SIGINT or SIGTERM, with a second signal forcing immediate termination.
- **slog**: Utilities for the standard library's structured logging package (`log/slog`)
- **ttlcache**: An efficient generic cache with TTL (time-to-live) expiration. Provides concurrent access via HaxMap with automatic background garbage collection of expired items.

## Documentation

For detailed API documentation, see [pkg.go.dev](https://pkg.go.dev/github.com/italypaleale/go-kit).

## License

MIT License - see [LICENSE.txt](LICENSE.txt) for details.
