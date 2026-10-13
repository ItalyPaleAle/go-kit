package utils

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStringSlicesMatch(t *testing.T) {
	tests := []struct {
		name     string
		a        []string
		b        []string
		expected bool
	}{
		{name: "both nil", a: nil, b: nil, expected: true},
		{name: "nil and empty", a: nil, b: []string{}, expected: true},
		{name: "same order", a: []string{"1.1.1.1", "2.2.2.2"}, b: []string{"1.1.1.1", "2.2.2.2"}, expected: true},
		{name: "different order", a: []string{"1.1.1.1", "2.2.2.2", "3.3.3.3"}, b: []string{"3.3.3.3", "1.1.1.1", "2.2.2.2"}, expected: true},
		{name: "different lengths", a: []string{"1.1.1.1"}, b: []string{"1.1.1.1", "2.2.2.2"}, expected: false},
		{name: "one empty", a: []string{"1.1.1.1"}, b: nil, expected: false},
		{name: "different elements", a: []string{"1.1.1.1", "2.2.2.2"}, b: []string{"1.1.1.1", "3.3.3.3"}, expected: false},
		{name: "same duplicates", a: []string{"1.1.1.1", "1.1.1.1", "2.2.2.2"}, b: []string{"2.2.2.2", "1.1.1.1", "1.1.1.1"}, expected: true},
		{name: "different duplicates", a: []string{"1.1.1.1", "1.1.1.1", "2.2.2.2"}, b: []string{"1.1.1.1", "2.2.2.2", "2.2.2.2"}, expected: false},
		{name: "single equal element", a: []string{"a"}, b: []string{"a"}, expected: true},
		{name: "single different element", a: []string{"a"}, b: []string{"b"}, expected: false},
		{name: "empty string elements equal", a: []string{""}, b: []string{""}, expected: true},
		{name: "empty string vs non-empty", a: []string{""}, b: []string{"a"}, expected: false},
		{name: "empty string vs empty slice", a: []string{""}, b: []string{}, expected: false},
		{name: "different multiplicity with several values", a: []string{"a", "a", "b", "c"}, b: []string{"a", "b", "b", "c"}, expected: false},
		{name: "same multiplicity with several values", a: []string{"a", "b", "c", "a"}, b: []string{"c", "a", "a", "b"}, expected: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, StringSlicesMatch(tc.a, tc.b))
			// The result does not depend on the order of the arguments
			assert.Equal(t, tc.expected, StringSlicesMatch(tc.b, tc.a))
		})
	}
}

func TestStringSlicesMatchDoesNotMutateInputs(t *testing.T) {
	a := []string{"c", "a", "b", "a"}
	b := []string{"a", "b", "a", "c"}

	StringSlicesMatch(a, b)

	assert.Equal(t, []string{"c", "a", "b", "a"}, a)
	assert.Equal(t, []string{"a", "b", "a", "c"}, b)
}
