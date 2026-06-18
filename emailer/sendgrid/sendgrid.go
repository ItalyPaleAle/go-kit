package sendgrid

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/italypaleale/go-kit/emailer/internal"
)

// SendGridEmailer is an Emailer that uses SendGrid.
type SendGridEmailer struct {
	apiKey     string
	from       SendGridEmail
	httpClient *http.Client
}

func (s *SendGridEmailer) Init(ctx context.Context, opts internal.InitOpts) error {
	const connStringFormat = "sendgrid://<api-key>?fromAddress=<address>&fromName=<name>"

	// Validate the fields in the connection string
	if opts.ConnString.Scheme != "sendgrid" {
		return fmt.Errorf("invalid connection string scheme; required format is '%s'", connStringFormat)
	}
	// The API key is exposed as the host
	s.apiKey = opts.ConnString.Host
	s.from.Address = opts.ConnString.Query().Get("fromAddress")
	s.from.Name = opts.ConnString.Query().Get("fromName") // fromName is optional
	if s.apiKey == "" {
		return fmt.Errorf("invalid connection string: missing SendGrid API key; required format is '%s'", connStringFormat)
	}
	if s.from.Address == "" {
		return fmt.Errorf("invalid connection string: missing from address; required format is '%s'", connStringFormat)
	}

	return nil
}

// SendEmail sends an email using SendGrid.
func (s *SendGridEmailer) SendEmail(ctx context.Context, toEmail string, subject string, message internal.SendEmailMessage) error {
	// Build the v3 mail/send content array with text/plain first, adding text/html only when present
	content := make([]SendGridContent, 1, 2)
	content[0] = SendGridContent{Type: "text/plain", Value: message.Text}
	if message.HTML != "" {
		content = append(content, SendGridContent{Type: "text/html", Value: message.HTML})
	}

	// Recipients must live inside the personalizations array, not at the top level
	body := SendGridMessage{
		Personalizations: []SendGridPersonalization{
			{To: []SendGridEmail{{Address: toEmail}}},
		},
		From:    s.from,
		Subject: subject,
		Content: content,
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("failed to marshal email payload: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, "https://api.sendgrid.com/v3/mail/send", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")

	// Allow tests to inject a local client while defaulting to the shared HTTP transport in production
	client := s.httpClient
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to send email (%d): %s", resp.StatusCode, string(body))
	}

	return nil
}

type SendGridEmail struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"email"`
}

// SendGridPersonalization is one entry in the v3 mail/send personalizations array
// Each personalization carries the recipients for one copy of the message
type SendGridPersonalization struct {
	To []SendGridEmail `json:"to"`
}

// SendGridContent is one MIME part in the v3 mail/send content array
type SendGridContent struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// SendGridMessage is the v3 mail/send request body
// Recipients live under personalizations[].to[] and bodies under content[], with text/plain ordered before text/html
type SendGridMessage struct {
	Personalizations []SendGridPersonalization `json:"personalizations"`
	From             SendGridEmail             `json:"from"`
	Subject          string                    `json:"subject"`
	Content          []SendGridContent         `json:"content"`
}
