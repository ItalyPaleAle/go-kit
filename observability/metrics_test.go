package observability

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

const captureMetricReaderName = "test-capture"

// testMetricReader is the reader created for the last meter provider that InitMetrics created in tests
// A ManualReader can only be registered with one provider, so the factory creates a new one every time
// autoexport panics on duplicate registrations, so the factory is registered once for the whole package
var testMetricReader *metric.ManualReader

func init() {
	autoexport.RegisterMetricReader(captureMetricReaderName, func(context.Context) (metric.Reader, error) {
		testMetricReader = metric.NewManualReader()
		return testMetricReader, nil
	})
}

func TestInitMetricsSetsGlobalProvider(t *testing.T) {
	t.Setenv("OTEL_METRICS_EXPORTER", captureMetricReaderName)

	meter, shutdownFn, err := InitMetrics(t.Context(), InitMetricsOpts{
		Config:  testConfig{},
		AppName: "test-app",
		Prefix:  "test",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = shutdownFn(context.Background()) })

	// Instruments from the returned meter and from the global provider, which libraries like otelhttp use, must both report through the configured reader
	counter, err := meter.Int64Counter("custom.count")
	require.NoError(t, err)
	counter.Add(t.Context(), 1)

	globalCounter, err := otel.GetMeterProvider().Meter("library").Int64Counter("library.count")
	require.NoError(t, err)
	globalCounter.Add(t.Context(), 1)

	var rm metricdata.ResourceMetrics
	err = testMetricReader.Collect(t.Context(), &rm)
	require.NoError(t, err)

	var names []string
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			names = append(names, m.Name)
		}
	}
	require.ElementsMatch(t, []string{"custom.count", "library.count"}, names)
}
