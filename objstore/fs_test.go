package objstore

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
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

		err = backend.Delete(t.Context(), "wf1.jpg")
		require.NoError(t, err)
		assert.NoFileExists(t, filepath.Join(root, "wf1.jpg"))
		err = backend.Delete(t.Context(), "wf1.jpg")
		require.NoError(t, err)
	})

	t.Run("rejects a key that escapes the root folder", func(t *testing.T) {
		backend, _ := newBackend(t)

		_, err := backend.Put(t.Context(), "../escape.jpg", strings.NewReader("image"), 1024, "image/jpeg")
		require.Error(t, err)

		_, _, err = backend.Get(t.Context(), "../escape.jpg")
		require.Error(t, err)

		err = backend.Delete(t.Context(), "/absolute.jpg")
		require.Error(t, err)
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

		err = backend.Delete(ctx, "wf1.jpg")
		require.ErrorIs(t, err, context.Canceled)
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

func TestFSBackendPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no permission bits beyond read-only")
	}

	// Report the permissions of a file or folder
	modeOf := func(t *testing.T, path string) fs.FileMode {
		t.Helper()

		info, err := os.Stat(path)
		require.NoError(t, err)
		return info.Mode().Perm()
	}

	t.Run("the defaults keep objects to the process's user", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "objects")
		backend, err := New("fs:"+root, Options{})
		require.NoError(t, err)

		_, err = backend.Put(t.Context(), "a/b/200x200.webp", strings.NewReader("thumbnail"), 1024, "image/webp")
		require.NoError(t, err)

		assert.Equal(t, DefaultFSFileMode, modeOf(t, filepath.Join(root, "a", "b", "200x200.webp")))
		assert.Equal(t, DefaultFSDirMode, modeOf(t, filepath.Join(root, "a", "b")))
		assert.Equal(t, DefaultFSDirMode, modeOf(t, filepath.Join(root, "a")))
		assert.Equal(t, DefaultFSDirMode, modeOf(t, root))
		assert.NotContains(t, backend.Description(), "permissions")
	})

	// 0775 is more than the usual umask of 022 lets a new folder have, so this also shows the permissions are set rather than left to the umask
	t.Run("the DSN sets the permissions of files and folders", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "objects")
		backend, err := New("fs:"+root+"?fileMode=0664&dirMode=0775", Options{})
		require.NoError(t, err)
		assert.Contains(t, backend.Description(), "permissions 0664 for files and 0775 for folders")

		_, err = backend.Put(t.Context(), "a/b/200x200.webp", strings.NewReader("thumbnail"), 1024, "image/webp")
		require.NoError(t, err)

		assert.Equal(t, fs.FileMode(0o664), modeOf(t, filepath.Join(root, "a", "b", "200x200.webp")))
		assert.Equal(t, fs.FileMode(0o775), modeOf(t, filepath.Join(root, "a", "b")))
		assert.Equal(t, fs.FileMode(0o775), modeOf(t, filepath.Join(root, "a")))
		assert.Equal(t, fs.FileMode(0o775), modeOf(t, root))

		// A write replaces the file, so an overwritten object gets the permissions too
		path := filepath.Join(root, "a", "b", "200x200.webp")
		err = os.Chmod(path, 0o600)
		require.NoError(t, err)
		_, err = backend.Put(t.Context(), "a/b/200x200.webp", strings.NewReader("again"), 1024, "image/webp")
		require.NoError(t, err)
		assert.Equal(t, fs.FileMode(0o664), modeOf(t, path))
	})

	t.Run("a bare path and the options in code take them too", func(t *testing.T) {
		dir := t.TempDir()
		backend, err := New(filepath.Join(dir, "bare")+"?fileMode=0o640", Options{})
		require.NoError(t, err)
		_, err = backend.Put(t.Context(), "photo.jpg", strings.NewReader("image"), 1024, "image/jpeg")
		require.NoError(t, err)
		assert.Equal(t, fs.FileMode(0o640), modeOf(t, filepath.Join(dir, "bare", "photo.jpg")))
		// The folder isn't named after the options
		assert.NoDirExists(t, filepath.Join(dir, "bare?fileMode=0o640"))

		coded, err := NewFSWithOptions(filepath.Join(dir, "coded"), FSOptions{DirMode: 0o755})
		require.NoError(t, err)
		_, err = coded.Put(t.Context(), "a/photo.jpg", strings.NewReader("image"), 1024, "image/jpeg")
		require.NoError(t, err)
		assert.Equal(t, DefaultFSFileMode, modeOf(t, filepath.Join(dir, "coded", "a", "photo.jpg")))
		assert.Equal(t, fs.FileMode(0o755), modeOf(t, filepath.Join(dir, "coded", "a")))
	})

	// The root may be a mount point or a folder another tool manages, which the process may not even own
	t.Run("leaves the folders that already exist as they are", func(t *testing.T) {
		root := t.TempDir()
		err := os.Mkdir(filepath.Join(root, "a"), 0o700)
		require.NoError(t, err)
		err = os.Chmod(root, 0o700) //nolint:gosec // A folder needs the execute bit to be entered
		require.NoError(t, err)

		backend, err := New(root+"?dirMode=0755", Options{})
		require.NoError(t, err)
		_, err = backend.Put(t.Context(), "a/b/photo.jpg", strings.NewReader("image"), 1024, "image/jpeg")
		require.NoError(t, err)

		assert.Equal(t, fs.FileMode(0o700), modeOf(t, root))
		assert.Equal(t, fs.FileMode(0o700), modeOf(t, filepath.Join(root, "a")))
		assert.Equal(t, fs.FileMode(0o755), modeOf(t, filepath.Join(root, "a", "b")))
	})

	invalid := map[string]string{
		"an unknown option":            "?mode=0644",
		"a mode that isn't octal":      "?fileMode=0899",
		"a mode past 0777":             "?dirMode=01777",
		"an empty mode":                "?fileMode=",
		"a mode of zero":               "?fileMode=0000",
		"files the process can't read": "?fileMode=0244",
		"folders it can't enter":       "?dirMode=0655",
	}
	for name, options := range invalid {
		t.Run("refuses "+name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "objects")
			_, err := New("fs:"+root+options, Options{})
			require.Error(t, err)

			// Nothing is created for a DSN that's refused
			assert.NoDirExists(t, root)
		})
	}

	t.Run("refuses options with bits beyond the permissions", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "objects")
		_, err := NewFSWithOptions(root, FSOptions{DirMode: fs.ModeSetgid | 0o750})
		require.Error(t, err)
		assert.NoDirExists(t, root)
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
