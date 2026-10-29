package objstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Default permissions for created files and directories
const (
	DefaultFSFileMode fs.FileMode = 0o600
	DefaultFSDirMode  fs.FileMode = 0o750
)

const (
	fsOptionFileMode = "fileMode"
	fsOptionDirMode  = "dirMode"
)

// FSOptions are the settings of a file system backend
type FSOptions struct {
	// Permissions of the files the backend writes, DefaultFSFileMode when zero
	// It needs at least 0600
	FileMode fs.FileMode
	// Permissions of the folders the backend creates, the root included, DefaultFSDirMode when zero
	// It needs at least 0700
	DirMode fs.FileMode
}

// FSBackend stores objects as files under a root folder, with the key as the path relative to it
type FSBackend struct {
	root     string
	fileMode fs.FileMode
	dirMode  fs.FileMode
}

// NewFS creates a file system backend rooted at a folder, creating the folder if it doesn't exist yet
func NewFS(root string) (*FSBackend, error) {
	return NewFSWithOptions(root, FSOptions{})
}

// NewFSWithOptions creates a file system backend rooted at a folder, creating the folder if it doesn't exist yet
func NewFSWithOptions(root string, opts FSOptions) (*FSBackend, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("the path of the local storage backend is empty")
	}

	// Validate the permissions before creating the folder, so a typo in them doesn't leave a folder behind
	fileMode, dirMode, err := opts.modes()
	if err != nil {
		return nil, err
	}

	// The path is resolved once, here, because callers can turn keys into paths from a different working directory than the one the process started in
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve the path '%s': %w", root, err)
	}

	b := &FSBackend{
		root:     root,
		fileMode: fileMode,
		dirMode:  dirMode,
	}
	err = b.mkdirAll(root)
	if err != nil {
		return nil, err
	}

	return b, nil
}

// Create the file system backend a DSN's value describes: a path, optionally followed by "?fileMode=0644&dirMode=0755"
func newFSBackendFromDSN(value string) (*FSBackend, error) {
	path, query, _ := strings.Cut(value, "?")
	opts, err := parseFSOptions(query)
	if err != nil {
		return nil, err
	}

	return NewFSWithOptions(path, opts)
}

// Read the options of a file system DSN
func parseFSOptions(query string) (FSOptions, error) {
	if query == "" {
		return FSOptions{}, nil
	}

	values, err := url.ParseQuery(query)
	if err != nil {
		return FSOptions{}, fmt.Errorf("the options of the local storage DSN are invalid: %w", err)
	}

	// Ensure there are no unknown options
	for key := range values {
		switch key {
		case fsOptionFileMode, fsOptionDirMode:
			// All good
		default:
			return FSOptions{}, fmt.Errorf("the local storage DSN has the unknown option '%s': the options are '%s' and '%s'", key, fsOptionFileMode, fsOptionDirMode)
		}
	}

	var opts FSOptions
	if values.Has(fsOptionFileMode) {
		opts.FileMode, err = parseFSMode(values.Get(fsOptionFileMode))
		if err != nil {
			return FSOptions{}, fmt.Errorf("option '%s' of the local storage DSN is invalid: %w", fsOptionFileMode, err)
		}
	}
	if values.Has(fsOptionDirMode) {
		opts.DirMode, err = parseFSMode(values.Get(fsOptionDirMode))
		if err != nil {
			return FSOptions{}, fmt.Errorf("option '%s' of the local storage DSN is invalid: %w", fsOptionDirMode, err)
		}
	}

	return opts, nil
}

// Parse permissions written in octal, such as "0644", "644" or "0o644"
func parseFSMode(value string) (fs.FileMode, error) {
	value = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), "0o")
	if value == "" {
		return 0, errors.New("it is empty")
	}

	mode, err := strconv.ParseUint(value, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("'%s' is not an octal number such as 0644", value)
	}
	if mode == 0 || mode > 0o777 {
		return 0, fmt.Errorf("'%04o' is not a permission between 0001 and 0777", mode)
	}

	return fs.FileMode(mode), nil
}

// Resolve the permissions the options ask for, falling back to the defaults for the ones they leave out
func (o FSOptions) modes() (fileMode fs.FileMode, dirMode fs.FileMode, err error) {
	fileMode = o.FileMode
	if fileMode == 0 {
		fileMode = DefaultFSFileMode
	}
	dirMode = o.DirMode
	if dirMode == 0 {
		dirMode = DefaultFSDirMode
	}

	// Only the permission bits are set, since the backend creates plain files and folders
	if fileMode&^fs.ModePerm != 0 {
		return 0, 0, fmt.Errorf("the file mode of the local storage backend is %s, which has bits other than the permissions", fileMode)
	}
	if dirMode&^fs.ModePerm != 0 {
		return 0, 0, fmt.Errorf("the folder mode of the local storage backend is %s, which has bits other than the permissions", dirMode)
	}

	// The backend reads back what it writes, and creates files in the folders it writes to, so it can't give those permissions away from itself
	if fileMode&0o600 != 0o600 {
		return 0, 0, fmt.Errorf("the file mode of the local storage backend is %04o, which doesn't let the process read and write its own files: it needs 0600 at least", fileMode)
	}
	if dirMode&0o700 != 0o700 {
		return 0, 0, fmt.Errorf("the folder mode of the local storage backend is %04o, which doesn't let the process use its own folders: it needs 0700 at least", dirMode)
	}

	return fileMode, dirMode, nil
}

// Create a folder and every missing one above it, giving each folder it creates the backend's permissions
//
// The permissions are set explicitly after each folder is created, because the umask applied to a new folder can only ever take bits away from them
// A folder that already exists is left as it is, whoever created it
func (f *FSBackend) mkdirAll(dir string) error {
	info, err := os.Stat(dir)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("failed to create the folder '%s': a file is in its place", dir)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to create the folder '%s': %w", dir, err)
	}

	// The parent is created first, so each level is created by this call and given the permissions in turn
	parent := filepath.Dir(dir)
	if parent != dir {
		err = f.mkdirAll(parent)
		if err != nil {
			return err
		}
	}

	err = os.Mkdir(dir, f.dirMode)
	if errors.Is(err, os.ErrExist) {
		// Another write created it at the same time, and that write is the one that sets its permissions
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to create the folder '%s': %w", dir, err)
	}

	err = os.Chmod(dir, f.dirMode)
	if err != nil {
		return fmt.Errorf("failed to set the permissions of the folder '%s': %w", dir, err)
	}

	return nil
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

// Description returns the root folder objects are stored in, and the permissions they're given when those aren't the defaults
func (f *FSBackend) Description() string {
	desc := "local file system, in '" + f.root + "'"
	if f.fileMode != DefaultFSFileMode || f.dirMode != DefaultFSDirMode {
		desc += fmt.Sprintf(", with permissions %04o for files and %04o for folders", f.fileMode, f.dirMode)
	}

	return desc
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
	err = f.mkdirAll(dir)
	if err != nil {
		return PutResult{}, err
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

	// The temporary file is created readable by its owner only, and it's given the backend's permissions before the rename, so the object never appears with any others
	err = tmp.Chmod(f.fileMode)
	if err != nil {
		return PutResult{}, fmt.Errorf("failed to set the permissions of the temporary file '%s': %w", tmpPath, err)
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
