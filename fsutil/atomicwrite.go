// Package fsutil contains helpers for working with files
package fsutil

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

// Option configures WriteFile, WriteFileFrom, and CreateFile
type Option func(*options)

type options struct {
	tempPrefix   string
	replaceEmpty bool
}

// WithTempPrefix sets the prefix for the temporary file name (must not contain path separators)
// Defaults to ".<filename>.tmp-"
func WithTempPrefix(prefix string) Option {
	return func(o *options) {
		o.tempPrefix = prefix
	}
}

// WithReplaceEmpty makes CreateFile overwrite an existing empty regular file
// Concurrent calls replacing the same empty file may all succeed (last write wins)
// Ignored by WriteFile and WriteFileFrom
func WithReplaceEmpty() Option {
	return func(o *options) {
		o.replaceEmpty = true
	}
}

func newOptions(path string, opts []Option) options {
	o := options{
		tempPrefix: "." + filepath.Base(path) + ".tmp-",
	}
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// WriteFile writes data to path atomically by writing to a temporary file in the same directory and renaming it into place
// Permissions are set to perm regardless of umask, and symlinks at path are replaced rather than followed
func WriteFile(path string, data []byte, perm fs.FileMode, opts ...Option) error {
	_, err := WriteFileFrom(path, bytes.NewReader(data), perm, opts...)
	return err
}

// WriteFileFrom is like WriteFile, but reads data from r and returns the number of bytes written
// If reading from r fails, path is left untouched
func WriteFileFrom(path string, r io.Reader, perm fs.FileMode, opts ...Option) (int64, error) {
	o := newOptions(path, opts)

	tmpPath, n, err := writeTempFile(path, r, perm, o.tempPrefix)
	if err != nil {
		return 0, err
	}

	err = os.Rename(tmpPath, path)
	if err != nil {
		_ = os.Remove(tmpPath)
		return 0, fmt.Errorf("failed to rename the temporary file to '%s': %w", path, err)
	}

	// Sync the parent directory so the rename is durable
	err = SyncDir(filepath.Dir(path))
	if err != nil {
		return n, err
	}

	return n, nil
}

// CreateFile writes data to path atomically like WriteFile, but fails with fs.ErrExist if path already exists
// It links the temporary file to path so only one concurrent call succeeds, falling back to rename if hard links are not supported
func CreateFile(path string, data []byte, perm fs.FileMode, opts ...Option) error {
	o := newOptions(path, opts)

	tmpPath, _, err := writeTempFile(path, bytes.NewReader(data), perm, o.tempPrefix)
	if err != nil {
		return err
	}

	// Always remove the temporary file (after linking, path points to the same inode)
	defer os.Remove(tmpPath) //nolint:errcheck

	linkErr := os.Link(tmpPath, path)
	if linkErr != nil {
		// Check whether path already exists or the filesystem does not support hard links
		info, statErr := os.Lstat(path)
		switch {
		case statErr == nil && o.replaceEmpty && info.Mode().IsRegular() && info.Size() == 0:
			// Replace the existing empty file
		case statErr == nil:
			return fmt.Errorf("file '%s' already exists: %w", path, fs.ErrExist)
		case errors.Is(statErr, fs.ErrNotExist) && !errors.Is(linkErr, fs.ErrExist):
			// Fall back to rename when hard links are not supported
		default:
			return fmt.Errorf("failed to link the temporary file to '%s': %w", path, linkErr)
		}

		err = os.Rename(tmpPath, path)
		if err != nil {
			return fmt.Errorf("failed to rename the temporary file to '%s': %w", path, err)
		}
	}

	// Sync the parent directory so the new entry is durable
	return SyncDir(filepath.Dir(path))
}

// SyncDir flushes a directory to disk
// It is a no-op on Windows and ignores errors from filesystems that do not support syncing directories
func SyncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}

	d, err := os.Open(dir) // #nosec G304 -- Caller-supplied directory path
	if err != nil {
		return fmt.Errorf("failed to open the directory '%s' to sync it: %w", dir, err)
	}

	err = d.Sync()
	closeErr := d.Close()
	if err != nil && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ENOTSUP) {
		return fmt.Errorf("failed to sync the directory '%s': %w", dir, err)
	}
	if closeErr != nil {
		return fmt.Errorf("failed to close the directory '%s': %w", dir, closeErr)
	}

	return nil
}

// writeTempFile writes r to a new temporary file in filepath.Dir(path), syncs it, and returns its path and byte count
func writeTempFile(path string, r io.Reader, perm fs.FileMode, prefix string) (_ string, n int64, err error) {
	// Create the temp file in the same directory as path so renaming is atomic
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, prefix+"*")
	if err != nil {
		return "", 0, fmt.Errorf("failed to create a temporary file in '%s': %w", dir, err)
	}

	tmpPath := f.Name()
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	n, err = io.Copy(f, r)
	if err != nil {
		return "", 0, fmt.Errorf("failed to write the temporary file '%s': %w", tmpPath, err)
	}

	// Set permissions on the open file descriptor to bypass umask and avoid following symlinks
	err = f.Chmod(perm)
	if err != nil {
		return "", 0, fmt.Errorf("failed to set the permissions of the temporary file '%s': %w", tmpPath, err)
	}

	// Flush contents to disk before renaming or linking
	err = f.Sync()
	if err != nil {
		return "", 0, fmt.Errorf("failed to sync the temporary file '%s': %w", tmpPath, err)
	}

	// Close before renaming (required on Windows)
	err = f.Close()
	if err != nil {
		return "", 0, fmt.Errorf("failed to close the temporary file '%s': %w", tmpPath, err)
	}

	return tmpPath, n, nil
}
