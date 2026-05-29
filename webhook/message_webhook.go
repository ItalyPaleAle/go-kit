package webhook

// MessageProvider is the interface provided by the webhook message data provider
type MessageProvider interface {
	// GetPlainMessage must return the message for the webhook in plain-text format
	GetPlainMessage() (string, error)
	// GetSlackMessage must return the message for the webhook in Slack-compatible format
	GetSlackMessage() (SlackMessage, error)
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

func (s StringMessage) GetSlackMessage() (SlackMessage, error) {
	return SlackMessage{
		Text: string(s),
	}, nil
}
