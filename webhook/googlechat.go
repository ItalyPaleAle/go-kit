package webhook

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// GoogleChatMessage is the type for a message in the Google Chat format (https://developers.google.com/workspace/chat/quickstart/webhooks)
type GoogleChatMessage struct {
	// Text is the text of the message (required)
	// It supports the Google Chat formatting syntax, such as *bold*, _italic_, and <https://example.com|links> (https://developers.google.com/workspace/chat/format-messages)
	Text string
	// ThreadKey is the optional key of the thread to post the message in
	// Messages with the same key are posted in the same thread, which the first one starts
	ThreadKey string
}

// googleChatRequest is the body of a request to a Google Chat webhook
type googleChatRequest struct {
	Text   string            `json:"text"`
	Thread *googleChatThread `json:"thread,omitempty"`
}

type googleChatThread struct {
	ThreadKey string `json:"threadKey"`
}

// requestBody validates the message and returns the body of the request that sends it
func (m GoogleChatMessage) requestBody() (googleChatRequest, error) {
	// Google Chat rejects empty messages
	if strings.TrimSpace(m.Text) == "" {
		return googleChatRequest{}, errors.New("text must not be empty")
	}

	body := googleChatRequest{Text: m.Text}
	if m.ThreadKey != "" {
		body.Thread = &googleChatThread{ThreadKey: m.ThreadKey}
	}

	return body, nil
}

// validateGoogleChatURL checks that the URL of a Google Chat webhook has the path of a space, and the key and token query parameters
// Google Chat generates URLs like "https://chat.googleapis.com/v1/spaces/<space>/messages?key=<key>&token=<token>", so a missing part usually means that the URL was truncated when copied
func validateGoogleChatURL(parsed *url.URL) error {
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	n := len(segments)
	if n < 3 || segments[n-3] != "spaces" || segments[n-2] == "" || segments[n-1] != "messages" {
		return errors.New("webhook URL for Google Chat must have a path like /v1/spaces/<space>/messages")
	}

	query := parsed.Query()
	if query.Get("key") == "" || query.Get("token") == "" {
		return errors.New("webhook URL for Google Chat must include the key and token query parameters")
	}

	return nil
}

// googleChatThreadURL returns the webhook URL for a message with a thread key
// Google Chat ignores the thread key unless the URL sets messageReplyOption, so this adds the option that replies in the thread and starts it if it does not exist
// If the URL already sets messageReplyOption, it is left unchanged
func googleChatThreadURL(webhookURL string) (string, error) {
	parsed, err := url.Parse(webhookURL)
	if err != nil {
		return "", fmt.Errorf("invalid webhook URL: %w", err)
	}

	if parsed.Query().Has("messageReplyOption") {
		return webhookURL, nil
	}

	// Append to the raw query string rather than encoding the query again, so the key and token are sent exactly as configured
	if parsed.RawQuery != "" {
		parsed.RawQuery += "&"
	}
	parsed.RawQuery += "messageReplyOption=REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD"

	return parsed.String(), nil
}
