package webhook

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// TelegramParseMode is the formatting of the text of a Telegram message (https://core.telegram.org/bots/api#formatting-options)
type TelegramParseMode string

const (
	// TelegramParseModeNone sends the text as-is, without formatting
	TelegramParseModeNone TelegramParseMode = ""
	// TelegramParseModeHTML parses the text as HTML, so "<", ">" and "&" must be escaped
	TelegramParseModeHTML TelegramParseMode = "HTML"
	// TelegramParseModeMarkdownV2 parses the text as MarkdownV2, so the special characters must be escaped
	TelegramParseModeMarkdownV2 TelegramParseMode = "MarkdownV2"
)

// TelegramMessage is the type for a message in the Telegram format
// It only includes the options of the sendMessage method that are useful for a notification
type TelegramMessage struct {
	// Text is the text of the message (required)
	// Telegram rejects texts longer than 4096 characters after the formatting is parsed, and the error is returned by SendWebhook
	Text string
	// ParseMode is the optional formatting of the text
	// If empty, the text is sent as-is
	ParseMode TelegramParseMode
	// DisableNotification sends the message silently, so users receive a notification with no sound
	DisableNotification bool
	// DisableLinkPreview disables the preview of the links in the message
	DisableLinkPreview bool
}

// telegramChat is the destination of the messages, from the query string of the webhook URL
type telegramChat struct {
	// Numeric ID of the chat, or username of the channel in the format "@channelusername"
	chatID string
	// ID of the topic in a forum supergroup, or zero to send to the main chat
	messageThreadID int64
}

// telegramSendMessage is the body of a request to the sendMessage method (https://core.telegram.org/bots/api#sendmessage)
type telegramSendMessage struct {
	ChatID              string                      `json:"chat_id"`
	MessageThreadID     int64                       `json:"message_thread_id,omitempty"`
	Text                string                      `json:"text"`
	ParseMode           TelegramParseMode           `json:"parse_mode,omitempty"`
	DisableNotification bool                        `json:"disable_notification,omitempty"`
	LinkPreviewOptions  *telegramLinkPreviewOptions `json:"link_preview_options,omitempty"`
}

type telegramLinkPreviewOptions struct {
	IsDisabled bool `json:"is_disabled"`
}

// sendMessageBody validates the message and returns the body of the request that sends it to the chat
func (m TelegramMessage) sendMessageBody(chat telegramChat) (telegramSendMessage, error) {
	// Telegram rejects messages that are empty after trimming whitespace
	if strings.TrimSpace(m.Text) == "" {
		return telegramSendMessage{}, errors.New("text must not be empty")
	}

	switch m.ParseMode {
	case TelegramParseModeNone, TelegramParseModeHTML, TelegramParseModeMarkdownV2:
		// All good
	default:
		return telegramSendMessage{}, fmt.Errorf("invalid parse mode %q", m.ParseMode)
	}

	body := telegramSendMessage{
		ChatID:              chat.chatID,
		MessageThreadID:     chat.messageThreadID,
		Text:                m.Text,
		ParseMode:           m.ParseMode,
		DisableNotification: m.DisableNotification,
	}
	if m.DisableLinkPreview {
		body.LinkPreviewOptions = &telegramLinkPreviewOptions{IsDisabled: true}
	}

	return body, nil
}

// parseTelegramURL parses a webhook URL for Telegram, such as "https://api.telegram.org/bot<token>/sendMessage?chat_id=<chat>"
// It returns the URL of the sendMessage method without the query string, and the chat from the query string, which is sent in the request body
func parseTelegramURL(webhookURL string) (endpoint string, chat telegramChat, err error) {
	parsed, err := url.Parse(webhookURL)
	if err != nil {
		return "", telegramChat{}, fmt.Errorf("invalid webhook URL: %w", err)
	}

	// The path ends with "/bot<token>", optionally followed by the method name, which is added when missing
	// Method names are case-insensitive for Telegram
	// Anything before "/bot<token>" is allowed, for self-hosted Bot API servers behind a reverse proxy
	path := strings.TrimSuffix(parsed.Path, "/")
	i := strings.LastIndexByte(path, '/')
	if strings.EqualFold(path[i+1:], "sendMessage") {
		path = path[:i]
	}
	token, ok := strings.CutPrefix(path[strings.LastIndexByte(path, '/')+1:], "bot")
	if !ok || token == "" {
		return "", telegramChat{}, errors.New("webhook URL for Telegram must have a path like /bot<token>/sendMessage")
	}

	// The query string selects the chat and nothing else, as the other options come from the message
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return "", telegramChat{}, fmt.Errorf("invalid query string in webhook URL: %w", err)
	}
	for key, values := range query {
		switch key {
		case "chat_id", "message_thread_id":
			if len(values) > 1 {
				return "", telegramChat{}, fmt.Errorf("query parameter %q in webhook URL for Telegram must be set only once", key)
			}
		default:
			return "", telegramChat{}, fmt.Errorf("unsupported query parameter %q in webhook URL for Telegram: only chat_id and message_thread_id are allowed", key)
		}
	}

	// The chat is a numeric ID (negative for groups and channels) or a channel username
	chat.chatID = query.Get("chat_id")
	if chat.chatID == "" {
		return "", telegramChat{}, errors.New("webhook URL for Telegram must include the chat_id query parameter")
	}
	if !isValidTelegramChatID(chat.chatID) {
		return "", telegramChat{}, errors.New("query parameter chat_id in webhook URL for Telegram must be a numeric chat ID or a channel username like @channelusername")
	}

	threadID := query.Get("message_thread_id")
	if threadID != "" {
		chat.messageThreadID, err = strconv.ParseInt(threadID, 10, 64)
		if err != nil || chat.messageThreadID <= 0 {
			return "", telegramChat{}, errors.New("query parameter message_thread_id in webhook URL for Telegram must be a positive integer")
		}
	}

	parsed.Path = path + "/sendMessage"
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.RawFragment = ""

	return parsed.String(), chat, nil
}

// isValidTelegramChatID returns true if the value is a numeric chat ID or a channel username in the format "@channelusername"
func isValidTelegramChatID(val string) bool {
	username, ok := strings.CutPrefix(val, "@")
	if ok {
		return username != ""
	}

	_, err := strconv.ParseInt(val, 10, 64)
	return err == nil
}

// telegramErrorDescription returns the description of the error in the body of a failed Bot API response, such as "Bad Request: chat not found"
// It returns an empty string if the body is not a Bot API response, for example because it comes from a proxy or it was truncated
func telegramErrorDescription(body []byte) string {
	var res struct {
		Description string `json:"description"`
	}
	err := json.Unmarshal(body, &res)
	if err != nil {
		return ""
	}

	return res.Description
}
