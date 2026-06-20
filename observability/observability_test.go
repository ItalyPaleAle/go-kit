package observability

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/resource"
	sdkTrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	kitconfig "github.com/italypaleale/go-kit/config"
)

// testConfig is a minimal kitconfig.Base implementation for unit tests
type testConfig struct{}

func (testConfig) GetLoadedConfigPath() string {
	return ""
}

func (testConfig) SetLoadedConfigPath(_ string) {
	// Nop
}

func (testConfig) GetInstanceID() string {
	return "test"
}

func (testConfig) GetOtelResource(_ string) (*resource.Resource, error) {
	return resource.Default(), nil
}

var _ kitconfig.Base = testConfig{}

// TestTracerProviderShutdownFlushesSpans verifies the shutdown pattern used by InitTraces
// Batched spans must reach the exporter when the provider is flushed before shutdown
func TestTracerProviderShutdownFlushesSpans(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()

	// WithBatcher mirrors how InitTraces registers the exporter
	tp := sdkTrace.NewTracerProvider(sdkTrace.WithBatcher(exporter))

	tracer := tp.Tracer("test")
	_, span := tracer.Start(t.Context(), "span-before-flush")
	span.End()

	// Span is still in the batch
	// Exporter should not have received it yet
	require.Empty(t, exporter.GetSpans())

	// ForceFlush drains the batch synchronously (what Shutdown calls before tearing down)
	err := tp.ForceFlush(t.Context())
	require.NoError(t, err)

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	require.Equal(t, "span-before-flush", spans[0].Name)

	err = tp.Shutdown(t.Context())
	require.NoError(t, err)
}

func TestInitTracesShutdown(t *testing.T) {
	t.Setenv("OTEL_TRACES_EXPORTER", "none")

	_, shutdownFn, err := InitTraces(t.Context(), InitTracesOpts{
		Config:  testConfig{},
		AppName: "test-app",
	})
	require.NoError(t, err)
	require.NotNil(t, shutdownFn)

	err = shutdownFn(t.Context())
	require.NoError(t, err)
}

func TestInitMetricsShutdown(t *testing.T) {
	t.Setenv("OTEL_METRICS_EXPORTER", "none")

	_, shutdownFn, err := InitMetrics(t.Context(), InitMetricsOpts{
		Config:  testConfig{},
		AppName: "test-app",
	})
	require.NoError(t, err)
	require.NotNil(t, shutdownFn)

	err = shutdownFn(t.Context())
	require.NoError(t, err)
}

func TestInitLogsShutdown(t *testing.T) {
	t.Setenv("OTEL_LOGS_EXPORTER", "none")

	_, shutdownFn, err := InitLogs(t.Context(), InitLogsOpts{
		Config:  testConfig{},
		AppName: "test-app",
		JSON:    true, // avoid TTY detection in CI
	})
	require.NoError(t, err)
	require.NotNil(t, shutdownFn)

	err = shutdownFn(t.Context())
	require.NoError(t, err)
}
