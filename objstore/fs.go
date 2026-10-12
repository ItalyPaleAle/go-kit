package objstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FSBackend stores objects as files under a root folder, with the key as the path relative to it
type FSBackend struct {
	root string
}

// NewFS creates a file system backend rooted at a folder, creating the folder if it doesn't exist yet
func NewFS(root string) (*FSBackend, error) {
	return newFSBackend(root)
}

func newFSBackend(root string) (*FSBackend, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("the path of the local storage backend is empty")
	}

	// The path is resolved once, here, because callers can turn keys into paths from a different working directory than the one the process started in
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve the path '%s': %w", root, err)
	}

	err = os.MkdirAll(root, 0o750)
	if err != nil {
		return nil, fmt.Errorf("failed to create the folder '%s': %w", root, err)
	}

	return &FSBackend{root: root}, nil
}

// Root returns the absolute path of the folder objects are stored in
func (f *FSBackend) Root() string {
	return f.root
}

// Path returns the path of the file an object is stored in, for the callers that work with the files directly
func (f *FSBackend) Path(key string) (string, error) {
	err := ValidateKey(key)
	if err != nil {
		return "", err
	}

	return filepath.Join(f.root, filepath.FromSlash(key)), nil
}

// Description returns the root folder objects are stored in
func (f *FSBackend) Description() string {
	return "local file system, in '" + f.root + "'"
}

// Put writes an object to a file, by first writing a temporary one next to it and then renaming it into place
//
// The rename is what makes the write atomic, so a reader never sees a partially-written object and a retried write can safely overwrite one
// The content type is dropped, since on disk it's the file extension that tells a reader what an object contains
func (f *FSBackend) Put(ctx context.Context, key string, body io.Reader, maxSize int64, _ string) (PutResult, error) {
	err := ctx.Err()
	if err != nil {
		return PutResult{}, fmt.Errorf("failed to write the object '%s': %w", key, err)
	}

	path, err := f.Path(key)
	if err != nil {
		return PutResult{}, err
	}

	// The folder of the object is created here rather than at startup, because a key can name folders the backend has never seen
	dir := filepath.Dir(path)
	err = os.MkdirAll(dir, 0o750)
	if err != nil {
		return PutResult{}, fmt.Errorf("failed to create the folder '%s': %w", dir, err)
	}

	// The temporary file goes in the same folder as its target, which is what keeps the rename below atomic
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return PutResult{}, fmt.Errorf("failed to create a temporary file in '%s': %w", dir, err)
	}
	tmpPath := tmp.Name()

	// The temporary file is removed on every path, including after a successful close, since the rename below is what makes it permanent
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()

	// Copying one byte past the limit is what tells an object that's exactly at the limit apart from one that's over it
	size, err := io.Copy(tmp, io.LimitReader(body, maxSize+1))
	if err != nil {
		return PutResult{}, fmt.Errorf("failed to write to the temporary file '%s': %w", tmpPath, err)
	}
	if size > maxSize {
		return PutResult{}, ErrTooLarge
	}

	// The data is flushed before the rename, so a power loss can't leave the rename pointing at a zero-length file
	err = tmp.Sync()
	if err != nil {
		return PutResult{}, fmt.Errorf("failed to flush the temporary file '%s': %w", tmpPath, err)
	}

	// The file is closed before the rename because Windows refuses to rename an open one
	err = tmp.Close()
	if err != nil {
		return PutResult{}, fmt.Errorf("failed to close the temporary file '%s': %w", tmpPath, err)
	}

	err = os.Rename(tmpPath, path)
	if err != nil {
		return PutResult{}, fmt.Errorf("failed to move the temporary file to '%s': %w", path, err)
	}

	return PutResult{Size: size}, nil
}

// Get opens the file an object is stored in
// The content type is derived from the file extension, since that's all the file system records about an object's type
func (f *FSBackend) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	err := ctx.Err()
	if err != nil {
		return nil, ObjectInfo{}, fmt.Errorf("failed to read the object '%s': %w", key, err)
	}

	path, err := f.Path(key)
	if err != nil {
		return nil, ObjectInfo{}, err
	}

	file, err := os.Open(path) //nolint:gosec // The path is built from a validated key, rooted in the backend's own folder
	if errors.Is(err, os.ErrNotExist) {
		return nil, ObjectInfo{}, ErrNotFound
	}
	if err != nil {
		return nil, ObjectInfo{}, fmt.Errorf("failed to open the file '%s': %w", path, err)
	}

	stat, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, ObjectInfo{}, fmt.Errorf("failed to stat the file '%s': %w", path, err)
	}

	return file, ObjectInfo{Size: stat.Size(), ContentType: mime.TypeByExtension(filepath.Ext(key))}, nil
}

// GetConsistent opens the file an object is stored in
// Nothing caches a read of the local file system, so a read is already consistent with the write before it
func (f *FSBackend) GetConsistent(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	return f.Get(ctx, key)
}

// Exists reports whether the file an object is stored in is there, without opening it
func (f *FSBackend) Exists(ctx context.Context, key string) (bool, error) {
	err := ctx.Err()
	if err != nil {
		return false, fmt.Errorf("failed to look up the object '%s': %w", key, err)
	}

	path, err := f.Path(key)
	if err != nil {
		return false, err
	}

	_, err = os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("failed to stat the file '%s': %w", path, err)
	}

	return true, nil
}

// Delete removes the file an object is stored in, and does nothing if it doesn't exist
func (f *FSBackend) Delete(ctx context.Context, key string) error {
	err := ctx.Err()
	if err != nil {
		return fmt.Errorf("failed to remove the object '%s': %w", key, err)
	}

	path, err := f.Path(key)
	if err != nil {
		return err
	}

	err = os.Remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to remove the file '%s': %w", path, err)
	}

	return nil
}

// SupportsDirectUpload reports that objects can't be uploaded to the file system by a client of the process
func (f *FSBackend) SupportsDirectUpload() bool {
	return false
}

// CreateDirectUpload reports that objects have to be uploaded through the process that owns the folder
func (f *FSBackend) CreateDirectUpload(_ context.Context, _ string, _ int64, _ string, _ time.Duration) (DirectUpload, error) {
	return DirectUpload{}, ErrDirectUploadUnsupported
}

// HasPublicAddressing reports that files on disk have no URL of their own, so they can only be served by the process
func (f *FSBackend) HasPublicAddressing() bool {
	return false
}

// PublicURL returns no URL because files on disk are only addressable through the owning process
func (f *FSBackend) PublicURL(_ string) (string, error) {
	return "", nil
}
