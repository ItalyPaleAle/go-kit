package webhook

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newGoogleChatTestClient(t *testing.T, url string, opts NewWebhookOpts) *webhookClient {
	t.Helper()

	opts.URL = url
	opts.Format = FormatGoogleChat
	opts.AllowPrivateIPs = true
	wh, _ := newTestWebhookClient(t, opts)
	return wh
}

func TestNewWebhookGoogleChatURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		// Empty when the URL must be accepted
		expectErr string
	}{
		{name: "webhook URL", url: "https://chat.googleapis.com/v1/spaces/AAAAbcd123/messages?key=AIzaSy&token=abc%3D"},
		{name: "trailing slash", url: "https://chat.googleapis.com/v1/spaces/AAAAbcd123/messages/?key=AIzaSy&token=abc"},
		{name: "with the reply option", url: "https://chat.googleapis.com/v1/spaces/AAAAbcd123/messages?key=AIzaSy&token=abc&messageReplyOption=REPLY_MESSAGE_OR_FAIL"},
		{name: "without host", url: "https:///v1/spaces/AAAAbcd123/messages?key=AIzaSy&token=abc", expectErr: "must include a host"},
		{name: "without path", url: "https://chat.googleapis.com?key=AIzaSy&token=abc", expectErr: "must have a path like /v1/spaces/<space>/messages"},
		{name: "URL of the space", url: "https://chat.googleapis.com/v1/spaces/AAAAbcd123?key=AIzaSy&token=abc", expectErr: "must have a path like /v1/spaces/<space>/messages"},
		{name: "without the space", url: "https://chat.googleapis.com/v1/spaces//messages?key=AIzaSy&token=abc", expectErr: "must have a path like /v1/spaces/<space>/messages"},
		{name: "without key", url: "https://chat.googleapis.com/v1/spaces/AAAAbcd123/messages?token=abc", expectErr: "must include the key and token query parameters"},
		{name: "without token", url: "https://chat.googleapis.com/v1/spaces/AAAAbcd123/messages?key=AIzaSy", expectErr: "must include the key and token query parameters"},
		{name: "empty token", url: "https://chat.googleapis.com/v1/spaces/AAAAbcd123/messages?key=AIzaSy&token=", expectErr: "must include the key and token query parameters"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			whAny, err := NewWebhook(NewWebhookOpts{URL: tc.url, Format: FormatGoogleChat})
			if tc.expectErr == "" {
				require.NoError(t, err)
				wh := whAny.(*webhookClient) //nolint:forcetypeassert
				assert.Equal(t, FormatGoogleChat, wh.format)
				assert.Equal(t, tc.url, wh.webhookURL)
				return
			}

			require.Nil(t, whAny)
			require.Error(t, err)
			require.ErrorContains(t, err, "webhook URL validation failed")
			require.ErrorContains(t, err, tc.expectErr)
		})
	}
}

func TestWebhookGoogleChat(t *testing.T) {
	const (
		path       = "/v1/spaces/AAAAbcd123/messages"
		query      = "key=AIzaSy&token=abc%3D"
		okResponse = `{"name":"spaces/AAAAbcd123/messages/1","thread":{"name":"spaces/AAAAbcd123/threads/1"}}`
	)

	t.Run("sends the text as JSON", func(t *testing.T) {
		srv, requests := newRecordingServer(t, http.StatusOK, okResponse)
		wh := newGoogleChatTestClient(t, srv.URL+path+"?"+query, NewWebhookOpts{})

		err := wh.SendWebhook(t.Context(), testMessageProvider{googleChatMessage: GoogleChatMessage{
			Text: "*Disk full* on <https://status.example.com/nas|nas> & co",
		}})
		require.NoError(t, err)

		reqs := requests()
		require.Len(t, reqs, 1)
		assert.Equal(t, http.MethodPost, reqs[0].method)
		assert.Equal(t, path, reqs[0].path)
		assert.Equal(t, query, reqs[0].rawQuery)
		assert.Equal(t, "application/json; charset=UTF-8", reqs[0].header.Get("Content-Type"))
		assert.Empty(t, reqs[0].header.Get("Authorization"))
		assert.JSONEq(t, `{"text":"*Disk full* on <https://status.example.com/nas|nas> & co"}`, reqs[0].body)
		// The links are not escaped in the JSON
		assert.Contains(t, reqs[0].body, "<https://status.example.com/nas|nas>")
	})

	t.Run("a plain-text message is sent as the text", func(t *testing.T) {
		srv, requests := newRecordingServer(t, http.StatusOK, okResponse)
		wh := newGoogleChatTestClient(t, srv.URL+path+"?"+query, NewWebhookOpts{})

		err := wh.SendWebhook(t.Context(), StringMessage("Disk usage is at 95% 🔥"))
		require.NoError(t, err)

		reqs := requests()
		require.Len(t, reqs, 1)
		assert.JSONEq(t, `{"text":"Disk usage is at 95% 🔥"}`, reqs[0].body)
	})

	t.Run("a thread key replies in the thread, or starts it", func(t *testing.T) {
		srv, requests := newRecordingServer(t, http.StatusOK, okResponse)
		wh := newGoogleChatTestClient(t, srv.URL+path+"?"+query, NewWebhookOpts{})

		err := wh.SendWebhook(t.Context(), testMessageProvider{googleChatMessage: GoogleChatMessage{Text: "Disk full", ThreadKey: "nas"}})
		require.NoError(t, err)

		reqs := requests()
		require.Len(t, reqs, 1)
		assert.Equal(t, path, reqs[0].path)
		assert.Equal(t, query+"&messageReplyOption=REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD", reqs[0].rawQuery)
		assert.JSONEq(t, `{"text":"Disk full","thread":{"threadKey":"nas"}}`, reqs[0].body)

		// The configured URL is not changed for the next messages
		assert.Equal(t, srv.URL+path+"?"+query, wh.webhookURL)
	})

	t.Run("a thread key keeps the reply option in the URL", func(t *testing.T) {
		srv, requests := newRecordingServer(t, http.StatusOK, okResponse)
		wh := newGoogleChatTestClient(t, srv.URL+path+"?"+query+"&messageReplyOption=REPLY_MESSAGE_OR_FAIL", NewWebhookOpts{})

		err := wh.SendWebhook(t.Context(), testMessageProvider{googleChatMessage: GoogleChatMessage{Text: "Disk full", ThreadKey: "nas"}})
		require.NoError(t, err)

		reqs := requests()
		require.Len(t, reqs, 1)
		assert.Equal(t, query+"&messageReplyOption=REPLY_MESSAGE_OR_FAIL", reqs[0].rawQuery)
	})

	t.Run("invalid messages are rejected without sending a request", func(t *testing.T) {
		tests := []struct {
			name string
			msg  MessageProvider
		}{
			{name: "empty text", msg: testMessageProvider{googleChatMessage: GoogleChatMessage{ThreadKey: "nas"}}},
			{name: "whitespace-only text", msg: testMessageProvider{googleChatMessage: GoogleChatMessage{Text: " \n\t "}}},
			{name: "empty plain-text message", msg: StringMessage("")},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				srv, requests := newRecordingServer(t, http.StatusOK, okResponse)
				wh := newGoogleChatTestClient(t, srv.URL+path+"?"+query, NewWebhookOpts{})

				err := wh.SendWebhook(t.Context(), tc.msg)
				require.Error(t, err)
				require.ErrorContains(t, err, "failed to create request")
				require.ErrorContains(t, err, "invalid Google Chat message: text must not be empty")
				assert.Empty(t, requests())
			})
		}
	})

	t.Run("errors from the provider are permanent", func(t *testing.T) {
		srv, requests := newRecordingServer(t, http.StatusOK, okResponse)
		wh := newGoogleChatTestClient(t, srv.URL+path+"?"+query, NewWebhookOpts{})

		err := wh.SendWebhook(t.Context(), testMessageProvider{googleChatErr: errors.New("boom")})
		require.Error(t, err)
		require.ErrorContains(t, err, "error getting Google Chat message")
		require.ErrorContains(t, err, "boom")
		assert.Empty(t, requests())
	})

	t.Run("error responses include the message from Google", func(t *testing.T) {
		srv, requests := newRecordingServer(t, http.StatusBadRequest, `{"error":{"code":400,"message":"Message cannot be empty.","status":"INVALID_ARGUMENT"}}`)
		wh := newGoogleChatTestClient(t, srv.URL+path+"?"+query, NewWebhookOpts{})

		err := wh.SendWebhook(t.Context(), StringMessage("text"))
		require.Error(t, err)
		require.ErrorContains(t, err, "failed to send webhook after 1 attempts")
		require.ErrorContains(t, err, "invalid response status code: 400 (Message cannot be empty.)")
		assert.Len(t, requests(), 1)
	})
}
