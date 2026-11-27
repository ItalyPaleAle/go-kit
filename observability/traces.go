package observability

import (
	"context"
	"errors"
	"fmt"

	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdkTrace "go.opentelemetry.io/otel/sdk/trace"

	kitconfig "github.com/italypaleale/go-kit/config"
)

// InitTracesOpts contains options for the InitTraces method
type InitTracesOpts struct {
	Config  kitconfig.Base
	AppName string
	Sampler sdkTrace.Sampler
}

// InitTraces initializes the tracing provider using OpenTelemetry
func InitTraces(ctx context.Context, opts InitTracesOpts) (traceProvider *sdkTrace.TracerProvider, shutdownFn func(ctx context.Context) error, err error) {
	if opts.Config == nil {
		return nil, nil, errors.New("opts.Config is required")
	}

	resource, err := opts.Config.GetOtelResource(opts.AppName)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get OpenTelemetry resource: %w", err)
	}

	// Default to no-op when OTEL_TRACES_EXPORTER is unset
	exporter, err := autoexport.NewSpanExporter(
		ctx,
		autoexport.WithFallbackSpanExporter(func(context.Context) (sdkTrace.SpanExporter, error) {
			return nil, nil
		}),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to initialize OpenTelemetry span exporter: %w", err)
	}

	// Init the trace provider
	tracerOpts := []sdkTrace.TracerProviderOption{
		sdkTrace.WithResource(resource),
	}
	if exporter != nil && !autoexport.IsNoneSpanExporter(exporter) {
		tracerOpts = append(tracerOpts, sdkTrace.WithBatcher(exporter))
	}
	if opts.Sampler != nil {
		tracerOpts = append(tracerOpts, sdkTrace.WithSampler(opts.Sampler))
	}

	traceProvider = sdkTrace.NewTracerProvider(tracerOpts...)
	otel.SetTracerProvider(traceProvider)
	otel.SetTextMapPropagator(
		propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}),
	)

	// Return the provider's Shutdown
	shutdownFn = traceProvider.Shutdown

	return traceProvider, shutdownFn, nil
}
