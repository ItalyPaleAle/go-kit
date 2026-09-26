package observability

import (
	"context"
	"fmt"
	"os"

	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/otel"
	api "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/sdk/metric"

	kitconfig "github.com/italypaleale/go-kit/config"
)

// InitMetricsOpts contains options for the InitMetrics method
type InitMetricsOpts struct {
	Config  kitconfig.Base
	AppName string
	Prefix  string
}

// InitMetrics initializes the meter provider using OpenTelemetry and sets it as the global one, which instrumentation libraries such as otelhttp use
// The returned meter can be used to create the application's own metrics
func InitMetrics(ctx context.Context, opts InitMetricsOpts) (meter api.Meter, shutdownFn func(ctx context.Context) error, err error) {
	resource, err := opts.Config.GetOtelResource(opts.AppName)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get OpenTelemetry resource: %w", err)
	}

	// Get the metric reader
	// autoexport defaults to OTLP when OTEL_METRICS_EXPORTER is empty, so set it to "none" to make exporting metrics opt-in
	if os.Getenv("OTEL_METRICS_EXPORTER") == "" {
		_ = os.Setenv("OTEL_METRICS_EXPORTER", "none") //nolint:errcheck
	}
	mr, err := autoexport.NewMetricReader(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to initialize OpenTelemetry metric reader: %w", err)
	}

	mp := metric.NewMeterProvider(
		metric.WithResource(resource),
		metric.WithReader(mr),
	)
	otel.SetMeterProvider(mp)
	meter = mp.Meter(opts.Prefix)

	return meter, mp.Shutdown, nil
}
