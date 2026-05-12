package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	kclock "k8s.io/utils/clock"
)

// WebhookFormat is the type for the webhook format
type WebhookFormat string

const (
	FormatSlack   WebhookFormat = "slack"
	FormatDiscord WebhookFormat = "discord"
	FormatPlain   WebhookFormat = "plain"
)

const (
	webhookTimeout       = 20 * time.Second
	retryIntervalSeconds = 20
)

// Webhook client interface
type Webhook interface {
	// SendWebhook sends the notification
	SendWebhook(ctx context.Context, data MessageProvider) error
}

// SlackMessage is the type for a Slack-compatible message
type SlackMessage struct {
	Text string `json:"text"`
}

// MessageProvider is the interface provided by the webhook message data provider
type MessageProvider interface {
	// GetPlainMessage must return the message for the webhook in plain-text format
	GetPlainMessage() (string, error)
	// GetSlackMessage must return the message for the webhook in Slack-compatible format
	GetSlackMessage() (SlackMessage, error)
}

// Webhook client
type webhookClient struct {
	format            WebhookFormat
	webhookURL        string
	webhookKey        string
	webhookAuthHeader string

	log        *slog.Logger
	httpClient *http.Client
	clock      kclock.Clock
}

type NewWebhookOpts struct {
	// URL is the webhook endpoint (required)
	URL string
	// Key is the optional key for the webhook
	// This is passed as-is in the Authorization header, so make sure to include amy prefix (like "Bearer" or "APIKey") if needed
	Key string
	// AuthorizationHeader is the name of the header that includes the authorization key
	// This is ignored when format is "discord" or "slack"
	// If empty, defaults to "Authorization"
	AuthorizationHeader string
	// Format is the webhook format
	// If empty, defaults to "plain"
	Format WebhookFormat
	// Optional logger to use instead of the default slog instance
	Logger *slog.Logger

	clock kclock.Clock
}

// NewWebhook creates a new Webhook
func NewWebhook(opts NewWebhookOpts) (Webhook, error) {
	opts.clock = kclock.RealClock{}
	return newWebhookInternal(opts)
}

func newWebhookInternal(opts NewWebhookOpts) (Webhook, error) {
	// Validate options
	if opts.URL == "" {
		return nil, errors.New("webhook URL is required")
	}
	switch opts.Format {
	case FormatSlack, FormatPlain:
		// All good
	case FormatDiscord:
		// Shorthand for using Slack-compatible webhooks with Discord
		if !strings.HasSuffix(opts.URL, "/slack") {
			opts.URL += "/slack"
		}
	case "":
		// Default to plain
		opts.Format = FormatPlain
	default:
		return nil, fmt.Errorf("invalid webhook format: %q", opts.Format)
	}

	// Validate the webhook URL scheme
	// The custom net.Dialer.Control on the HTTP transport enforces the private-IP block at connect time
	err := validateWebhookScheme(opts.URL)
	if err != nil {
		return nil, fmt.Errorf("webhook URL validation failed: %w", err)
	}

	// Set default logger
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	// Create the HTTP client
	httpClient := &http.Client{
		Timeout: webhookTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Disable automatic redirect following to prevent SSRF via redirects to internal IPs
			return http.ErrUseLastResponse
		},
		Transport: otelhttp.NewTransport(newTransport()),
	}

	// Create the webhook client object
	w := &webhookClient{
		format:            opts.Format,
		webhookURL:        opts.URL,
		webhookKey:        opts.Key,
		webhookAuthHeader: opts.AuthorizationHeader,

		log:        opts.Logger,
		httpClient: httpClient,
		clock:      opts.clock,
	}

	return w, nil
}

// validateWebhookScheme checks that the webhook URL uses an allowed scheme (http/https)
func validateWebhookScheme(webhookUrl string) error {
	parsed, err := url.Parse(webhookUrl)
	if err != nil {
		return fmt.Errorf("invalid webhook URL: %w", err)
	}

	// Only allow http and https schemes
	switch parsed.Scheme {
	case "http", "https":
		// OK
		return nil
	default:
		return fmt.Errorf("webhook URL has disallowed scheme %q: only http and https are permitted", parsed.Scheme)
	}
}

// SendWebhook sends the notification
func (w *webhookClient) SendWebhook(ctx context.Context, data MessageProvider) (err error) {
	// Retry up to 3 times
	const attempts = 3
	var i int
retryLoop:
	for i = range attempts {
		var req *http.Request
		reqCtx, reqCancel := context.WithTimeout(ctx, webhookTimeout)
		switch w.format {
		case "slack":
			req, err = w.prepareSlackRequest(reqCtx, w.webhookURL, data)
		case "discord":
			req, err = w.prepareSlackRequest(reqCtx, w.webhookURL, data)
		// case "plain":
		default:
			req, err = w.preparePlainRequest(reqCtx, w.webhookURL, data)
		}
		if err != nil {
			reqCancel()
			// This is a permanent error
			return fmt.Errorf("failed to create request: %w", err)
		}

		var res *http.Response
		res, err = w.httpClient.Do(req)
		reqCancel()
		if err != nil {
			// Retry after 15 seconds on network failures, if we have more attempts
			if i < (attempts - 1) {
				w.log.WarnContext(ctx,
					"Network error sending webhook; will retry after 15 seconds",
					slog.Any("error", err),
				)
				select {
				case <-w.clock.After(15 * time.Second):
					// Nop
				case <-ctx.Done():
					err = ctx.Err()
					break retryLoop
				}
				continue
			}

			// If we've exhausted the available attempts, break out of the loop right away
			break
		}

		// Drain body before closing
		_, _ = io.Copy(io.Discard, res.Body)
		res.Body.Close()

		// Handle retries if we have more attempts
		if i < (attempts - 1) {
			// Handle throttling on 429 responses and on 5xx errors
			if res.StatusCode == http.StatusTooManyRequests {
				retryAfter, _ := strconv.Atoi(res.Header.Get("Retry-After"))
				if retryAfter < 1 || retryAfter > retryIntervalSeconds {
					retryAfter = retryIntervalSeconds
				}
				w.log.WarnContext(ctx,
					"Webhook throttled; will retry after delay",
					slog.Int("delaySeconds", retryAfter),
				)
				select {
				case <-w.clock.After(time.Duration(retryAfter) * time.Second):
					// Nop
				case <-ctx.Done():
					err = ctx.Err()
					break retryLoop
				}
				continue
			}

			// Retry after a delay on 408 (Request Timeout) and 5xx errors, which indicate a problem with the server
			if res.StatusCode == http.StatusRequestTimeout || (res.StatusCode >= 500 && res.StatusCode < 600) {
				w.log.WarnContext(ctx,
					"Webhook returned an error response; will retry after delay",
					slog.Int("code", res.StatusCode),
					slog.Int("delaySeconds", retryIntervalSeconds),
				)
				select {
				case <-w.clock.After(retryIntervalSeconds * time.Second):
					// Nop
				case <-ctx.Done():
					err = ctx.Err()
					break retryLoop
				}
				continue
			}
		}

		// Any other error is permanent
		if res.StatusCode < 200 || res.StatusCode > 299 {
			err = fmt.Errorf("invalid response status code: %d", res.StatusCode)
			break
		}

		// If we're here, everything is good
		break
	}

	if err != nil {
		return fmt.Errorf("failed to send webhook after %d attempts; last error: %w", i+1, err)
	}

	return nil
}

func (w *webhookClient) preparePlainRequest(ctx context.Context, webhookUrl string, data MessageProvider) (req *http.Request, err error) {
	// Format the message
	message, err := data.GetPlainMessage()
	if err != nil {
		return nil, fmt.Errorf("error getting plain-text message: %w", err)
	}

	// Create the request
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, webhookUrl, strings.NewReader(message))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/plain")

	if w.webhookKey != "" {
		// Get header name, defaults to "Authorization"
		headerName := w.webhookAuthHeader
		if headerName == "" {
			headerName = "Authorization"
		}

		req.Header.Set(headerName, w.webhookKey)
	}

	return req, nil
}

func (w *webhookClient) prepareSlackRequest(ctx context.Context, webhookUrl string, data MessageProvider) (req *http.Request, err error) {
	// Format the message
	message, err := data.GetSlackMessage()
	if err != nil {
		return nil, fmt.Errorf("error getting Slack-compatible message: %w", err)
	}

	// Build the body
	pr, pw := io.Pipe()
	go func() {
		enc := json.NewEncoder(pw)
		enc.SetEscapeHTML(false)
		pErr := enc.Encode(message)
		if pErr != nil {
			_ = pw.CloseWithError(pErr)
			return
		}
		_ = pw.Close()
	}()

	// Create the request
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, webhookUrl, pr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	if w.webhookKey != "" {
		req.Header.Set("Authorization", w.webhookKey)
	}

	return req, nil
}
