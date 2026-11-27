package slog

import (
	"bytes"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLogFatalRecordHandlesNilLogger(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() {
		slog.SetDefault(prev)
	})

	logFatalRecord(nil, "startup failed", errors.New("bad config"), 2)
	flushTelemetry()

	assert.Contains(t, buf.String(), "startup failed")
	assert.Contains(t, buf.String(), "bad config")
}
