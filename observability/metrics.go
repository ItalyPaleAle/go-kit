package observability

import (
	"context"
	"fmt"
	"os"

	"go.opentelemetry.io/contrib/exporters/autoexport"
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

// InitMetrics initializes metrics using OpenTelemetry.
// The returned meter can be used to add additional metrics tracked by the applictaion.
func InitMetrics(ctx context.Context, opts InitMetricsOpts) (meter api.Meter, shutdownFn func(ctx context.Context) error, err error) {
	resource, err := opts.Config.GetOtelResource(opts.AppName)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get OpenTelemetry resource: %w", err)
	}

	// Get the metric reader
	// If the env var OTEL_METRICS_EXPORTER is empty, we set it to "none"
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
	meter = mp.Meter(opts.Prefix)

	return meter, mp.Shutdown, nil
}
