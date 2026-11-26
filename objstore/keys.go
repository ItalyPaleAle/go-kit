package objstore

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Maximum length of an object key, which is the smallest limit among the providers this package supports
const maxKeyLength = 512

// ValidateKey checks that a key addresses an object inside a backend
//
// Keys are checked rather than sanitized, so a key that could escape the backend's own namespace fails the operation instead of landing somewhere unexpected
func ValidateKey(key string) error {
	switch {
	case key == "":
		return errors.New("object key is empty")
	case len(key) > maxKeyLength:
		return errors.New("object key is too long")
	case !utf8.ValidString(key):
		return errors.New("object key is not valid UTF-8")
	case strings.HasPrefix(key, "/"), strings.HasSuffix(key, "/"), strings.Contains(key, "\\"), strings.Contains(key, ":"):
		return fmt.Errorf("object key '%s' is not a relative key", key)
	}

	for _, r := range key {
		if unicode.IsControl(r) {
			return fmt.Errorf("object key '%s' contains control characters", key)
		}
	}

	for segment := range strings.SplitSeq(key, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("object key '%s' contains an invalid path segment", key)
		}
	}

	return nil
}

// Put the backend's configured key prefix in front of an object key
// path.Join is what normalizes the separators, and it's the right one here because object keys use forward slashes on every platform
func prefixedKey(prefix string, key string) string {
	if prefix == "" {
		return key
	}

	return strings.TrimPrefix(path.Join(prefix, key), "/")
}
