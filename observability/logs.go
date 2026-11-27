package observability

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/lmittmann/tint"
	"github.com/mattn/go-isatty"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/otel"
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
	if opts.Config == nil {
		return nil, nil, errors.New("opts.Config is required")
	}

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
	resource, err := opts.Config.GetOtelResource(opts.AppName)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get OpenTelemetry resource: %w", err)
	}

	// Default to no-op when OTEL_LOGS_EXPORTER is unset so exporting logs stays opt-in
	exp, err := autoexport.NewLogExporter(
		ctx,
		autoexport.WithFallbackLogExporter(func(context.Context) (logSdk.Exporter, error) {
			return nil, nil
		}),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to initialize OpenTelemetry log exporter: %w", err)
	}

	providerOpts := []logSdk.LoggerProviderOption{
		logSdk.WithResource(resource),
	}

	exportEnabled := (exp != nil && !autoexport.IsNoneLogExporter(exp))
	if exportEnabled {
		providerOpts = append(providerOpts, logSdk.WithProcessor(logSdk.NewBatchProcessor(exp)))
	}

	provider := logSdk.NewLoggerProvider(providerOpts...)
	otel.SetLoggerProvider(provider)

	if exportEnabled {
		// Wrap the handler in a MultiHandler for fanout when an OTel exporter is active
		handler = slog.NewMultiHandler(
			handler,
			levelHandler{
				level:   level,
				handler: otelslog.NewHandler(opts.AppName, otelslog.WithLoggerProvider(provider)),
			},
		)
	}

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
