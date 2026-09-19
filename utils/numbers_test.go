package utils

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestPositiveOr(t *testing.T) {
	tests := []struct {
		name     string
		value    int
		fallback int
		expected int
	}{
		{name: "positive", value: 2, fallback: 1, expected: 2},
		{name: "zero", value: 0, fallback: 1, expected: 1},
		{name: "negative", value: -1, fallback: 1, expected: 1},
		{name: "zero fallback", value: -1, fallback: 0, expected: 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.expected, PositiveOr(test.value, test.fallback))
		})
	}
}

func TestPositiveOrSupportsDurations(t *testing.T) {
	assert.Equal(t, 2*time.Second, PositiveOr(2*time.Second, time.Second))
	assert.Equal(t, time.Second, PositiveOr(-time.Second, time.Second))
}
