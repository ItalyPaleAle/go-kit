package config

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetInstanceID(t *testing.T) {
	t.Run("uses Azure Container Apps replica name when set", func(t *testing.T) {
		t.Setenv("CONTAINER_APP_REPLICA_NAME", "replica-123")
		t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "service.instance.id=from-otel")

		got, err := GetInstanceID()
		require.NoError(t, err)
		assert.Equal(t, "replica-123", got)
	})

	t.Run("uses OTEL service.instance.id when Azure env var is not set", func(t *testing.T) {
		t.Setenv("CONTAINER_APP_REPLICA_NAME", "")
		t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "service.name=myapp,service.instance.id=otel-instance-1")

		got, err := GetInstanceID()
		require.NoError(t, err)
		assert.Equal(t, "otel-instance-1", got)
	})

	t.Run("decodes URL-encoded OTEL service.instance.id", func(t *testing.T) {
		t.Setenv("CONTAINER_APP_REPLICA_NAME", "")
		t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "service.instance.id=abc%2Fdef%20ghi")

		got, err := GetInstanceID()
		require.NoError(t, err)
		assert.Equal(t, "abc/def ghi", got)
	})

	t.Run("falls back to random ID when OTEL data is invalid", func(t *testing.T) {
		t.Setenv("CONTAINER_APP_REPLICA_NAME", "")
		t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "service.instance.id=%zz")

		got, err := GetInstanceID()
		require.NoError(t, err)
		assert.NotEmpty(t, got)

		decoded, decErr := base64.RawURLEncoding.DecodeString(got)
		require.NoError(t, decErr)
		assert.Len(t, decoded, 7)
	})

	t.Run("falls back to random ID when no env vars are set", func(t *testing.T) {
		t.Setenv("CONTAINER_APP_REPLICA_NAME", "")
		t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "")

		got, err := GetInstanceID()
		require.NoError(t, err)
		assert.NotEmpty(t, got)

		decoded, decErr := base64.RawURLEncoding.DecodeString(got)
		require.NoError(t, decErr)
		assert.Len(t, decoded, 7)
	})

	t.Run("random fallback produces different values across calls", func(t *testing.T) {
		t.Setenv("CONTAINER_APP_REPLICA_NAME", "")
		t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "")

		first, err := GetInstanceID()
		require.NoError(t, err)
		second, err := GetInstanceID()
		require.NoError(t, err)

		assert.NotEqual(t, first, second)
	})
}
