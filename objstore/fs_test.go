package objstore

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	_ Backend                 = (*FSBackend)(nil)
	_ DirectUploadBackend     = (*FSBackend)(nil)
	_ PublicAddressingBackend = (*FSBackend)(nil)
)

func TestFSBackend(t *testing.T) {
	newBackend := func(t *testing.T) (*FSBackend, string) {
		t.Helper()

		dir := t.TempDir()
		backend, err := NewFS(filepath.Join(dir, "objects"))
		require.NoError(t, err)

		return backend, filepath.Join(dir, "objects")
	}

	t.Run("creates the root folder", func(t *testing.T) {
		_, root := newBackend(t)
		assert.DirExists(t, root)
	})

	t.Run("requires a path", func(t *testing.T) {
		_, err := NewFS("   ")
		require.Error(t, err)
	})

	t.Run("writes and reads an object", func(t *testing.T) {
		backend, root := newBackend(t)

		result, err := backend.Put(t.Context(), "wf1/200x200.webp", strings.NewReader("thumbnail"), 1024, "image/webp")
		require.NoError(t, err)
		assert.Equal(t, int64(9), result.Size)
		// Files on disk have no URL of their own
		assert.Empty(t, result.URL)

		content, err := os.ReadFile(filepath.Join(root, "wf1", "200x200.webp")) //nolint:gosec // The path is inside a temporary folder this test created
		require.NoError(t, err)
		assert.Equal(t, "thumbnail", string(content))

		body, info, err := backend.Get(t.Context(), "wf1/200x200.webp")
		require.NoError(t, err)
		defer body.Close() //nolint:errcheck
		assert.Equal(t, int64(9), info.Size)
		// The content type is derived from the extension, since that's all the file system records
		assert.Contains(t, info.ContentType, "image/webp")

		read, err := io.ReadAll(body)
		require.NoError(t, err)
		assert.Equal(t, "thumbnail", string(read))
	})

	// A retried write regenerates the same object, so writing it twice must be allowed
	t.Run("overwrites an object that exists already", func(t *testing.T) {
		backend, _ := newBackend(t)

		_, err := backend.Put(t.Context(), "wf1/200x200.webp", strings.NewReader("first"), 1024, "image/webp")
		require.NoError(t, err)
		_, err = backend.Put(t.Context(), "wf1/200x200.webp", strings.NewReader("second"), 1024, "image/webp")
		require.NoError(t, err)

		body, _, err := backend.Get(t.Context(), "wf1/200x200.webp")
		require.NoError(t, err)
		defer body.Close() //nolint:errcheck
		read, err := io.ReadAll(body)
		require.NoError(t, err)
		assert.Equal(t, "second", string(read))
	})

	t.Run("rejects an object that is too large", func(t *testing.T) {
		backend, root := newBackend(t)

		_, err := backend.Put(t.Context(), "wf1.jpg", strings.NewReader("image-data"), 4, "image/jpeg")
		require.ErrorIs(t, err, ErrTooLarge)

		// The rename never happens, so a rejected write leaves nothing behind, not even a temporary file
		assert.NoFileExists(t, filepath.Join(root, "wf1.jpg"))
		entries, err := os.ReadDir(root)
		require.NoError(t, err)
		assert.Empty(t, entries)
	})

	t.Run("reports a missing object", func(t *testing.T) {
		backend, _ := newBackend(t)

		_, _, err := backend.Get(t.Context(), "wf1.jpg")
		require.ErrorIs(t, err, ErrNotFound)

		_, _, err = backend.GetConsistent(t.Context(), "wf1.jpg")
		require.ErrorIs(t, err, ErrNotFound)
	})

	// Nothing caches a read of the local file system, so a consistent read is the same read
	t.Run("reads an object consistently", func(t *testing.T) {
		backend, _ := newBackend(t)

		_, err := backend.Put(t.Context(), "wf1.jpg", strings.NewReader("image"), 1024, "image/jpeg")
		require.NoError(t, err)

		body, _, err := backend.GetConsistent(t.Context(), "wf1.jpg")
		require.NoError(t, err)
		defer body.Close() //nolint:errcheck
		read, err := io.ReadAll(body)
		require.NoError(t, err)
		assert.Equal(t, "image", string(read))
	})

	t.Run("reports whether an object exists", func(t *testing.T) {
		backend, _ := newBackend(t)

		exists, err := backend.Exists(t.Context(), "wf1.jpg")
		require.NoError(t, err)
		assert.False(t, exists)

		_, err = backend.Put(t.Context(), "wf1.jpg", strings.NewReader("image"), 1024, "image/jpeg")
		require.NoError(t, err)

		exists, err = backend.Exists(t.Context(), "wf1.jpg")
		require.NoError(t, err)
		assert.True(t, exists)
	})

	// Deletes are idempotent because the operation that failed may have been retried
	t.Run("deletes an object, and ignores one that is already gone", func(t *testing.T) {
		backend, root := newBackend(t)

		_, err := backend.Put(t.Context(), "wf1.jpg", strings.NewReader("image"), 1024, "image/jpeg")
		require.NoError(t, err)

		require.NoError(t, backend.Delete(t.Context(), "wf1.jpg"))
		assert.NoFileExists(t, filepath.Join(root, "wf1.jpg"))
		require.NoError(t, backend.Delete(t.Context(), "wf1.jpg"))
	})

	t.Run("rejects a key that escapes the root folder", func(t *testing.T) {
		backend, _ := newBackend(t)

		_, err := backend.Put(t.Context(), "../escape.jpg", strings.NewReader("image"), 1024, "image/jpeg")
		require.Error(t, err)

		_, _, err = backend.Get(t.Context(), "../escape.jpg")
		require.Error(t, err)

		require.Error(t, backend.Delete(t.Context(), "/absolute.jpg"))
	})

	t.Run("reports a canceled context", func(t *testing.T) {
		backend, _ := newBackend(t)

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err := backend.Put(ctx, "wf1.jpg", strings.NewReader("image"), 1024, "image/jpeg")
		require.ErrorIs(t, err, context.Canceled)

		_, _, err = backend.Get(ctx, "wf1.jpg")
		require.ErrorIs(t, err, context.Canceled)

		_, err = backend.Exists(ctx, "wf1.jpg")
		require.ErrorIs(t, err, context.Canceled)

		require.ErrorIs(t, backend.Delete(ctx, "wf1.jpg"), context.Canceled)
	})

	t.Run("has no capability beyond storing objects", func(t *testing.T) {
		backend, _ := newBackend(t)

		assert.False(t, backend.SupportsDirectUpload())
		assert.False(t, backend.HasPublicAddressing())

		_, err := backend.CreateDirectUpload(t.Context(), "wf1.jpg", 1024, "image/jpeg", 0)
		require.ErrorIs(t, err, ErrDirectUploadUnsupported)
	})

	t.Run("describes itself without leaking anything", func(t *testing.T) {
		backend, root := newBackend(t)
		assert.Contains(t, backend.Description(), root)
	})
}

func TestSpoolBoundedRejectsOversizeInput(t *testing.T) {
	file, _, err := spoolBounded(strings.NewReader("12345"), 4)
	assert.Nil(t, file)
	require.ErrorIs(t, err, ErrTooLarge)
}

func TestSpoolBoundedRemovesTheFileItCreated(t *testing.T) {
	file, size, err := spoolBounded(strings.NewReader("12345"), 8)
	require.NoError(t, err)
	assert.Equal(t, int64(5), size)

	name := file.Name()
	assert.FileExists(t, name)

	closeAndRemove(file)
	_, err = os.Stat(name)
	assert.ErrorIs(t, err, os.ErrNotExist)
}
