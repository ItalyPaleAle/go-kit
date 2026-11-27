package utils

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileExists(t *testing.T) {
	dir := t.TempDir()

	exists, err := FileExists(filepath.Join(dir, "missing.txt"))
	require.NoError(t, err)
	assert.False(t, exists)

	exists, err = FileExists(dir)
	require.NoError(t, err)
	assert.False(t, exists)

	regFile := filepath.Join(dir, "regular.txt")
	err = os.WriteFile(regFile, []byte("ok"), 0o600)
	require.NoError(t, err)

	exists, err = FileExists(regFile)
	require.NoError(t, err)
	assert.True(t, exists)
}
