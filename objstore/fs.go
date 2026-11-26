package objstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
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

	err = ValidateKey(key)
	if err != nil {
		return PutResult{}, err
	}

	relPath := filepath.FromSlash(key)

	root, err := os.OpenRoot(f.root)
	if err != nil {
		return PutResult{}, fmt.Errorf("failed to open the storage root '%s': %w", f.root, err)
	}
	defer root.Close() //nolint:errcheck

	// The folder of the object is created here rather than at startup, because a key can name folders the backend has never seen
	relDir := filepath.Dir(relPath)
	if relDir != "." {
		err = f.mkdirAllInRoot(root, relDir)
		if err != nil {
			return PutResult{}, err
		}
	}

	existing, err := root.Lstat(relPath)
	if err == nil && existing.IsDir() {
		return PutResult{}, fmt.Errorf("failed to write the object '%s': a folder is in its place", key)
	}

	// Write atomically via a temporary file inside root, aborting with ErrTooLarge if body exceeds maxSize
	msr := &maxSizeReader{
		r:         body,
		remaining: maxSize,
	}
	size, err := writeFileInRoot(root, relDir, relPath, msr, f.fileMode)
	if errors.Is(err, ErrTooLarge) {
		return PutResult{}, ErrTooLarge
	} else if err != nil {
		return PutResult{}, fmt.Errorf("failed to write the object '%s': %w", key, err)
	}

	return PutResult{Size: size}, nil
}

// mkdirAllInRoot creates dir and any missing parents inside root, applying f.dirMode to every newly created folder
func (f *FSBackend) mkdirAllInRoot(root *os.Root, dir string) error {
	info, err := root.Stat(dir)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("failed to create the folder '%s': a file is in its place", dir)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to create the folder '%s': %w", dir, err)
	}

	parent := filepath.Dir(dir)
	if parent != "." && parent != dir {
		err = f.mkdirAllInRoot(root, parent)
		if err != nil {
			return err
		}
	}

	err = root.Mkdir(dir, f.dirMode)
	if errors.Is(err, os.ErrExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to create the folder '%s': %w", dir, err)
	}

	err = root.Chmod(dir, f.dirMode)
	if err != nil {
		return fmt.Errorf("failed to set the permissions of the folder '%s': %w", dir, err)
	}

	return nil
}

// writeFileInRoot writes r to a temporary file inside root and atomically renames it to relPath
func writeFileInRoot(root *os.Root, relDir string, relPath string, r io.Reader, perm fs.FileMode) (n int64, err error) {
	prefix := "." + filepath.Base(relPath) + "."
	tmpFile, tmpRelPath, err := createTempInRoot(root, relDir, prefix)
	if err != nil {
		return 0, err
	}

	closed := false
	defer func() {
		if err != nil {
			if !closed {
				_ = tmpFile.Close()
			}
			_ = root.Remove(tmpRelPath)
		}
	}()

	n, err = io.Copy(tmpFile, r)
	if err != nil {
		return 0, err
	}

	err = tmpFile.Chmod(perm)
	if err != nil {
		return 0, err
	}

	err = tmpFile.Sync()
	if err != nil {
		return 0, err
	}

	err = tmpFile.Close()
	closed = true
	if err != nil {
		return 0, err
	}

	err = root.Rename(tmpRelPath, relPath)
	if err != nil {
		return 0, err
	}

	err = syncDirInRoot(root, relDir)
	if err != nil {
		return n, err
	}

	return n, nil
}

func createTempInRoot(root *os.Root, relDir string, prefix string) (*os.File, string, error) {
	for range 16 {
		var raw [8]byte
		_, err := rand.Read(raw[:])
		if err != nil {
			return nil, "", fmt.Errorf("failed to generate temporary file name: %w", err)
		}

		tmpName := prefix + hex.EncodeToString(raw[:])
		tmpRelPath := tmpName
		if relDir != "." {
			tmpRelPath = filepath.Join(relDir, tmpName)
		}

		f, err := root.OpenFile(tmpRelPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		} else if err != nil {
			return nil, "", fmt.Errorf("failed to create temporary file '%s': %w", tmpRelPath, err)
		}

		return f, tmpRelPath, nil
	}

	return nil, "", errors.New("failed to create a unique temporary file")
}

func syncDirInRoot(root *os.Root, relDir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}

	d, err := root.Open(relDir)
	if err != nil {
		return fmt.Errorf("failed to open the directory '%s' to sync it: %w", relDir, err)
	}

	err = d.Sync()
	closeErr := d.Close()
	if err != nil && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ENOTSUP) {
		return fmt.Errorf("failed to sync the directory '%s': %w", relDir, err)
	}
	if closeErr != nil {
		return fmt.Errorf("failed to close the directory '%s': %w", relDir, closeErr)
	}

	return nil
}

// maxSizeReader returns ErrTooLarge once more than remaining bytes are read
type maxSizeReader struct {
	r         io.Reader
	remaining int64
}

func (m *maxSizeReader) Read(p []byte) (int, error) {
	if m.remaining < 0 {
		return 0, ErrTooLarge
	}

	// Read one byte past the limit to check if we're going over
	if m.remaining < math.MaxInt64 && int64(len(p)) > m.remaining+1 {
		p = p[:m.remaining+1]
	}

	n, err := m.r.Read(p)
	m.remaining -= int64(n)
	if m.remaining < 0 {
		return n, ErrTooLarge
	}
	return n, err //nolint:wrapcheck // Callers must see io.EOF unchanged
}

// Get opens the file an object is stored in
// The content type is derived from the file extension, since that's all the file system records about an object's type
func (f *FSBackend) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	err := ctx.Err()
	if err != nil {
		return nil, ObjectInfo{}, fmt.Errorf("failed to read the object '%s': %w", key, err)
	}

	err = ValidateKey(key)
	if err != nil {
		return nil, ObjectInfo{}, err
	}

	relPath := filepath.FromSlash(key)

	root, err := os.OpenRoot(f.root)
	if err != nil {
		return nil, ObjectInfo{}, fmt.Errorf("failed to open the storage root '%s': %w", f.root, err)
	}
	defer root.Close() //nolint:errcheck

	file, err := root.Open(relPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ObjectInfo{}, ErrNotFound
	}
	if err != nil {
		info, statErr := root.Stat(relPath)
		if statErr == nil && !info.Mode().IsRegular() {
			return nil, ObjectInfo{}, ErrNotFound
		}

		return nil, ObjectInfo{}, fmt.Errorf("failed to open the object '%s': %w", key, err)
	}

	stat, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, ObjectInfo{}, fmt.Errorf("failed to stat the object '%s': %w", key, err)
	}
	if !stat.Mode().IsRegular() {
		_ = file.Close()
		return nil, ObjectInfo{}, ErrNotFound
	}

	return file, ObjectInfo{Size: stat.Size(), ContentType: mime.TypeByExtension(filepath.Ext(key))}, nil
}

// GetConsistent opens the file an object is stored in
// Nothing caches a read of the local file system, so a read is already consistent with the write before it
func (f *FSBackend) GetConsistent(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	return f.Get(ctx, key)
}

// Exists reports whether the regular file an object is stored in is there, without opening it
func (f *FSBackend) Exists(ctx context.Context, key string) (bool, error) {
	err := ctx.Err()
	if err != nil {
		return false, fmt.Errorf("failed to look up the object '%s': %w", key, err)
	}

	err = ValidateKey(key)
	if err != nil {
		return false, err
	}

	relPath := filepath.FromSlash(key)

	root, err := os.OpenRoot(f.root)
	if err != nil {
		return false, fmt.Errorf("failed to open the storage root '%s': %w", f.root, err)
	}
	defer root.Close() //nolint:errcheck

	stat, err := root.Stat(relPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("failed to stat the object '%s': %w", key, err)
	}

	return stat.Mode().IsRegular(), nil
}

// Delete removes the file an object is stored in, and does nothing if it doesn't exist
func (f *FSBackend) Delete(ctx context.Context, key string) error {
	err := ctx.Err()
	if err != nil {
		return fmt.Errorf("failed to remove the object '%s': %w", key, err)
	}

	err = ValidateKey(key)
	if err != nil {
		return err
	}

	relPath := filepath.FromSlash(key)

	root, err := os.OpenRoot(f.root)
	if err != nil {
		return fmt.Errorf("failed to open the storage root '%s': %w", f.root, err)
	}
	defer root.Close() //nolint:errcheck

	info, err := root.Lstat(relPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err == nil && info.IsDir() {
		return nil
	}

	err = root.Remove(relPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to remove the object '%s': %w", key, err)
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
