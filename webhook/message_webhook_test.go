package webhook

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStringMessage(t *testing.T) {
	t.Run("returns the wrapped string as plain text", func(t *testing.T) {
		provider := StringMessage("hello world")

		message, err := provider.GetPlainMessage()

		require.NoError(t, err)
		require.Equal(t, "hello world", message)
	})

	t.Run("returns a Slack-compatible message with the wrapped string", func(t *testing.T) {
		provider := StringMessage("hello slack")

		message, err := provider.GetSlackMessage()

		require.NoError(t, err)
		require.Equal(t, SlackMessage{Text: "hello slack"}, message)
	})
}
