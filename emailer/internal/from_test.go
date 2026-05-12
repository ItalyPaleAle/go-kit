package internal

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFormatFromAddress(t *testing.T) {
	// Preserve the raw address when callers do not provide a display name
	assert.Equal(t, "sender@example.com", FormatFromAddress("", "sender@example.com"))

	// Include the display name when one is available so providers receive the full From header form
	assert.Equal(t, "Sender Name <sender@example.com>", FormatFromAddress("Sender Name", "sender@example.com"))
}
