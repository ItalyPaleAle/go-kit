package awsses

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	// Validate the translated request outside the handler so the test never fails from a server goroutine
	validateRequest := func(r *http.Request) error {
		if r.Method != http.MethodPost {
			return fmt.Errorf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/v2/email/outbound-emails" {
			return fmt.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			return fmt.Errorf("unexpected content type: %s", r.Header.Get("Content-Type"))
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			return fmt.Errorf("read request body: %w", err)
		}

		var payload map[string]any
		err = json.Unmarshal(body, &payload)
		if err != nil {
			return fmt.Errorf("unmarshal payload: %w", err)
		}

		// Assert the Emailer abstraction is translated into the SES JSON shape we expect
		if payload["FromEmailAddress"] != "Sender <sender@example.com>" {
			return fmt.Errorf("unexpected from address: %#v", payload["FromEmailAddress"])
		}

		destination, ok := payload["Destination"].(map[string]any)
		if !ok {
			return fmt.Errorf("destination has unexpected type: %#v", payload["Destination"])
		}
		if !assert.ObjectsAreEqual([]any{"recipient@example.com"}, destination["ToAddresses"]) {
			return fmt.Errorf("unexpected to addresses: %#v", destination["ToAddresses"])
		}

		content, ok := payload["Content"].(map[string]any)
		if !ok {
			return fmt.Errorf("content has unexpected type: %#v", payload["Content"])
		}
		simple, ok := content["Simple"].(map[string]any)
		if !ok {
			return fmt.Errorf("simple content has unexpected type: %#v", content["Simple"])
		}
		subject, ok := simple["Subject"].(map[string]any)
		if !ok {
			return fmt.Errorf("subject has unexpected type: %#v", simple["Subject"])
		}
		if subject["Data"] != "Hello" {
			return fmt.Errorf("unexpected subject: %#v", subject["Data"])
		}

		bodyMap, ok := simple["Body"].(map[string]any)
		if !ok {
			return fmt.Errorf("body has unexpected type: %#v", simple["Body"])
		}
		text, ok := bodyMap["Text"].(map[string]any)
		if !ok {
			return fmt.Errorf("text body has unexpected type: %#v", bodyMap["Text"])
		}
		if text["Data"] != "Plain body" {
			return fmt.Errorf("unexpected text body: %#v", text["Data"])
		}
		html, ok := bodyMap["Html"].(map[string]any)
		if !ok {
			return fmt.Errorf("html body has unexpected type: %#v", bodyMap["Html"])
		}
		if html["Data"] != "<p>HTML body</p>" {
			return fmt.Errorf("unexpected html body: %#v", html["Data"])
		}

		return nil
	}
	handlerErrCh := make(chan error, 1)

	// Capture the outgoing request so the test can validate the translated SES payload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := validateRequest(r)
		handlerErrCh <- err
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

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
	require.NoError(t, <-handlerErrCh)
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
	require.ErrorContains(t, err, "failed to send email (400):")
	require.ErrorContains(t, err, "MessageRejected")
}
