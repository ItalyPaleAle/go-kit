package awsses

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

// AWSSES is an Emailer that uses AWS SES
type AWSSES struct {
	accessKeyID     string
	secretAccessKey string
	sessionToken    string
	region          string
	from            string
	endpoint        string
	httpClient      *http.Client
	now             func() time.Time
}

// Init validates the connection string and stores the static AWS configuration used for signed SES requests
func (a *AWSSES) Init(ctx context.Context, opts internal.InitOpts) error {
	const connStringFormat = "awsses://<access-key-id>:<secret-access-key>@<region>?fromAddress=<address>&fromName=<name>"

	// Validate the fields in the connection string
	if opts.ConnString.Scheme != "awsses" {
		return fmt.Errorf("invalid connection string scheme; required format is '%s'", connStringFormat)
	}
	accessKeyID := opts.ConnString.User.Username()
	secretAccessKey, _ := opts.ConnString.User.Password()
	region := opts.ConnString.Hostname()
	if accessKeyID == "" || secretAccessKey == "" {
		return fmt.Errorf("invalid connection string: missing AWS access key ID and/or secret access key; required format is '%s'", connStringFormat)
	}
	if region == "" {
		return fmt.Errorf("invalid connection string: missing AWS region; required format is '%s'", connStringFormat)
	}
	fromAddress := opts.ConnString.Query().Get("fromAddress")
	fromName := opts.ConnString.Query().Get("fromName") // fromName is optional
	if fromAddress == "" {
		return fmt.Errorf("invalid connection string: missing from address; required format is '%s'", connStringFormat)
	}

	err := internal.ValidateEmailAddress("from address", fromAddress)
	if err != nil {
		return fmt.Errorf("invalid connection string: %w; required format is '%s'", err, connStringFormat)
	}

	// Persist the static credentials and endpoint so SendEmail can stay allocation-light
	a.accessKeyID = accessKeyID
	a.secretAccessKey = secretAccessKey
	a.sessionToken = opts.ConnString.Query().Get("sessionToken")
	a.region = region
	a.endpoint = "https://email." + region + ".amazonaws.com"
	if a.now == nil {
		a.now = time.Now
	}

	// Preserve the caller's display name in the From header when one is provided
	a.from = internal.FormatFromAddress(fromName, fromAddress)

	return nil
}

// SendEmail posts a simple SES v2 payload and signs the request with AWS Signature Version 4
func (a AWSSES) SendEmail(ctx context.Context, toEmail string, subject string, message internal.SendEmailMessage) error {
	err := internal.ValidateEmailAddress("recipient address", toEmail)
	if err != nil {
		return err
	}

	// Build the smallest SES v2 payload that matches the Emailer interface
	payloadBody := sendEmailRequest{
		Content: sendEmailContent{
			Simple: &sendEmailMessage{
				Body: sendEmailBody{
					Text: &sendEmailContentValue{
						Charset: "UTF-8",
						Data:    message.Text,
					},
				},
				Subject: sendEmailContentValue{
					Charset: "UTF-8",
					Data:    subject,
				},
			},
		},
		Destination: sendEmailDestination{
			ToAddresses: []string{toEmail},
		},
		FromEmailAddress: a.from,
	}
	if message.HTML != "" {
		payloadBody.Content.Simple.Body.HTML = &sendEmailContentValue{
			Charset: "UTF-8",
			Data:    message.HTML,
		}
	}

	// Encode the request body once so the same bytes can be signed and transmitted
	payload, err := json.Marshal(payloadBody)
	if err != nil {
		return fmt.Errorf("failed to marshal email payload: %w", err)
	}

	// Bound the outbound request so a slow SES endpoint does not stall the caller indefinitely
	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, a.endpoint+"/v2/email/outbound-emails", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Use the injected clock in tests while remaining safe for manually constructed instances
	requestTime := time.Now()
	if a.now != nil {
		requestTime = a.now()
	}
	requestTime = requestTime.UTC()

	// Sign the final request bytes so SES can authenticate the caller without the AWS SDK
	err = a.signRequest(req, payload, requestTime)
	if err != nil {
		return fmt.Errorf("failed to sign request: %w", err)
	}

	// Allow tests to inject a local client while defaulting to the shared HTTP transport in production
	client := a.httpClient
	if client == nil {
		client = http.DefaultClient
	}

	// Always drain and close the response body so the connection can be reused by the transport
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	// Bubble up the SES response body because it usually contains the rejection reason
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return fmt.Errorf("failed to send email (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return nil
}
