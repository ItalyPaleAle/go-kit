package observability

import (
	"context"
	"errors"
	"fmt"

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
	if opts.Config == nil {
		return nil, nil, errors.New("opts.Config is required")
	}

	resource, err := opts.Config.GetOtelResource(opts.AppName)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get OpenTelemetry resource: %w", err)
	}

	// Default to no-op when OTEL_METRICS_EXPORTER is unset
	mr, err := autoexport.NewMetricReader(
		ctx,
		autoexport.WithFallbackMetricReader(func(context.Context) (metric.Reader, error) {
			return nil, nil
		}),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to initialize OpenTelemetry metric reader: %w", err)
	}

	mpOpts := []metric.Option{
		metric.WithResource(resource),
	}
	if mr != nil && !autoexport.IsNoneMetricReader(mr) {
		mpOpts = append(mpOpts, metric.WithReader(mr))
	}

	mp := metric.NewMeterProvider(mpOpts...)
	otel.SetMeterProvider(mp)
	meter = mp.Meter(opts.Prefix)

	return meter, mp.Shutdown, nil
}
