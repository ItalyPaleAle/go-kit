package observability

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/lmittmann/tint"
	"github.com/mattn/go-isatty"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/exporters/autoexport"
	logGlobal "go.opentelemetry.io/otel/log/global"
	logSdk "go.opentelemetry.io/otel/sdk/log"

	kitconfig "github.com/italypaleale/go-kit/config"
)

func getLogLevel(level string) (slog.Level, error) {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info": // Also default log level
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, kitconfig.NewConfigError("Invalid value for 'logLevel'", "Invalid configuration")
	}
}

// InitLogsOpts contains options for the InitLogs method
type InitLogsOpts struct {
	// Log level: "debug", "info", "warn", "error", or an empty string (defaults to "info")
	Level string
	// If true, logs as JSON by default
	JSON bool

	Config     kitconfig.Base
	AppName    string
	AppVersion string
}

// InitLogs initializes a new slog logger and configures it using OpenTelemetry if needed.
func InitLogs(ctx context.Context, opts InitLogsOpts) (log *slog.Logger, shutdownFn func(ctx context.Context) error, err error) {
	// Get the level
	level, err := getLogLevel(opts.Level)
	if err != nil {
		return nil, nil, err
	}

	// Create the handler
	var handler slog.Handler
	switch {
	case opts.JSON:
		// Log as JSON if configured
		handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			Level: level,
		})
	case isatty.IsTerminal(os.Stdout.Fd()):
		// Enable colors if we have a TTY
		handler = tint.NewHandler(os.Stdout, &tint.Options{
			Level:      level,
			TimeFormat: time.StampMilli,
		})
	default:
		handler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
			Level: level,
		})
	}

	// Create a handler that sends logs to OTel too
	// We wrap the handler in a "fanout" handler that sends logs to both
	resource, err := opts.Config.GetOtelResource(opts.AppName)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get OpenTelemetry resource: %w", err)
	}

	// If the env var OTEL_LOGS_EXPORTER is empty, we set it to "none"
	if os.Getenv("OTEL_LOGS_EXPORTER") == "" {
		_ = os.Setenv("OTEL_LOGS_EXPORTER", "none") //nolint:errcheck
	}
	exp, err := autoexport.NewLogExporter(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to initialize OpenTelemetry log exporter: %w", err)
	}

	// Create the logger provider
	provider := logSdk.NewLoggerProvider(
		logSdk.WithProcessor(
			logSdk.NewBatchProcessor(exp),
		),
		logSdk.WithResource(resource),
	)

	// Set the logger provider globally
	logGlobal.SetLoggerProvider(provider)

	// Wrap the handler in a MultiHandler for fanout
	handler = slog.NewMultiHandler(
		handler,
		otelslog.NewHandler(opts.AppName, otelslog.WithLoggerProvider(provider)),
	)

	// Return a function to invoke during shutdown
	shutdownFn = provider.Shutdown

	log = slog.New(handler).
		With(slog.String("app", opts.AppName)).
		With(slog.String("version", opts.AppVersion))

	return log, shutdownFn, nil
}
