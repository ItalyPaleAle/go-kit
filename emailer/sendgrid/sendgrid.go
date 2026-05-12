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
	apiKey string
	from   SendGridEmail
}

func (s *SendGridEmailer) Init(ctx context.Context, opts internal.InitOpts) error {
	const connStringFormat = "sendgrid://<api-key>?fromAddress=<address>&fromName=<name>"

	// Validate the fields in the connection string
	if opts.ConnString.Scheme != "sendgrid" {
		return fmt.Errorf("invalid connection string scheme; required format is '%s'", connStringFormat)
	}
	s.apiKey = opts.ConnString.User.Username()
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
	body := SendGridMessage{
		From:    s.from,
		To:      SendGridEmail{Address: toEmail},
		Subject: subject,
		Text:    message.Text,
		HTML:    message.HTML,
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

	resp, err := http.DefaultClient.Do(req)
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

type SendGridMessage struct {
	From    SendGridEmail `json:"from"`
	To      SendGridEmail `json:"to"`
	Subject string        `json:"subject"`
	Text    string        `json:"text,omitempty"`
	HTML    string        `json:"html,omitempty"`
}
