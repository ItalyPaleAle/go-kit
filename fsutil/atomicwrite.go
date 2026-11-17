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

// WithTempPrefix sets the prefix of the temporary file's name
// The temporary file is created in the destination's directory, and a crash can leave it behind, so a fixed prefix lets callers recognize leftovers and clean them up
// The prefix must not contain path separators
// The default prefix is "." followed by the destination's name and ".tmp-"
func WithTempPrefix(prefix string) Option {
	return func(o *options) {
		o.tempPrefix = prefix
	}
}

// WithReplaceEmpty makes CreateFile replace an existing empty regular file, as if it didn't exist
// This is for callers that treat an empty file as missing, such as one left behind by an earlier non-atomic write
// Replacing an empty file isn't exclusive: when concurrent calls find the same empty file, they can all succeed, and the last one wins
// WriteFile and WriteFileFrom ignore this option, since they always replace the file
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

// WriteFile writes data to the file at path atomically, creating it or replacing it
// The data is written to a temporary file in the same directory and synced to disk, the temporary file is renamed to path, and then the directory is synced too, so the new file survives a crash or a power loss
// Readers see either the previous file or the new one, never a partial write, and when an error happens before the rename, path is left untouched
// The file's permissions are exactly perm, regardless of the umask
// A symlink at path is replaced, and not followed
func WriteFile(path string, data []byte, perm fs.FileMode, opts ...Option) error {
	_, err := WriteFileFrom(path, bytes.NewReader(data), perm, opts...)
	return err
}

// WriteFileFrom is like WriteFile, but it copies the file's contents from r, and returns the number of bytes written
// If reading from r fails, the error is returned wrapped and path is left untouched, so r can enforce a size limit by returning an error
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

	// The file is in place at this point, but the rename is only durable once the directory is synced
	err = SyncDir(filepath.Dir(path))
	if err != nil {
		return n, err
	}

	return n, nil
}

// CreateFile writes data to the file at path atomically like WriteFile, but only if path doesn't exist yet
// If path exists, it returns an error that wraps fs.ErrExist and leaves the file untouched
// The temporary file is hard-linked to path, which fails when path exists, so when there are concurrent calls for the same path, exactly one succeeds
// On filesystems that don't support hard links, it falls back to a rename, which doesn't protect against concurrent calls
func CreateFile(path string, data []byte, perm fs.FileMode, opts ...Option) error {
	o := newOptions(path, opts)

	tmpPath, _, err := writeTempFile(path, bytes.NewReader(data), perm, o.tempPrefix)
	if err != nil {
		return err
	}

	// After a successful link, path is another name for the temporary file, so the temporary file is always removed
	defer os.Remove(tmpPath) //nolint:errcheck

	linkErr := os.Link(tmpPath, path)
	if linkErr != nil {
		// Find out whether the link failed because path exists, or because the filesystem doesn't support hard links
		info, statErr := os.Lstat(path)
		switch {
		case statErr == nil && o.replaceEmpty && info.Mode().IsRegular() && info.Size() == 0:
			// An empty file that the caller wants replaced
		case statErr == nil:
			return fmt.Errorf("file '%s' already exists: %w", path, fs.ErrExist)
		case errors.Is(statErr, fs.ErrNotExist) && !errors.Is(linkErr, fs.ErrExist):
			// Hard links are not supported
		default:
			return fmt.Errorf("failed to link the temporary file to '%s': %w", path, linkErr)
		}

		err = os.Rename(tmpPath, path)
		if err != nil {
			return fmt.Errorf("failed to rename the temporary file to '%s': %w", path, err)
		}
	}

	// The file is in place at this point, but the new directory entry is only durable once the directory is synced
	return SyncDir(filepath.Dir(path))
}

// SyncDir flushes a directory's entries to disk, so files that were just created, renamed, or linked in it survive a crash or a power loss
// It does nothing on Windows, which can't open directories as files
// It ignores the errors of filesystems that don't support syncing directories, such as some network and FUSE filesystems, since on those a rename is as durable as the filesystem makes it
func SyncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}

	d, err := os.Open(dir) // #nosec G304 -- Opening the directory that the caller chose is the purpose of this function
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

// Writes the contents of r to a new temporary file in the directory of path, and returns the temporary file's path and the number of bytes written
// The temporary file has mode perm and is synced to disk, and on errors it's removed
func writeTempFile(path string, r io.Reader, perm fs.FileMode, prefix string) (_ string, n int64, err error) {
	// The temporary file must be in the same directory as path, since a rename is only atomic within a filesystem
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, prefix+"*")
	if err != nil {
		return "", 0, fmt.Errorf("failed to create a temporary file in '%s': %w", dir, err)
	}

	// The path is kept in its own variable, since the error returns below reset the function's results before this deferred cleanup runs
	tmpPath := f.Name()

	// Closing a file that is already closed only returns an error, which is ignored
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

	// CreateTemp creates the file with mode 0600, so the contents can't be read by other users while they're written, even if perm allows it
	// The mode is changed through the open file and not its path, which another user with write access to the directory could replace with a symlink
	// Setting it explicitly also undoes the umask, which could have removed some bits
	err = f.Chmod(perm)
	if err != nil {
		return "", 0, fmt.Errorf("failed to set the permissions of the temporary file '%s': %w", tmpPath, err)
	}

	// The contents are flushed before the rename or link, so a power loss can't leave path pointing at an empty or partial file
	err = f.Sync()
	if err != nil {
		return "", 0, fmt.Errorf("failed to sync the temporary file '%s': %w", tmpPath, err)
	}

	// The file is closed before the rename, because Windows refuses to rename an open file, and network filesystems can report a failed write only when the file is closed
	err = f.Close()
	if err != nil {
		return "", 0, fmt.Errorf("failed to close the temporary file '%s': %w", tmpPath, err)
	}

	return tmpPath, n, nil
}
