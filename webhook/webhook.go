package webhook

import (
	"bytes"
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

	"github.com/italypaleale/go-kit/internal/webhooktransport"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	kclock "k8s.io/utils/clock"
)

// WebhookFormat is the type for the webhook format
type WebhookFormat string

const (
	FormatSlack      WebhookFormat = "slack"
	FormatDiscord    WebhookFormat = "discord"
	FormatPlain      WebhookFormat = "plain"
	FormatNtfy       WebhookFormat = "ntfy"
	FormatTelegram   WebhookFormat = "telegram"
	FormatGoogleChat WebhookFormat = "googlechat"
	FormatTeams      WebhookFormat = "teams"
)

const (
	defaultTimeout       = 20 * time.Second
	defaultMaxAttempts   = 3
	defaultRetryInterval = 20 * time.Second
	maxRetryAfterSeconds = 60
)

// Webhook client interface
type Webhook interface {
	// SendWebhook sends the notification
	SendWebhook(ctx context.Context, data MessageProvider) error
}

// Webhook client
type webhookClient struct {
	format            WebhookFormat
	webhookURL        string
	webhookKey        string
	webhookAuthHeader string
	timeout           time.Duration
	maxAttempts       int
	retryInterval     time.Duration
	telegramChat      telegramChat

	log        *slog.Logger
	httpClient *http.Client
	clock      kclock.Clock
}

type NewWebhookOpts struct {
	// URL is the webhook endpoint (required)
	URL string
	// Key is the optional key for the webhook
	// It is sent as-is in the authorization header, so include any prefix it needs (like "Bearer" or "APIKey")
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

	// Timeout is the timeout for each request attempt, including connecting, sending, and reading the response
	// If zero, defaults to 20 seconds
	// Must not be negative
	Timeout time.Duration
	// MaxAttempts is the maximum number of times a request is sent, including the first attempt
	// Network errors, 408, 429 and 5xx responses are retried, while other errors are permanent
	// If zero, defaults to 3
	// Set it to 1 to disable retries
	// Must not be negative
	MaxAttempts int
	// RetryInterval is the delay between attempts
	// It applies to every retried failure: network errors, and 408, 429 and 5xx responses
	// For 429 responses, a valid "Retry-After" header (up to 60 seconds) takes precedence
	// If zero, defaults to 20 seconds
	// Must not be negative
	RetryInterval time.Duration
	// AllowPrivateIPs allows sending the webhook to private and otherwise non-routable destinations, such as loopback, link-local, and RFC 1918 addresses
	// Enable it only when the endpoint is expected to be on an internal network
	// Note: When a proxy is configured in the environment, the block applies to the proxy's address rather than the destination
	AllowPrivateIPs bool

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
	var tgChat telegramChat
	switch opts.Format {
	case FormatSlack, FormatPlain, FormatNtfy, FormatGoogleChat, FormatTeams:
		// All good
	case FormatDiscord:
		// Shorthand for using Slack-compatible webhooks with Discord
		discordURL, err := discordSlackURL(opts.URL)
		if err != nil {
			return nil, fmt.Errorf("webhook URL validation failed: %w", err)
		}
		opts.URL = discordURL
	case FormatTelegram:
		// The chat is sent in the body of each request, so the URL keeps only the endpoint of the sendMessage method
		endpoint, chat, err := parseTelegramURL(opts.URL)
		if err != nil {
			return nil, fmt.Errorf("webhook URL validation failed: %w", err)
		}
		opts.URL = endpoint
		tgChat = chat
	case "":
		// Default to plain
		opts.Format = FormatPlain
	default:
		return nil, fmt.Errorf("invalid webhook format: %q", opts.Format)
	}

	// Validate the webhook URL (scheme, host, and the topic for ntfy)
	// The custom net.Dialer.Control on the HTTP transport enforces the private-IP block at connect time
	err := validateWebhookURL(opts.URL, opts.Format)
	if err != nil {
		return nil, fmt.Errorf("webhook URL validation failed: %w", err)
	}

	if opts.Timeout < 0 {
		return nil, errors.New("webhook timeout must not be negative")
	}
	if opts.MaxAttempts < 0 {
		return nil, errors.New("webhook max attempts must not be negative")
	}
	if opts.RetryInterval < 0 {
		return nil, errors.New("webhook retry interval must not be negative")
	}

	// Apply defaults
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	maxAttempts := opts.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = defaultMaxAttempts
	}
	retryInterval := opts.RetryInterval
	if retryInterval == 0 {
		retryInterval = defaultRetryInterval
	}

	// Create the HTTP client
	httpClient := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Do not follow redirects, to prevent SSRF via redirects to internal hosts
			return http.ErrUseLastResponse
		},
		Transport: otelhttp.NewTransport(webhooktransport.New(webhooktransport.Options{
			AllowPrivateIPs: opts.AllowPrivateIPs,
			Logger:          opts.Logger,
		})),
	}

	// Create the webhook client object
	w := &webhookClient{
		format:            opts.Format,
		webhookURL:        opts.URL,
		webhookKey:        opts.Key,
		webhookAuthHeader: opts.AuthorizationHeader,
		timeout:           timeout,
		maxAttempts:       maxAttempts,
		retryInterval:     retryInterval,
		telegramChat:      tgChat,

		log:        opts.Logger,
		httpClient: httpClient,
		clock:      opts.clock,
	}

	return w, nil
}

// discordSlackURL returns the Slack-compatible endpoint of a Discord webhook, which adds "/slack" to the path
func discordSlackURL(webhookURL string) (string, error) {
	parsed, err := url.Parse(webhookURL)
	if err != nil {
		return "", fmt.Errorf("invalid webhook URL: %w", err)
	}

	if strings.HasSuffix(strings.TrimSuffix(parsed.Path, "/"), "/slack") {
		return webhookURL, nil
	}

	return parsed.JoinPath("slack").String(), nil
}

// validateWebhookURL checks that the webhook URL uses an allowed scheme (http/https) and has a host
// Error messages never include the URL, as it commonly contains secrets
func validateWebhookURL(webhookUrl string, format WebhookFormat) error {
	parsed, err := url.Parse(webhookUrl)
	if err != nil {
		return fmt.Errorf("invalid webhook URL: %w", err)
	}

	// Only allow http and https schemes
	switch parsed.Scheme {
	case "http", "https":
		// OK
	default:
		return fmt.Errorf("webhook URL has disallowed scheme %q: only http and https are permitted", parsed.Scheme)
	}

	// URLs like "https:///path" and "https:path" parse without errors but have no host
	if parsed.Hostname() == "" {
		return errors.New("webhook URL must include a host")
	}

	// ntfy publishes to the topic named in the path, such as "https://ntfy.sh/mytopic"
	if format == FormatNtfy && strings.Trim(parsed.Path, "/") == "" {
		return errors.New("ntfy webhook URL must include the topic in the path")
	}

	// For Google Chat, check that the URL includes the space, key, and token
	if format == FormatGoogleChat {
		err = validateGoogleChatURL(parsed)
		if err != nil {
			return err
		}
	}

	return nil
}

// SendWebhook sends the notification
func (w *webhookClient) SendWebhook(ctx context.Context, data MessageProvider) (err error) {
	var i int
retryLoop:
	for i = range w.maxAttempts {
		var req *http.Request
		reqCtx, reqCancel := context.WithTimeout(ctx, w.timeout)
		switch w.format {
		case "slack":
			req, err = w.prepareSlackRequest(reqCtx, w.webhookURL, data)
		case "discord":
			req, err = w.prepareSlackRequest(reqCtx, w.webhookURL, data)
		case FormatNtfy:
			req, err = w.prepareNtfyRequest(reqCtx, w.webhookURL, data)
		case FormatTelegram:
			req, err = w.prepareTelegramRequest(reqCtx, w.webhookURL, data)
		case FormatGoogleChat:
			req, err = w.prepareGoogleChatRequest(reqCtx, w.webhookURL, data)
		case FormatTeams:
			req, err = w.prepareTeamsRequest(reqCtx, w.webhookURL, data)
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
		if err != nil {
			reqCancel()
			// Retry after a delay on network failures, if we have more attempts
			if i < (w.maxAttempts - 1) {
				w.log.WarnContext(ctx,
					"Network error sending webhook; will retry after delay",
					slog.Any("error", err),
					slog.Duration("delay", w.retryInterval),
				)
				select {
				case <-w.clock.After(w.retryInterval):
					// Nop
				case <-ctx.Done():
					err = ctx.Err()
					break retryLoop
				}
				continue
			}

			// No attempts left, so return the network error
			break
		}

		// Drain and close the body before cancelling the context so the connection can be reused
		// Limit reading to 1KB
		body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<10))
		_ = res.Body.Close()
		reqCancel()

		// Handle retries if we have more attempts
		if i < (w.maxAttempts - 1) {
			// Handle throttling on 429 responses, honoring Retry-After up to a maximum
			if res.StatusCode == http.StatusTooManyRequests {
				retryAfter := w.retryInterval
				seconds, _ := strconv.Atoi(res.Header.Get("Retry-After"))
				if seconds > maxRetryAfterSeconds {
					retryAfter = maxRetryAfterSeconds * time.Second
				} else if seconds >= 1 {
					retryAfter = time.Duration(seconds) * time.Second
				}
				w.log.WarnContext(ctx,
					"Webhook throttled; will retry after delay",
					slog.Duration("delay", retryAfter),
				)
				select {
				case <-w.clock.After(retryAfter):
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
					slog.Duration("delay", w.retryInterval),
				)
				select {
				case <-w.clock.After(w.retryInterval):
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
			err = w.responseError(res.StatusCode, body)
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

// responseError returns the error for a response whose status code is not successful
func (w *webhookClient) responseError(statusCode int, body []byte) error {
	var description string
	switch w.format {
	case FormatTelegram:
		description = telegramErrorDescription(body)
	case FormatGoogleChat, FormatTeams:
		description = jsonErrorMessage(body)
	}

	if description != "" {
		return fmt.Errorf("invalid response status code: %d (%s)", statusCode, description)
	}

	return fmt.Errorf("invalid response status code: %d", statusCode)
}

// jsonErrorMessage returns the message of the error in a body like {"error": {"message": "..."}}, which is used by Google APIs and Power Automate
// It returns an empty string if the body does not have that shape, for example because it comes from a proxy or it was truncated
func jsonErrorMessage(body []byte) string {
	var res struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	err := json.Unmarshal(body, &res)
	if err != nil {
		return ""
	}

	return res.Error.Message
}

// encodeJSONBody encodes the body of a request as JSON
func encodeJSONBody(v any) (*bytes.Buffer, error) {
	buf := &bytes.Buffer{}
	enc := json.NewEncoder(buf)
	// HTML escaping is disabled so markup in the text, such as Telegram's HTML or Google Chat's <url|text> links, is readable in the request
	enc.SetEscapeHTML(false)
	err := enc.Encode(v)
	if err != nil {
		return nil, err
	}

	return buf, nil
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
	w.setAuthHeader(req)

	return req, nil
}

// setAuthHeader adds the key to the request, if there's one
func (w *webhookClient) setAuthHeader(req *http.Request) {
	if w.webhookKey == "" {
		return
	}

	// Get header name, defaults to "Authorization"
	headerName := w.webhookAuthHeader
	if headerName == "" {
		headerName = "Authorization"
	}

	req.Header.Set(headerName, w.webhookKey)
}

func (w *webhookClient) prepareSlackRequest(ctx context.Context, webhookUrl string, data MessageProvider) (req *http.Request, err error) {
	// Format the message
	message, err := getSlackMessage(data)
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

// prepareNtfyRequest prepares a request to publish to an ntfy topic (https://docs.ntfy.sh/publish/)
// The URL must include the topic, such as "https://ntfy.sh/mytopic"
// To publish to a protected topic, set Key to the full value of the authorization header, such as "Bearer tk_..." for an access token
func (w *webhookClient) prepareNtfyRequest(ctx context.Context, webhookUrl string, data MessageProvider) (req *http.Request, err error) {
	// Format the message
	message, err := getNtfyMessage(data)
	if err != nil {
		return nil, fmt.Errorf("error getting ntfy message: %w", err)
	}

	// Build the headers first, so an invalid message does not create a request
	headers, err := message.headers()
	if err != nil {
		return nil, fmt.Errorf("invalid ntfy message: %w", err)
	}

	// Create the request
	// The message is the body, and the other fields are sent as headers
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, webhookUrl, strings.NewReader(message.Message))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	w.setAuthHeader(req)

	return req, nil
}

// prepareTelegramRequest prepares a request to the sendMessage method of the Telegram Bot API (https://core.telegram.org/bots/api#sendmessage)
// The URL is the endpoint of the method, while the chat from the query string of the configured URL is sent in the body
func (w *webhookClient) prepareTelegramRequest(ctx context.Context, webhookUrl string, data MessageProvider) (req *http.Request, err error) {
	// Format the message
	message, err := getTelegramMessage(data)
	if err != nil {
		return nil, fmt.Errorf("error getting Telegram message: %w", err)
	}

	// Build the body first, so an invalid message does not create a request
	body, err := message.sendMessageBody(w.telegramChat)
	if err != nil {
		return nil, fmt.Errorf("invalid Telegram message: %w", err)
	}

	buf, err := encodeJSONBody(body)
	if err != nil {
		return nil, fmt.Errorf("failed to encode Telegram message: %w", err)
	}

	// Create the request
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, webhookUrl, buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	return req, nil
}

// prepareGoogleChatRequest prepares a request to a Google Chat webhook (https://developers.google.com/workspace/chat/quickstart/webhooks)
func (w *webhookClient) prepareGoogleChatRequest(ctx context.Context, webhookUrl string, data MessageProvider) (req *http.Request, err error) {
	// Format the message
	message, err := getGoogleChatMessage(data)
	if err != nil {
		return nil, fmt.Errorf("error getting Google Chat message: %w", err)
	}

	// Build the body first, so an invalid message does not create a request
	body, err := message.requestBody()
	if err != nil {
		return nil, fmt.Errorf("invalid Google Chat message: %w", err)
	}

	buf, err := encodeJSONBody(body)
	if err != nil {
		return nil, fmt.Errorf("failed to encode Google Chat message: %w", err)
	}

	if message.ThreadKey != "" {
		webhookUrl, err = googleChatThreadURL(webhookUrl)
		if err != nil {
			return nil, err
		}
	}

	// Create the request
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, webhookUrl, buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")

	w.setAuthHeader(req)

	return req, nil
}

// prepareTeamsRequest prepares a request to a Microsoft Teams webhook created with the Workflows app, which posts the message as an Adaptive Card
func (w *webhookClient) prepareTeamsRequest(ctx context.Context, webhookUrl string, data MessageProvider) (req *http.Request, err error) {
	// Format the message
	message, err := getTeamsMessage(data)
	if err != nil {
		return nil, fmt.Errorf("error getting Teams message: %w", err)
	}

	// Build the body first, so an invalid message does not create a request
	body, err := message.requestBody()
	if err != nil {
		return nil, fmt.Errorf("invalid Teams message: %w", err)
	}

	buf, err := encodeJSONBody(body)
	if err != nil {
		return nil, fmt.Errorf("failed to encode Teams message: %w", err)
	}

	// Create the request
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, webhookUrl, buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	w.setAuthHeader(req)

	return req, nil
}
