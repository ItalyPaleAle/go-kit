package webhook

import (
	"errors"
	"strings"
)

// TeamsMessage is the type for a message in the Microsoft Teams format
// It is sent as an Adaptive Card, which the Workflows templates for webhooks, such as "Send webhook alerts to a channel", post to the channel or chat
type TeamsMessage struct {
	// Title is the optional title of the message, shown in bold above the text
	Title string
	// Text is the text of the message
	// Teams renders it with a subset of Markdown, such as **bold**, _italic_, lists, and [links](https://example.com) (https://learn.microsoft.com/en-us/adaptive-cards/authoring-cards/text-features)
	// At least one of Title and Text is required
	Text string
}

const (
	teamsAdaptiveCardContentType = "application/vnd.microsoft.card.adaptive"
	teamsAdaptiveCardSchema      = "http://adaptivecards.io/schemas/adaptive-card.json"
	// Version 1.2 has every element used here, and it is supported by all Teams clients, including mobile
	teamsAdaptiveCardVersion = "1.2"
)

// teamsRequest is the body of a request to a Teams webhook: a message with an Adaptive Card as its only attachment
type teamsRequest struct {
	Type        string            `json:"type"`
	Attachments []teamsAttachment `json:"attachments"`
}

type teamsAttachment struct {
	ContentType string            `json:"contentType"`
	Content     teamsAdaptiveCard `json:"content"`
}

type teamsAdaptiveCard struct {
	Schema  string           `json:"$schema"`
	Type    string           `json:"type"`
	Version string           `json:"version"`
	Body    []teamsTextBlock `json:"body"`
}

type teamsTextBlock struct {
	Type   string `json:"type"`
	Text   string `json:"text"`
	Wrap   bool   `json:"wrap"`
	Size   string `json:"size,omitempty"`
	Weight string `json:"weight,omitempty"`
}

// requestBody validates the message and returns the body of the request that sends it
func (m TeamsMessage) requestBody() (teamsRequest, error) {
	title := strings.TrimSpace(m.Title)
	text := strings.TrimSpace(m.Text)
	if title == "" && text == "" {
		return teamsRequest{}, errors.New("title and text must not both be empty")
	}

	// Wrapping is enabled because text blocks are truncated to a single line otherwise
	body := make([]teamsTextBlock, 0, 2)
	if title != "" {
		body = append(body, teamsTextBlock{Type: "TextBlock", Text: m.Title, Wrap: true, Size: "Medium", Weight: "Bolder"})
	}
	if text != "" {
		body = append(body, teamsTextBlock{Type: "TextBlock", Text: m.Text, Wrap: true})
	}

	return teamsRequest{
		Type: "message",
		Attachments: []teamsAttachment{{
			ContentType: teamsAdaptiveCardContentType,
			Content: teamsAdaptiveCard{
				Schema:  teamsAdaptiveCardSchema,
				Type:    "AdaptiveCard",
				Version: teamsAdaptiveCardVersion,
				Body:    body,
			},
		}},
	}, nil
}
