package awsses

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/italypaleale/go-kit/emailer/internal"
)

func TestInit(t *testing.T) {
	// Use a full connection string so Init exercises the same parsing path as production
	connString, err := url.Parse("awsses://key:secret@eu-west-1?fromAddress=sender@example.com&fromName=Sender+Name")
	require.NoError(t, err)

	var emailer AWSSES
	err = emailer.Init(t.Context(), internal.InitOpts{ConnString: connString})
	require.NoError(t, err)

	// Verify the parsed configuration is stored in the shape expected by SendEmail
	assert.Equal(t, "key", emailer.accessKeyID)
	assert.Equal(t, "secret", emailer.secretAccessKey)
	assert.Equal(t, "eu-west-1", emailer.region)
	assert.Equal(t, "Sender Name <sender@example.com>", emailer.from)
	assert.Equal(t, "https://email.eu-west-1.amazonaws.com", emailer.endpoint)
	require.NotNil(t, emailer.now)
}

func TestSendEmail(t *testing.T) {
	// Capture the outgoing request so the test can validate the translated SES payload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v2/email/outbound-emails", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		var payload map[string]any
		err = json.Unmarshal(body, &payload)
		require.NoError(t, err)

		// Assert the Emailer abstraction is translated into the SES JSON shape we expect
		assert.Equal(t, "Sender <sender@example.com>", payload["FromEmailAddress"])

		destination, ok := payload["Destination"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, []any{"recipient@example.com"}, destination["ToAddresses"])

		content, ok := payload["Content"].(map[string]any)
		require.True(t, ok)
		simple, ok := content["Simple"].(map[string]any)
		require.True(t, ok)
		subject, ok := simple["Subject"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "Hello", subject["Data"])

		bodyMap, ok := simple["Body"].(map[string]any)
		require.True(t, ok)
		text, ok := bodyMap["Text"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "Plain body", text["Data"])
		html, ok := bodyMap["Html"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "<p>HTML body</p>", html["Data"])

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"MessageId":"msg-123"}`))
	}))
	t.Cleanup(server.Close)

	// Inject the local server so the test stays hermetic and never talks to AWS
	emailer := AWSSES{
		accessKeyID:     "access-key",
		secretAccessKey: "secret-key",
		region:          "us-east-1",
		from:            "Sender <sender@example.com>",
		endpoint:        server.URL,
		httpClient:      server.Client(),
	}

	// Send both text and HTML content because the request builder handles them differently
	err := emailer.SendEmail(t.Context(), "recipient@example.com", "Hello", internal.SendEmailMessage{
		Text: "Plain body",
		HTML: "<p>HTML body</p>",
	})
	require.NoError(t, err)
}

func TestSendEmailReturnsRemoteErrors(t *testing.T) {
	// Return an SES-like rejection so the test can verify error propagation from the response body
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "MessageRejected", http.StatusBadRequest)
	}))
	t.Cleanup(server.Close)

	// Point the emailer at the local server so only the response handling path is exercised
	emailer := AWSSES{
		accessKeyID:     "access-key",
		secretAccessKey: "secret-key",
		region:          "us-east-1",
		from:            "sender@example.com",
		endpoint:        server.URL,
		httpClient:      server.Client(),
		now:             time.Now,
	}

	// The returned error should preserve both the status code and the SES message text
	err := emailer.SendEmail(t.Context(), "recipient@example.com", "Hello", internal.SendEmailMessage{Text: "Body"})
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "failed to send email (400):"))
	assert.True(t, strings.Contains(err.Error(), "MessageRejected"))
}
