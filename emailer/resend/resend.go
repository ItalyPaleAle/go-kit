// Package resend implements an Emailer that sends messages through the Resend HTTP API
//
// The connection string has the format:
//
//	resend://<api-key>?fromAddress=<address>&fromName=<name>
//
// The API key is the host part of the URL, and fromName is optional
package resend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/italypaleale/go-kit/emailer/internal"
)

const (
	defaultEndpoint = "https://api.resend.com"
	requestTimeout  = 20 * time.Second
)

// defaultHTTPClient never follows redirects, so the Authorization header is only ever sent to the configured endpoint
var defaultHTTPClient = &http.Client{
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// ResendEmailer is an Emailer that uses Resend
type ResendEmailer struct {
	apiKey     string
	from       string
	endpoint   string
	httpClient *http.Client
}

// Init validates the connection string and stores the API key and sender
func (r *ResendEmailer) Init(ctx context.Context, opts internal.InitOpts) error {
	const connStringFormat = "resend://<api-key>?fromAddress=<address>&fromName=<name>"

	// Validate the fields in the connection string
	if opts.ConnString.Scheme != "resend" {
		return fmt.Errorf("invalid connection string scheme; required format is '%s'", connStringFormat)
	}

	// The API key is exposed as the host
	apiKey := opts.ConnString.Host
	fromAddress := opts.ConnString.Query().Get("fromAddress")
	fromName := opts.ConnString.Query().Get("fromName") // fromName is optional
	if apiKey == "" {
		return fmt.Errorf("invalid connection string: missing Resend API key; required format is '%s'", connStringFormat)
	}
	if fromAddress == "" {
		return fmt.Errorf("invalid connection string: missing from address; required format is '%s'", connStringFormat)
	}

	err := internal.ValidateEmailAddress("from address", fromAddress)
	if err != nil {
		return fmt.Errorf("invalid connection string: %w; required format is '%s'", err, connStringFormat)
	}

	r.apiKey = apiKey
	r.from = internal.FormatFromAddress(fromName, fromAddress)
	r.endpoint = defaultEndpoint

	return nil
}

// SendEmail sends an email using Resend
func (r *ResendEmailer) SendEmail(ctx context.Context, to internal.EmailAddress, subject string, message internal.SendEmailMessage) error {
	err := internal.ValidateEmailAddress("recipient address", to.Address)
	if err != nil {
		return err
	}

	body := resendMessage{
		From:    r.from,
		To:      []string{to.Format()},
		Subject: subject,
		Text:    message.Text,
		HTML:    message.HTML,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("failed to marshal email payload: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	endpoint := r.endpoint
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint+"/emails", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+r.apiKey)
	req.Header.Set("Content-Type", "application/json")

	// Allow tests to inject a local client while defaulting to a client that does not follow redirects in production
	client := r.httpClient
	if client == nil {
		client = defaultHTTPClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return r.responseError(resp)
	}

	return nil
}

// responseError builds an error from a non-2xx response, using Resend's error JSON when present
func (r *ResendEmailer) responseError(resp *http.Response) error {
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))

	var apiErr resendError
	err := json.Unmarshal(respBody, &apiErr)
	if err == nil && apiErr.Message != "" {
		if apiErr.Name != "" {
			return fmt.Errorf("failed to send email (%d): %s: %s", resp.StatusCode, apiErr.Name, apiErr.Message)
		}
		return fmt.Errorf("failed to send email (%d): %s", resp.StatusCode, apiErr.Message)
	}

	// Rate limited responses may carry a hint on when to retry
	retryAfter := resp.Header.Get("Retry-After")
	if resp.StatusCode == http.StatusTooManyRequests && retryAfter != "" {
		return fmt.Errorf("failed to send email (%d): rate limit exceeded, retry after %s seconds", resp.StatusCode, retryAfter)
	}

	return fmt.Errorf("failed to send email (%d): %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
}

// resendMessage is the request body of the send email endpoint
type resendMessage struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	Text    string   `json:"text,omitempty"`
	HTML    string   `json:"html,omitempty"`
}

// resendError is the error body returned by the Resend API
type resendError struct {
	Name       string `json:"name"`
	Message    string `json:"message"`
	StatusCode int    `json:"statusCode"`
}
