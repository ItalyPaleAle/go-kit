package webhook

// MessageProvider is the interface provided by the webhook message data provider
// Only the plain-text message is required: to customize the message for a format, a provider can also implement the optional interface for that format (SlackMessageProvider, NtfyMessageProvider, TelegramMessageProvider, GoogleChatMessageProvider, or TeamsMessageProvider)
// For formats whose optional interface is not implemented, the plain-text message is sent
type MessageProvider interface {
	// GetPlainMessage must return the message for the webhook in plain-text format
	GetPlainMessage() (string, error)
}

// SlackMessageProvider is the optional interface for a MessageProvider that customizes the message for the "slack" and "discord" formats
type SlackMessageProvider interface {
	// GetSlackMessage must return the message for the webhook in Slack-compatible format
	GetSlackMessage() (SlackMessage, error)
}

// NtfyMessageProvider is the optional interface for a MessageProvider that customizes the message for the "ntfy" format
type NtfyMessageProvider interface {
	// GetNtfyMessage must return the message for the webhook in ntfy format
	GetNtfyMessage() (NtfyMessage, error)
}

// TelegramMessageProvider is the optional interface for a MessageProvider that customizes the message for the "telegram" format
type TelegramMessageProvider interface {
	// GetTelegramMessage must return the message for the webhook in Telegram format
	GetTelegramMessage() (TelegramMessage, error)
}

// GoogleChatMessageProvider is the optional interface for a MessageProvider that customizes the message for the "googlechat" format
type GoogleChatMessageProvider interface {
	// GetGoogleChatMessage must return the message for the webhook in Google Chat format
	GetGoogleChatMessage() (GoogleChatMessage, error)
}

// TeamsMessageProvider is the optional interface for a MessageProvider that customizes the message for the "teams" format
type TeamsMessageProvider interface {
	// GetTeamsMessage must return the message for the webhook in Microsoft Teams format
	GetTeamsMessage() (TeamsMessage, error)
}

// SlackMessage is the type for a Slack-compatible message
type SlackMessage struct {
	Text string `json:"text"`
}

// StringMessage is a MessageProvider that wraps a simple string
type StringMessage string

func (s StringMessage) GetPlainMessage() (string, error) {
	return string(s), nil
}

// getSlackMessage returns the Slack-compatible message from the provider, falling back to the plain-text message as the text
func getSlackMessage(data MessageProvider) (SlackMessage, error) {
	provider, ok := data.(SlackMessageProvider)
	if ok {
		return provider.GetSlackMessage()
	}

	text, err := data.GetPlainMessage()
	if err != nil {
		return SlackMessage{}, err
	}

	return SlackMessage{Text: text}, nil
}

// getNtfyMessage returns the ntfy message from the provider, falling back to the plain-text message as the body
func getNtfyMessage(data MessageProvider) (NtfyMessage, error) {
	provider, ok := data.(NtfyMessageProvider)
	if ok {
		return provider.GetNtfyMessage()
	}

	text, err := data.GetPlainMessage()
	if err != nil {
		return NtfyMessage{}, err
	}

	return NtfyMessage{Message: text}, nil
}

// getTelegramMessage returns the Telegram message from the provider, falling back to the plain-text message as the text, without formatting
func getTelegramMessage(data MessageProvider) (TelegramMessage, error) {
	provider, ok := data.(TelegramMessageProvider)
	if ok {
		return provider.GetTelegramMessage()
	}

	text, err := data.GetPlainMessage()
	if err != nil {
		return TelegramMessage{}, err
	}

	return TelegramMessage{Text: text}, nil
}

// getGoogleChatMessage returns the Google Chat message from the provider, falling back to the plain-text message as the text
func getGoogleChatMessage(data MessageProvider) (GoogleChatMessage, error) {
	provider, ok := data.(GoogleChatMessageProvider)
	if ok {
		return provider.GetGoogleChatMessage()
	}

	text, err := data.GetPlainMessage()
	if err != nil {
		return GoogleChatMessage{}, err
	}

	return GoogleChatMessage{Text: text}, nil
}

// getTeamsMessage returns the Microsoft Teams message from the provider, falling back to the plain-text message as the text, without a title
func getTeamsMessage(data MessageProvider) (TeamsMessage, error) {
	provider, ok := data.(TeamsMessageProvider)
	if ok {
		return provider.GetTeamsMessage()
	}

	text, err := data.GetPlainMessage()
	if err != nil {
		return TeamsMessage{}, err
	}

	return TeamsMessage{Text: text}, nil
}
