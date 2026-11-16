package utils

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasControlChars(t *testing.T) {
	tests := []struct {
		name     string
		s        string
		expected bool
	}{
		{name: "empty", s: "", expected: false},
		{name: "URL", s: "https://api.example.com", expected: false},
		{name: "URI with colons", s: "api://AzureADTokenExchange", expected: false},
		{name: "non-ASCII", s: "https://café.example.com/ñ", expected: false},
		{name: "space and tilde", s: "a b~", expected: false},
		{name: "NUL", s: "a\x00b", expected: true},
		{name: "CR", s: "a\rb", expected: true},
		{name: "LF", s: "a\nb", expected: true},
		{name: "tab", s: "a\tb", expected: true},
		{name: "unit separator", s: "\x1f", expected: true},
		{name: "DEL", s: "\x7f", expected: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, HasControlChars(tt.s))
		})
	}
}

func TestIsPrintableASCII(t *testing.T) {
	tests := []struct {
		name     string
		s        string
		expected bool
	}{
		{name: "empty", s: "", expected: true},
		{name: "base64url", s: "aB3-_xYz", expected: true},
		{name: "space and tilde", s: "a b~", expected: true},
		{name: "all punctuation", s: "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}", expected: true},
		{name: "non-ASCII", s: "clé", expected: false},
		{name: "NUL", s: "a\x00b", expected: false},
		{name: "LF", s: "a\nb", expected: false},
		{name: "tab", s: "a\tb", expected: false},
		{name: "DEL", s: "\x7f", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, IsPrintableASCII(tt.s))
		})
	}
}
