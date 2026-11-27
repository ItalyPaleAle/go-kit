//go:build unix

package utils

import (
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileExistsRejectsFIFO(t *testing.T) {
	fifoPath := filepath.Join(t.TempDir(), "pipe.fifo")
	err := syscall.Mkfifo(fifoPath, 0o600)
	require.NoError(t, err)

	exists, err := FileExists(fifoPath)
	require.NoError(t, err)
	assert.False(t, exists)
}
