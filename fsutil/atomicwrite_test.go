package fsutil

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Returns the names of the files in dir
func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	return names
}

// Returns the permission bits of the file at path
func filePerm(t *testing.T, path string) fs.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	return info.Mode().Perm()
}

func TestWriteFile(t *testing.T) {
	t.Run("Creates file with expected contents", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "file")

		err := WriteFile(path, []byte("hello world"), 0o600)
		require.NoError(t, err)

		got, err := os.ReadFile(path) //nolint:gosec
		require.NoError(t, err)
		assert.Equal(t, []byte("hello world"), got)
		assert.Equal(t, []string{"file"}, listDir(t, dir), "temp file should be removed")
	})

	t.Run("Sets the requested file mode, regardless of the umask", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("skipping permission check on Windows")
		}
		dir := t.TempDir()

		for _, perm := range []fs.FileMode{0o600, 0o640, 0o644, 0o400} {
			path := filepath.Join(dir, "file-"+strconv.FormatUint(uint64(perm), 8))
			err := WriteFile(path, []byte("x"), perm)
			require.NoError(t, err)
			assert.Equal(t, perm, filePerm(t, path))
		}
	})

	t.Run("Overwrites an existing file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "file")
		err := os.WriteFile(path, []byte("old"), 0o600)
		require.NoError(t, err)

		err = WriteFile(path, []byte("new"), 0o600)
		require.NoError(t, err)

		got, err := os.ReadFile(path) //nolint:gosec
		require.NoError(t, err)
		assert.Equal(t, []byte("new"), got)
		assert.Equal(t, []string{"file"}, listDir(t, dir), "temp file should be removed")
	})

	t.Run("Replaces a symlink instead of following it", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("creating symlinks requires extra privileges on Windows")
		}
		dir := t.TempDir()
		target := filepath.Join(dir, "target")
		err := os.WriteFile(target, []byte("target"), 0o600)
		require.NoError(t, err)
		link := filepath.Join(dir, "link")
		err = os.Symlink(target, link)
		require.NoError(t, err)

		err = WriteFile(link, []byte("new"), 0o600)
		require.NoError(t, err)

		info, err := os.Lstat(link)
		require.NoError(t, err)
		assert.True(t, info.Mode().IsRegular())
		got, err := os.ReadFile(target) //nolint:gosec
		require.NoError(t, err)
		assert.Equal(t, []byte("target"), got)
	})

	t.Run("Uses the temp file prefix", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "file")

		// Check the temp file prefix in the error message
		_, err := WriteFileFrom(path, iotest.ErrReader(errors.New("simulated")), 0o600, WithTempPrefix(".custom-"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), string(filepath.Separator)+".custom-")
		assert.Empty(t, listDir(t, dir))

		err = WriteFile(path, []byte("x"), 0o600, WithTempPrefix("../escape-"))
		require.ErrorContains(t, err, "invalid temporary file prefix")
		err = WriteFile(path, []byte("x"), 0o600, WithTempPrefix("sub/escape-"))
		require.ErrorContains(t, err, "invalid temporary file prefix")
	})

	t.Run("Leaves no temp files on failure", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "does-not-exist", "file")

		err := WriteFile(path, []byte("x"), 0o600)
		require.Error(t, err)
		assert.Empty(t, listDir(t, dir))
	})

	t.Run("Does not touch other files when the parent is not a directory", func(t *testing.T) {
		dir := t.TempDir()
		existing := filepath.Join(dir, "existing")
		err := os.WriteFile(existing, []byte("preserved"), 0o600)
		require.NoError(t, err)

		err = WriteFile(filepath.Join(existing, "file"), []byte("new"), 0o600)
		require.Error(t, err)

		got, err := os.ReadFile(existing) //nolint:gosec
		require.NoError(t, err)
		assert.Equal(t, []byte("preserved"), got)
	})
}

func TestWriteFileFrom(t *testing.T) {
	t.Run("Copies the reader and returns the size", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "file")

		n, err := WriteFileFrom(path, strings.NewReader("hello world"), 0o600)
		require.NoError(t, err)
		assert.Equal(t, int64(11), n)

		got, err := os.ReadFile(path) //nolint:gosec
		require.NoError(t, err)
		assert.Equal(t, []byte("hello world"), got)
	})

	t.Run("A reader error leaves the existing file untouched", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "file")
		err := os.WriteFile(path, []byte("old"), 0o600)
		require.NoError(t, err)

		errLimit := errors.New("too large")
		r := io.MultiReader(strings.NewReader("partial"), iotest.ErrReader(errLimit))
		n, err := WriteFileFrom(path, r, 0o600)
		require.ErrorIs(t, err, errLimit)
		assert.Equal(t, int64(0), n)

		got, err := os.ReadFile(path) //nolint:gosec
		require.NoError(t, err)
		assert.Equal(t, []byte("old"), got)
		assert.Equal(t, []string{"file"}, listDir(t, dir), "temp file should be removed")
	})
}

func TestCreateFile(t *testing.T) {
	t.Run("Creates file with expected contents and mode", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "file")

		err := CreateFile(path, []byte("hello world"), 0o640)
		require.NoError(t, err)

		got, err := os.ReadFile(path) //nolint:gosec
		require.NoError(t, err)
		assert.Equal(t, []byte("hello world"), got)
		assert.Equal(t, []string{"file"}, listDir(t, dir), "temp file should be removed")

		if runtime.GOOS != "windows" {
			assert.Equal(t, fs.FileMode(0o640), filePerm(t, path))
		}
	})

	t.Run("Does not overwrite an existing file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "file")
		err := os.WriteFile(path, []byte("old"), 0o600)
		require.NoError(t, err)

		err = CreateFile(path, []byte("new"), 0o600)
		require.ErrorIs(t, err, fs.ErrExist)

		got, err := os.ReadFile(path) //nolint:gosec
		require.NoError(t, err)
		assert.Equal(t, []byte("old"), got)
		assert.Equal(t, []string{"file"}, listDir(t, dir), "temp file should be removed")
	})

	t.Run("Does not replace an existing empty file by default", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "file")
		err := os.WriteFile(path, nil, 0o600)
		require.NoError(t, err)

		err = CreateFile(path, []byte("new"), 0o600)
		require.ErrorIs(t, err, fs.ErrExist)

		got, err := os.ReadFile(path) //nolint:gosec
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("Replaces an existing empty file with WithReplaceEmpty", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "file")
		err := os.WriteFile(path, nil, 0o600)
		require.NoError(t, err)

		err = CreateFile(path, []byte("new"), 0o600, WithReplaceEmpty())
		require.NoError(t, err)

		got, err := os.ReadFile(path) //nolint:gosec
		require.NoError(t, err)
		assert.Equal(t, []byte("new"), got)
		assert.Equal(t, []string{"file"}, listDir(t, dir), "temp file should be removed")
	})

	t.Run("WithReplaceEmpty does not replace a file with contents", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "file")
		err := os.WriteFile(path, []byte("old"), 0o600)
		require.NoError(t, err)

		err = CreateFile(path, []byte("new"), 0o600, WithReplaceEmpty())
		require.ErrorIs(t, err, fs.ErrExist)
	})

	t.Run("Only one of many concurrent writers succeeds", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "file")

		const writers = 20
		errs := make([]error, writers)
		var wg sync.WaitGroup
		for i := range writers {
			wg.Go(func() {
				errs[i] = CreateFile(path, []byte(strconv.Itoa(i)), 0o600)
			})
		}
		wg.Wait()

		winner := -1
		for i, err := range errs {
			if err == nil {
				require.Equal(t, -1, winner, "only one writer should succeed")
				winner = i
				continue
			}
			require.ErrorIs(t, err, fs.ErrExist)
		}
		require.NotEqual(t, -1, winner, "one writer should succeed")

		got, err := os.ReadFile(path) //nolint:gosec
		require.NoError(t, err)
		assert.Equal(t, strconv.Itoa(winner), string(got))
		assert.Equal(t, []string{"file"}, listDir(t, dir), "temp files should be removed")
	})

	t.Run("Leaves no temp files on failure", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "does-not-exist", "file")

		err := CreateFile(path, []byte("x"), 0o600)
		require.Error(t, err)
		assert.Empty(t, listDir(t, dir))
	})
}

func TestSyncDir(t *testing.T) {
	t.Run("Syncs an existing directory", func(t *testing.T) {
		err := SyncDir(t.TempDir())
		require.NoError(t, err)
	})

	t.Run("Fails for a directory that doesn't exist", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("SyncDir does nothing on Windows")
		}
		err := SyncDir(filepath.Join(t.TempDir(), "does-not-exist"))
		require.ErrorIs(t, err, fs.ErrNotExist)
	})
}
