package objstore

import (
	"fmt"
	"io"
	"math"
	"os"
)

// Copy a bounded body to a temporary file, returning it rewound and ready to be sent
//
// The backends that upload over HTTP need to know the size of the object up-front to set a Content-Length, and a temporary file is what gives them that without keeping the whole object in memory
// Reading stops one byte past the limit, which is what tells an object that's exactly at the limit apart from one that's over it
func spoolBounded(body io.Reader, maxSize int64) (*os.File, int64, error) {
	if maxSize < 0 {
		return nil, 0, ErrTooLarge
	}

	file, err := os.CreateTemp("", "objstore-upload-*")
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create the upload spool file: %w", err)
	}

	reader := body
	if maxSize < math.MaxInt64 {
		reader = io.LimitReader(body, maxSize+1)
	}

	size, err := io.Copy(file, reader)
	if err != nil {
		closeAndRemove(file)
		return nil, 0, fmt.Errorf("failed to write the upload spool file: %w", err)
	}
	if size > maxSize {
		closeAndRemove(file)
		return nil, 0, ErrTooLarge
	}

	_, err = file.Seek(0, io.SeekStart)
	if err != nil {
		closeAndRemove(file)
		return nil, 0, fmt.Errorf("failed to rewind the upload spool file: %w", err)
	}

	return file, size, nil
}

// Close a spool file and remove it from disk
func closeAndRemove(file *os.File) {
	if file == nil {
		return
	}

	name := file.Name()
	_ = file.Close()
	_ = os.Remove(name)
}
