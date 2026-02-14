package observability

import (
	"context"
	"fmt"
	"os"

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
	resource, err := opts.Config.GetOtelResource(opts.AppName)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get OpenTelemetry resource: %w", err)
	}

	// Get the trace exporter
	// If the env var OTEL_TRACES_EXPORTER is empty, we set it to "none"
	if os.Getenv("OTEL_TRACES_EXPORTER") == "" {
		os.Setenv("OTEL_TRACES_EXPORTER", "none")
	}
	exporter, err := autoexport.NewSpanExporter(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to initialize OpenTelemetry span exporter: %w", err)
	}
	shutdownFn = exporter.Shutdown

	// Init the trace provider
	tracerOpts := []sdkTrace.TracerProviderOption{
		sdkTrace.WithResource(resource),
		sdkTrace.WithBatcher(exporter),
	}
	if opts.Sampler != nil {
		tracerOpts = append(tracerOpts, sdkTrace.WithSampler(opts.Sampler))
	}

	traceProvider = sdkTrace.NewTracerProvider(tracerOpts...)
	otel.SetTracerProvider(traceProvider)
	otel.SetTextMapPropagator(
		propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}),
	)

	return traceProvider, shutdownFn, nil
}
