package siem

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testKnownEventTypes = []string{
	"account.login",
	"account.delete",
	"order.create",
	"order.confirm",
	"invoice.delete",
}

func TestFilterMatchesEverythingWhenEmpty(t *testing.T) {
	f, err := newFilter(nil, testKnownEventTypes)
	require.NoError(t, err)

	assert.True(t, f.matchAll())
	assert.True(t, f.matches("order.confirm"))
	assert.True(t, f.matches("anything.at_all"))
}

func TestFilterExactEntries(t *testing.T) {
	f, err := newFilter([]string{"order.confirm"}, testKnownEventTypes)
	require.NoError(t, err)

	assert.False(t, f.matchAll())
	assert.True(t, f.matches("order.confirm"))
	assert.False(t, f.matches("order.create"))
	assert.False(t, f.matches("account.login"))
}

func TestFilterWildcardEntries(t *testing.T) {
	f, err := newFilter([]string{"order.*"}, testKnownEventTypes)
	require.NoError(t, err)

	assert.True(t, f.matches("order.confirm"))
	assert.True(t, f.matches("order.create"))

	// A verb that is not known today still matches its area, so a new event type joins the feed without a config change
	assert.True(t, f.matches("order.something_new"))
	assert.False(t, f.matches("account.login"))
}

func TestFilterMixedEntries(t *testing.T) {
	f, err := newFilter([]string{"order.*", "account.login", "  "}, testKnownEventTypes)
	require.NoError(t, err)

	assert.True(t, f.matches("order.create"))
	assert.True(t, f.matches("account.login"))
	assert.False(t, f.matches("account.delete"))
	assert.False(t, f.matches("invoice.delete"))
}

func TestFilterNormalizesEntries(t *testing.T) {
	f, err := newFilter([]string{" Order.Confirm "}, testKnownEventTypes)
	require.NoError(t, err)

	assert.True(t, f.matches("order.confirm"))
}

func TestFilterRejectsMalformedEntries(t *testing.T) {
	tests := []string{
		"order",
		"order.",
		".confirm",
		"order.confirm.extra",
		"Order-Confirm",
		"*",
		"*.confirm",
	}

	for _, entry := range tests {
		t.Run(entry, func(t *testing.T) {
			_, err := newFilter([]string{entry}, testKnownEventTypes)

			require.Error(t, err)
			assert.ErrorContains(t, err, "invalid event type filter")
		})
	}
}

func TestFilterRejectsUnknownEntries(t *testing.T) {
	t.Run("unknown event type", func(t *testing.T) {
		_, err := newFilter([]string{"order.confrim"}, testKnownEventTypes)

		require.Error(t, err)
		assert.ErrorContains(t, err, "not a known event type")
	})

	t.Run("unknown area", func(t *testing.T) {
		_, err := newFilter([]string{"vault.*"}, testKnownEventTypes)

		require.Error(t, err)
		assert.ErrorContains(t, err, "no known event type belongs to area")
	})

	t.Run("accepts anything when no known set is supplied", func(t *testing.T) {
		f, err := newFilter([]string{"order.confrim", "vault.*"}, nil)

		require.NoError(t, err)
		assert.True(t, f.matches("order.confrim"))
		assert.True(t, f.matches("vault.anything"))
	})
}
