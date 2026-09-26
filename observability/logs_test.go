package observability

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/contrib/exporters/autoexport"
	logSdk "go.opentelemetry.io/otel/sdk/log"
)

const captureLogExporterName = "test-capture"

// testLogExporter collects the records exported by the logger providers that InitLogs creates in tests
// autoexport panics on duplicate registrations, so the exporter is registered once for the whole package
var testLogExporter = &captureLogExporter{}

func init() {
	autoexport.RegisterLogExporter(captureLogExporterName, func(context.Context) (logSdk.Exporter, error) {
		return testLogExporter, nil
	})
}

// captureLogExporter is a logSdk.Exporter that keeps the body of every exported record
type captureLogExporter struct {
	lock   sync.Mutex
	bodies []string
}

func (e *captureLogExporter) Export(_ context.Context, records []logSdk.Record) error {
	e.lock.Lock()
	defer e.lock.Unlock()

	for _, r := range records {
		e.bodies = append(e.bodies, r.Body().AsString())
	}

	return nil
}

func (e *captureLogExporter) Shutdown(context.Context) error {
	return nil
}

func (e *captureLogExporter) ForceFlush(context.Context) error {
	return nil
}

func (e *captureLogExporter) Reset() {
	e.lock.Lock()
	defer e.lock.Unlock()

	e.bodies = nil
}

func (e *captureLogExporter) Bodies() []string {
	e.lock.Lock()
	defer e.lock.Unlock()

	return append([]string(nil), e.bodies...)
}

func TestInitLogsLevel(t *testing.T) {
	t.Setenv("OTEL_LOGS_EXPORTER", captureLogExporterName)
	testLogExporter.Reset()

	out, err := os.Create(filepath.Join(t.TempDir(), "out.log"))
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = out.Close()
	})

	log, shutdownFn, err := InitLogs(t.Context(), InitLogsOpts{
		Level:   "warn",
		JSON:    true,
		Config:  testConfig{},
		AppName: "test-app",
		Writer:  out,
	})
	require.NoError(t, err)

	log.Debug("debug message")
	log.Info("info message")
	log.Warn("warn message")
	log.Error("error message")

	// Shutting down flushes the batch processor
	err = shutdownFn(t.Context())
	require.NoError(t, err)

	// Both destinations must drop records below the configured level and keep the others
	written, err := os.ReadFile(out.Name())
	require.NoError(t, err)
	require.NotContains(t, string(written), "debug message")
	require.NotContains(t, string(written), "info message")
	require.Contains(t, string(written), "warn message")
	require.Contains(t, string(written), "error message")

	require.Equal(t, []string{"warn message", "error message"}, testLogExporter.Bodies())
}
