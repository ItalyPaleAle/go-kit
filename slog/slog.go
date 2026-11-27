package slog

import (
	"context"
	"log/slog"
	"os"
	"runtime"
	"time"

	"go.opentelemetry.io/otel"
)

type forceFlusher interface {
	ForceFlush(ctx context.Context) error
}

// FatalError emits a log at the error level and causes the application to exit
func FatalError(log *slog.Logger, message string, err error) {
	logFatalRecord(log, message, err, 3)
	flushTelemetry()
	os.Exit(1)
}

func logFatalRecord(log *slog.Logger, message string, err error, callerSkip int) {
	if log == nil {
		log = slog.Default()
	}

	// Emit the log only if the level is enabled
	if !log.Enabled(context.Background(), slog.LevelError) {
		return
	}

	// See https://pkg.go.dev/log/slog#example-package-Wrapping
	var pcs [1]uintptr
	runtime.Callers(callerSkip, pcs[:])
	r := slog.NewRecord(time.Now(), slog.LevelError, message, pcs[0])
	if err != nil {
		r.AddAttrs(slog.String("error", err.Error()))
	}
	_ = log.Handler().Handle(context.Background(), r)
}

func flushTelemetry() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	lp, ok := otel.GetLoggerProvider().(forceFlusher)
	if ok {
		_ = lp.ForceFlush(ctx)
	}
	tp, ok := otel.GetTracerProvider().(forceFlusher)
	if ok {
		_ = tp.ForceFlush(ctx)
	}
}
