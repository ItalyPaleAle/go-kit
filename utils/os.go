package utils

import (
	"errors"
	"io/fs"
	"os"
)

// FileExists returns true if a file exists on disk and is a regular file
func FileExists(path string) (bool, error) {
	s, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return s.Mode().IsRegular(), nil
}
