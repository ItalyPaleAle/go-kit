package webhook

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTeamsTestClient(t *testing.T, url string, opts NewWebhookOpts) *webhookClient {
	t.Helper()

	opts.URL = url
	opts.Format = FormatTeams
	opts.AllowPrivateIPs = true
	wh, _ := newTestWebhookClient(t, opts)
	return wh
}

func TestNewWebhookTeamsFormat(t *testing.T) {
	const workflowURL = "https://default0123.environment.api.powerplatform.com:443/powerautomate/automations/direct/workflows/abc123/triggers/manual/paths/invoke?api-version=1&sp=%2Ftriggers%2Fmanual%2Frun&sv=1.0&sig=xyz"

	whAny, err := NewWebhook(NewWebhookOpts{URL: workflowURL, Format: FormatTeams})
	require.NoError(t, err)

	wh := whAny.(*webhookClient) //nolint:forcetypeassert
	assert.Equal(t, FormatTeams, wh.format)
	assert.Equal(t, workflowURL, wh.webhookURL)
}

func TestWebhookTeams(t *testing.T) {
	const (
		path  = "/workflows/abc123/triggers/manual/paths/invoke"
		query = "api-version=1&sp=%2Ftriggers%2Fmanual%2Frun&sv=1.0&sig=xyz"
	)

	t.Run("sends the title and the text as an Adaptive Card", func(t *testing.T) {
		srv, requests := newRecordingServer(t, http.StatusAccepted, "")
		wh := newTeamsTestClient(t, srv.URL+path+"?"+query, NewWebhookOpts{})

		err := wh.SendWebhook(t.Context(), testMessageProvider{teamsMessage: TeamsMessage{
			Title: "Disk full on nas",
			Text:  "Disk usage is at **95%** & rising: [status](https://status.example.com/nas)",
		}})
		require.NoError(t, err)

		reqs := requests()
		require.Len(t, reqs, 1)
		assert.Equal(t, http.MethodPost, reqs[0].method)
		assert.Equal(t, path, reqs[0].path)
		assert.Equal(t, query, reqs[0].rawQuery)
		assert.Equal(t, "application/json", reqs[0].header.Get("Content-Type"))
		assert.Empty(t, reqs[0].header.Get("Authorization"))
		assert.JSONEq(t, `{
			"type": "message",
			"attachments": [{
				"contentType": "application/vnd.microsoft.card.adaptive",
				"content": {
					"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
					"type": "AdaptiveCard",
					"version": "1.2",
					"body": [
						{"type": "TextBlock", "text": "Disk full on nas", "wrap": true, "size": "Medium", "weight": "Bolder"},
						{"type": "TextBlock", "text": "Disk usage is at **95%** & rising: [status](https://status.example.com/nas)", "wrap": true}
					]
				}
			}]
		}`, reqs[0].body)
	})

	t.Run("a plain-text message is sent as the only text block", func(t *testing.T) {
		srv, requests := newRecordingServer(t, http.StatusAccepted, "")
		wh := newTeamsTestClient(t, srv.URL+path+"?"+query, NewWebhookOpts{})

		err := wh.SendWebhook(t.Context(), StringMessage("Disk usage is at 95% 🔥"))
		require.NoError(t, err)

		reqs := requests()
		require.Len(t, reqs, 1)
		assert.JSONEq(t, `{
			"type": "message",
			"attachments": [{
				"contentType": "application/vnd.microsoft.card.adaptive",
				"content": {
					"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
					"type": "AdaptiveCard",
					"version": "1.2",
					"body": [{"type": "TextBlock", "text": "Disk usage is at 95% 🔥", "wrap": true}]
				}
			}]
		}`, reqs[0].body)
	})

	t.Run("a title without text is sent as the only text block", func(t *testing.T) {
		srv, requests := newRecordingServer(t, http.StatusAccepted, "")
		wh := newTeamsTestClient(t, srv.URL+path+"?"+query, NewWebhookOpts{})

		err := wh.SendWebhook(t.Context(), testMessageProvider{teamsMessage: TeamsMessage{Title: "Backup completed", Text: "  "}})
		require.NoError(t, err)

		reqs := requests()
		require.Len(t, reqs, 1)
		assert.Contains(t, reqs[0].body, `"body":[{"type":"TextBlock","text":"Backup completed","wrap":true,"size":"Medium","weight":"Bolder"}]`)
	})

	t.Run("the key is sent in the configured authorization header", func(t *testing.T) {
		srv, requests := newRecordingServer(t, http.StatusAccepted, "")
		wh := newTeamsTestClient(t, srv.URL+path+"?"+query, NewWebhookOpts{Key: "Bearer token", AuthorizationHeader: "X-Auth"})

		err := wh.SendWebhook(t.Context(), StringMessage("text"))
		require.NoError(t, err)

		reqs := requests()
		require.Len(t, reqs, 1)
		assert.Equal(t, "Bearer token", reqs[0].header.Get("X-Auth"))
		assert.Empty(t, reqs[0].header.Get("Authorization"))
	})

	t.Run("invalid messages are rejected without sending a request", func(t *testing.T) {
		tests := []struct {
			name string
			msg  MessageProvider
		}{
			{name: "empty title and text", msg: testMessageProvider{}},
			{name: "whitespace-only title and text", msg: testMessageProvider{teamsMessage: TeamsMessage{Title: " ", Text: "\n\t"}}},
			{name: "empty plain-text message", msg: StringMessage("")},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				srv, requests := newRecordingServer(t, http.StatusAccepted, "")
				wh := newTeamsTestClient(t, srv.URL+path+"?"+query, NewWebhookOpts{})

				err := wh.SendWebhook(t.Context(), tc.msg)
				require.Error(t, err)
				require.ErrorContains(t, err, "failed to create request")
				require.ErrorContains(t, err, "invalid Teams message: title and text must not both be empty")
				assert.Empty(t, requests())
			})
		}
	})

	t.Run("errors from the provider are permanent", func(t *testing.T) {
		srv, requests := newRecordingServer(t, http.StatusAccepted, "")
		wh := newTeamsTestClient(t, srv.URL+path+"?"+query, NewWebhookOpts{})

		err := wh.SendWebhook(t.Context(), testMessageProvider{teamsErr: errors.New("boom")})
		require.Error(t, err)
		require.ErrorContains(t, err, "error getting Teams message")
		require.ErrorContains(t, err, "boom")
		assert.Empty(t, requests())
	})

	t.Run("error responses include the message from Power Automate", func(t *testing.T) {
		srv, requests := newRecordingServer(t, http.StatusBadRequest, `{"error":{"code":"TriggerInputSchemaMismatch","message":"The input body for trigger 'manual' of type 'Request' did not match its schema definition."}}`)
		wh := newTeamsTestClient(t, srv.URL+path+"?"+query, NewWebhookOpts{})

		err := wh.SendWebhook(t.Context(), StringMessage("text"))
		require.Error(t, err)
		require.ErrorContains(t, err, "failed to send webhook after 1 attempts")
		require.ErrorContains(t, err, "invalid response status code: 400 (The input body for trigger 'manual' of type 'Request' did not match its schema definition.)")
		assert.Len(t, requests(), 1)
	})
}
