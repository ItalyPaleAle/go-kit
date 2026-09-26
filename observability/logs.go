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

// GetLogLevel returns the parsed slog level
// Defaults to "info" if empty
func GetLogLevel(level string) (slog.Level, error) {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info": // Also default log level
		return slog.LevelInfo, nil
	case "warn", "warning":
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
	// If true, logs as JSON instead of text
	JSON bool

	Config     kitconfig.Base
	AppName    string
	AppVersion string

	// Writer for text and JSON logs
	// If nil, defaults to stdout
	Writer *os.File
}

// InitLogs initializes a new slog logger that also sends logs to OpenTelemetry, if an exporter is configured
// Records below the configured level are dropped for both destinations
func InitLogs(ctx context.Context, opts InitLogsOpts) (log *slog.Logger, shutdownFn func(ctx context.Context) error, err error) {
	level, err := GetLogLevel(opts.Level)
	if err != nil {
		return nil, nil, err
	}

	writer := opts.Writer
	if writer == nil {
		writer = os.Stdout
	}

	// Create the handler
	var handler slog.Handler
	if opts.JSON {
		// Log as JSON if configured
		handler = slog.NewJSONHandler(writer, &slog.HandlerOptions{
			Level: level,
		})
	} else {
		// Enable colors if we have a TTY
		handler = tint.NewTextHandler(writer, &tint.Options{
			Level:      level,
			TimeFormat: time.StampMilli,
			NoColor:    !isatty.IsTerminal(writer.Fd()),
		})
	}

	// Create a handler that sends logs to OTel too
	// We wrap the handler in a "fanout" handler that sends logs to both
	resource, err := opts.Config.GetOtelResource(opts.AppName)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get OpenTelemetry resource: %w", err)
	}

	// autoexport defaults to OTLP when OTEL_LOGS_EXPORTER is empty, so set it to "none" to make exporting logs opt-in
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
		levelHandler{
			level:   level,
			handler: otelslog.NewHandler(opts.AppName, otelslog.WithLoggerProvider(provider)),
		},
	)

	// Return a function to invoke during shutdown
	shutdownFn = provider.Shutdown

	log = slog.New(handler).
		With(slog.String("app", opts.AppName)).
		With(slog.String("version", opts.AppVersion))

	return log, shutdownFn, nil
}

// levelHandler wraps a slog.Handler and drops records below a minimum level
type levelHandler struct {
	level   slog.Leveler
	handler slog.Handler
}

func (h levelHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= h.level.Level() && h.handler.Enabled(ctx, level)
}

func (h levelHandler) Handle(ctx context.Context, r slog.Record) error {
	return h.handler.Handle(ctx, r)
}

func (h levelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return levelHandler{
		level:   h.level,
		handler: h.handler.WithAttrs(attrs),
	}
}

func (h levelHandler) WithGroup(name string) slog.Handler {
	return levelHandler{
		level:   h.level,
		handler: h.handler.WithGroup(name),
	}
}
