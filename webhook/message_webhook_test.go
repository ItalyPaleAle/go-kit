package webhook

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// plainMessageProvider implements only the required MessageProvider interface
type plainMessageProvider struct {
	message string
	err     error
}

func (p plainMessageProvider) GetPlainMessage() (string, error) {
	return p.message, p.err
}

func TestStringMessage(t *testing.T) {
	provider := StringMessage("hello world")

	message, err := provider.GetPlainMessage()

	require.NoError(t, err)
	require.Equal(t, "hello world", message)
}

func TestGetSlackMessage(t *testing.T) {
	t.Run("uses the Slack message of the provider", func(t *testing.T) {
		message, err := getSlackMessage(testMessageProvider{message: "plain", slackMessage: SlackMessage{Text: "custom"}})
		require.NoError(t, err)
		require.Equal(t, SlackMessage{Text: "custom"}, message)
	})

	t.Run("falls back to the plain-text message", func(t *testing.T) {
		message, err := getSlackMessage(StringMessage("hello slack"))
		require.NoError(t, err)
		require.Equal(t, SlackMessage{Text: "hello slack"}, message)
	})

	t.Run("returns the error of the plain-text message", func(t *testing.T) {
		_, err := getSlackMessage(plainMessageProvider{err: errors.New("plain failed")})
		require.Error(t, err)
		require.ErrorContains(t, err, "plain failed")
	})
}

func TestGetNtfyMessage(t *testing.T) {
	t.Run("uses the ntfy message of the provider", func(t *testing.T) {
		custom := NtfyMessage{Message: "custom", Title: "title", Tags: []string{"warning"}}
		message, err := getNtfyMessage(testMessageProvider{message: "plain", ntfyMessage: custom})
		require.NoError(t, err)
		require.Equal(t, custom, message)
	})

	t.Run("falls back to the plain-text message", func(t *testing.T) {
		message, err := getNtfyMessage(StringMessage("hello ntfy"))
		require.NoError(t, err)
		require.Equal(t, NtfyMessage{Message: "hello ntfy"}, message)
	})

	t.Run("returns the error of the plain-text message", func(t *testing.T) {
		_, err := getNtfyMessage(plainMessageProvider{err: errors.New("plain failed")})
		require.Error(t, err)
		require.ErrorContains(t, err, "plain failed")
	})
}

func TestGetTelegramMessage(t *testing.T) {
	t.Run("uses the Telegram message of the provider", func(t *testing.T) {
		custom := TelegramMessage{Text: "<b>custom</b>", ParseMode: TelegramParseModeHTML, DisableNotification: true}
		message, err := getTelegramMessage(testMessageProvider{message: "plain", telegramMessage: custom})
		require.NoError(t, err)
		require.Equal(t, custom, message)
	})

	t.Run("falls back to the plain-text message without formatting", func(t *testing.T) {
		message, err := getTelegramMessage(StringMessage("hello <telegram>"))
		require.NoError(t, err)
		require.Equal(t, TelegramMessage{Text: "hello <telegram>"}, message)
	})

	t.Run("returns the error of the plain-text message", func(t *testing.T) {
		_, err := getTelegramMessage(plainMessageProvider{err: errors.New("plain failed")})
		require.Error(t, err)
		require.ErrorContains(t, err, "plain failed")
	})
}
