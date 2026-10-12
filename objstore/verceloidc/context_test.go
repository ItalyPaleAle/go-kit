package verceloidc

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTokenContextIgnoresEmptyValues(t *testing.T) {
	ctx := t.Context()
	ctx = WithToken(ctx, "  ")
	assert.Empty(t, TokenFromContext(ctx))
}

func TestTokenContextTrimsAndReturnsToken(t *testing.T) {
	ctx := WithToken(t.Context(), "  current-token  ")
	assert.Equal(t, "current-token", TokenFromContext(ctx))
}
