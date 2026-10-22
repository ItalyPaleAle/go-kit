package webhook

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTelegramTestClient(t *testing.T, url string, opts NewWebhookOpts) *webhookClient {
	t.Helper()

	opts.URL = url
	opts.Format = FormatTelegram
	opts.AllowPrivateIPs = true
	wh, _ := newTestWebhookClient(t, opts)
	return wh
}

func TestNewWebhookTelegramURL(t *testing.T) {
	t.Run("valid URLs", func(t *testing.T) {
		tests := []struct {
			name             string
			url              string
			expectedEndpoint string
			expectedChat     telegramChat
		}{
			{
				name:             "full URL",
				url:              "https://api.telegram.org/bot123:ABC-def_456/sendMessage?chat_id=-1001234567890",
				expectedEndpoint: "https://api.telegram.org/bot123:ABC-def_456/sendMessage",
				expectedChat:     telegramChat{chatID: "-1001234567890"},
			},
			{
				name:             "without the method",
				url:              "https://api.telegram.org/bot123:ABC?chat_id=42",
				expectedEndpoint: "https://api.telegram.org/bot123:ABC/sendMessage",
				expectedChat:     telegramChat{chatID: "42"},
			},
			{
				name:             "without the method and with a trailing slash",
				url:              "https://api.telegram.org/bot123:ABC/?chat_id=42",
				expectedEndpoint: "https://api.telegram.org/bot123:ABC/sendMessage",
				expectedChat:     telegramChat{chatID: "42"},
			},
			{
				name:             "method with a trailing slash",
				url:              "https://api.telegram.org/bot123:ABC/sendMessage/?chat_id=42",
				expectedEndpoint: "https://api.telegram.org/bot123:ABC/sendMessage",
				expectedChat:     telegramChat{chatID: "42"},
			},
			{
				name:             "method in lowercase",
				url:              "https://api.telegram.org/bot123:ABC/sendmessage?chat_id=42",
				expectedEndpoint: "https://api.telegram.org/bot123:ABC/sendMessage",
				expectedChat:     telegramChat{chatID: "42"},
			},
			{
				name:             "channel username",
				url:              "https://api.telegram.org/bot123:ABC/sendMessage?chat_id=@mychannel",
				expectedEndpoint: "https://api.telegram.org/bot123:ABC/sendMessage",
				expectedChat:     telegramChat{chatID: "@mychannel"},
			},
			{
				name:             "percent-encoded channel username",
				url:              "https://api.telegram.org/bot123:ABC/sendMessage?chat_id=%40mychannel",
				expectedEndpoint: "https://api.telegram.org/bot123:ABC/sendMessage",
				expectedChat:     telegramChat{chatID: "@mychannel"},
			},
			{
				name:             "topic of a forum supergroup",
				url:              "https://api.telegram.org/bot123:ABC/sendMessage?chat_id=-1001234567890&message_thread_id=7",
				expectedEndpoint: "https://api.telegram.org/bot123:ABC/sendMessage",
				expectedChat:     telegramChat{chatID: "-1001234567890", messageThreadID: 7},
			},
			{
				name:             "self-hosted server with a port and a path prefix",
				url:              "http://telegram.example.com:8081/api/bot123:ABC/sendMessage?chat_id=42#fragment",
				expectedEndpoint: "http://telegram.example.com:8081/api/bot123:ABC/sendMessage",
				expectedChat:     telegramChat{chatID: "42"},
			},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				whAny, err := NewWebhook(NewWebhookOpts{URL: tc.url, Format: FormatTelegram})
				require.NoError(t, err)

				wh := whAny.(*webhookClient) //nolint:forcetypeassert
				assert.Equal(t, FormatTelegram, wh.format)
				assert.Equal(t, tc.expectedEndpoint, wh.webhookURL)
				assert.Equal(t, tc.expectedChat, wh.telegramChat)
			})
		}
	})

	t.Run("invalid URLs", func(t *testing.T) {
		tests := []struct {
			name      string
			url       string
			expectErr string
		}{
			{name: "malformed", url: "://bad url", expectErr: "invalid webhook URL"},
			{name: "disallowed scheme", url: "ftp://api.telegram.org/bot123:ABC?chat_id=42", expectErr: "only http and https are permitted"},
			{name: "without host", url: "https:///bot123:ABC?chat_id=42", expectErr: "must include a host"},
			{name: "without path", url: "https://api.telegram.org?chat_id=42", expectErr: "must have a path like /bot<token>/sendMessage"},
			{name: "token without the bot prefix", url: "https://api.telegram.org/123:ABC/sendMessage?chat_id=42", expectErr: "must have a path like /bot<token>/sendMessage"},
			{name: "empty token", url: "https://api.telegram.org/bot/sendMessage?chat_id=42", expectErr: "must have a path like /bot<token>/sendMessage"},
			{name: "another method", url: "https://api.telegram.org/bot123:ABC/sendPhoto?chat_id=42", expectErr: "must have a path like /bot<token>/sendMessage"},
			{name: "without chat_id", url: "https://api.telegram.org/bot123:ABC/sendMessage", expectErr: "must include the chat_id query parameter"},
			{name: "empty chat_id", url: "https://api.telegram.org/bot123:ABC/sendMessage?chat_id=", expectErr: "must include the chat_id query parameter"},
			{name: "chat_id that is not a number", url: "https://api.telegram.org/bot123:ABC/sendMessage?chat_id=mychannel", expectErr: "must be a numeric chat ID or a channel username"},
			{name: "chat_id with an empty username", url: "https://api.telegram.org/bot123:ABC/sendMessage?chat_id=@", expectErr: "must be a numeric chat ID or a channel username"},
			{name: "repeated chat_id", url: "https://api.telegram.org/bot123:ABC/sendMessage?chat_id=1&chat_id=2", expectErr: `query parameter "chat_id" in webhook URL for Telegram must be set only once`},
			{name: "message_thread_id that is not a number", url: "https://api.telegram.org/bot123:ABC/sendMessage?chat_id=42&message_thread_id=abc", expectErr: "message_thread_id in webhook URL for Telegram must be a positive integer"},
			{name: "zero message_thread_id", url: "https://api.telegram.org/bot123:ABC/sendMessage?chat_id=42&message_thread_id=0", expectErr: "message_thread_id in webhook URL for Telegram must be a positive integer"},
			{name: "negative message_thread_id", url: "https://api.telegram.org/bot123:ABC/sendMessage?chat_id=42&message_thread_id=-3", expectErr: "message_thread_id in webhook URL for Telegram must be a positive integer"},
			{name: "unsupported query parameter", url: "https://api.telegram.org/bot123:ABC/sendMessage?chat_id=42&parse_mode=HTML", expectErr: `unsupported query parameter "parse_mode"`},
			{name: "malformed query string", url: "https://api.telegram.org/bot123:ABC/sendMessage?chat_id=%zz", expectErr: "invalid query string in webhook URL"},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				wh, err := NewWebhook(NewWebhookOpts{URL: tc.url, Format: FormatTelegram})
				require.Nil(t, wh)
				require.Error(t, err)
				require.ErrorContains(t, err, "webhook URL validation failed")
				require.ErrorContains(t, err, tc.expectErr)
			})
		}
	})
}

func TestWebhookTelegram(t *testing.T) {
	const okResponse = `{"ok":true,"result":{"message_id":1}}`

	t.Run("sends the message as JSON to the sendMessage method", func(t *testing.T) {
		srv, requests := newRecordingServer(t, http.StatusOK, okResponse)
		wh := newTelegramTestClient(t, srv.URL+"/bot123:ABC?chat_id=-1001234567890&message_thread_id=42", NewWebhookOpts{})

		err := wh.SendWebhook(t.Context(), testMessageProvider{telegramMessage: TelegramMessage{
			Text:                "<b>Disk full</b> on nas & co",
			ParseMode:           TelegramParseModeHTML,
			DisableNotification: true,
			DisableLinkPreview:  true,
		}})
		require.NoError(t, err)

		reqs := requests()
		require.Len(t, reqs, 1)
		assert.Equal(t, http.MethodPost, reqs[0].method)
		assert.Equal(t, "/bot123:ABC/sendMessage", reqs[0].path)
		assert.Empty(t, reqs[0].rawQuery)
		assert.Equal(t, "application/json", reqs[0].header.Get("Content-Type"))
		assert.Empty(t, reqs[0].header.Get("Authorization"))
		assert.JSONEq(t, `{
			"chat_id": "-1001234567890",
			"message_thread_id": 42,
			"text": "<b>Disk full</b> on nas & co",
			"parse_mode": "HTML",
			"disable_notification": true,
			"link_preview_options": {"is_disabled": true}
		}`, reqs[0].body)
		// HTML is not escaped in the JSON
		assert.Contains(t, reqs[0].body, `"<b>Disk full</b> on nas & co"`)
	})

	t.Run("a plain-text message only sends the chat and the text", func(t *testing.T) {
		srv, requests := newRecordingServer(t, http.StatusOK, okResponse)
		wh := newTelegramTestClient(t, srv.URL+"/bot123:ABC/sendMessage?chat_id=@mychannel", NewWebhookOpts{})

		err := wh.SendWebhook(t.Context(), StringMessage("Disk usage is at 95% 🔥"))
		require.NoError(t, err)

		reqs := requests()
		require.Len(t, reqs, 1)
		assert.JSONEq(t, `{"chat_id":"@mychannel","text":"Disk usage is at 95% 🔥"}`, reqs[0].body)
	})

	t.Run("MarkdownV2 parse mode", func(t *testing.T) {
		srv, requests := newRecordingServer(t, http.StatusOK, okResponse)
		wh := newTelegramTestClient(t, srv.URL+"/bot123:ABC/sendMessage?chat_id=42", NewWebhookOpts{})

		err := wh.SendWebhook(t.Context(), testMessageProvider{telegramMessage: TelegramMessage{
			Text:      "*bold* text\\.",
			ParseMode: TelegramParseModeMarkdownV2,
		}})
		require.NoError(t, err)

		reqs := requests()
		require.Len(t, reqs, 1)
		assert.JSONEq(t, `{"chat_id":"42","text":"*bold* text\\.","parse_mode":"MarkdownV2"}`, reqs[0].body)
	})

	t.Run("the key is ignored", func(t *testing.T) {
		srv, requests := newRecordingServer(t, http.StatusOK, okResponse)
		wh := newTelegramTestClient(t, srv.URL+"/bot123:ABC/sendMessage?chat_id=42", NewWebhookOpts{Key: "Bearer proxy-key", AuthorizationHeader: "X-Auth"})

		err := wh.SendWebhook(t.Context(), StringMessage("text"))
		require.NoError(t, err)

		reqs := requests()
		require.Len(t, reqs, 1)
		assert.Empty(t, reqs[0].header.Get("X-Auth"))
		assert.Empty(t, reqs[0].header.Get("Authorization"))
	})

	t.Run("invalid messages are rejected without sending a request", func(t *testing.T) {
		tests := []struct {
			name   string
			msg    TelegramMessage
			expect string
		}{
			{name: "empty text", msg: TelegramMessage{ParseMode: TelegramParseModeHTML}, expect: "text must not be empty"},
			{name: "whitespace-only text", msg: TelegramMessage{Text: " \n\t "}, expect: "text must not be empty"},
			{name: "legacy Markdown parse mode", msg: TelegramMessage{Text: "x", ParseMode: "Markdown"}, expect: `invalid parse mode "Markdown"`},
			{name: "unknown parse mode", msg: TelegramMessage{Text: "x", ParseMode: "html"}, expect: `invalid parse mode "html"`},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				srv, requests := newRecordingServer(t, http.StatusOK, okResponse)
				wh := newTelegramTestClient(t, srv.URL+"/bot123:ABC/sendMessage?chat_id=42", NewWebhookOpts{})

				err := wh.SendWebhook(t.Context(), testMessageProvider{telegramMessage: tc.msg})
				require.Error(t, err)
				require.ErrorContains(t, err, "failed to create request")
				require.ErrorContains(t, err, "invalid Telegram message")
				require.ErrorContains(t, err, tc.expect)
				assert.Empty(t, requests())
			})
		}
	})

	t.Run("an empty plain-text message is rejected without sending a request", func(t *testing.T) {
		srv, requests := newRecordingServer(t, http.StatusOK, okResponse)
		wh := newTelegramTestClient(t, srv.URL+"/bot123:ABC/sendMessage?chat_id=42", NewWebhookOpts{})

		err := wh.SendWebhook(t.Context(), StringMessage(""))
		require.Error(t, err)
		require.ErrorContains(t, err, "text must not be empty")
		assert.Empty(t, requests())
	})

	t.Run("errors from the provider are permanent", func(t *testing.T) {
		srv, requests := newRecordingServer(t, http.StatusOK, okResponse)
		wh := newTelegramTestClient(t, srv.URL+"/bot123:ABC/sendMessage?chat_id=42", NewWebhookOpts{})

		err := wh.SendWebhook(t.Context(), testMessageProvider{telegramErr: errors.New("boom")})
		require.Error(t, err)
		require.ErrorContains(t, err, "error getting Telegram message")
		require.ErrorContains(t, err, "boom")
		assert.Empty(t, requests())
	})

	t.Run("error responses include the description from Telegram", func(t *testing.T) {
		srv, requests := newRecordingServer(t, http.StatusBadRequest, `{"ok":false,"error_code":400,"description":"Bad Request: message is too long"}`)
		wh := newTelegramTestClient(t, srv.URL+"/bot123:ABC/sendMessage?chat_id=42", NewWebhookOpts{})

		err := wh.SendWebhook(t.Context(), StringMessage("long message"))
		require.Error(t, err)
		require.ErrorContains(t, err, "failed to send webhook after 1 attempts")
		require.ErrorContains(t, err, "invalid response status code: 400 (Bad Request: message is too long)")
		assert.Len(t, requests(), 1)
	})

	t.Run("error responses without a description report the status code", func(t *testing.T) {
		srv, _ := newRecordingServer(t, http.StatusForbidden, "<html>Forbidden</html>")
		wh := newTelegramTestClient(t, srv.URL+"/bot123:ABC/sendMessage?chat_id=42", NewWebhookOpts{})

		err := wh.SendWebhook(t.Context(), StringMessage("text"))
		require.Error(t, err)
		require.ErrorContains(t, err, "invalid response status code: 403")
		require.NotContains(t, err.Error(), "(")
	})

	t.Run("other formats do not include the description", func(t *testing.T) {
		srv, _ := newRecordingServer(t, http.StatusBadRequest, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`)
		wh, _ := newTestWebhookClient(t, NewWebhookOpts{URL: srv.URL, AllowPrivateIPs: true})

		err := wh.SendWebhook(t.Context(), StringMessage("text"))
		require.Error(t, err)
		require.ErrorContains(t, err, "invalid response status code: 400")
		require.NotContains(t, err.Error(), "chat not found")
	})
}
