package utils

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// IsTruthy returns true if a string is truthy, such as "1", "on", "yes", "true", "t", "y"
func IsTruthy(str string) bool {
	if len(str) > 4 {
		// Short-circuit to avoid processing strings that can't be true
		return false
	}
	switch strings.ToLower(str) {
	case "1", "true", "t", "on", "yes", "y":
		return true
	default:
		return false
	}
}

// HasControlChars returns true if s contains control characters, or if it isn't valid UTF-8
// Other characters, including other non-ASCII ones, are allowed
func HasControlChars(s string) bool {
	if !utf8.ValidString(s) {
		return true
	}

	for _, c := range s {
		if unicode.In(c, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp) {
			return true
		}
	}

	return false
}

// IsPrintableASCII returns true if s contains only printable ASCII characters (0x20-0x7E)
// Returns true for an empty string
func IsPrintableASCII(s string) bool {
	for _, c := range []byte(s) {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}
