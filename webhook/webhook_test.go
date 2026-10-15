package webhook

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	clocktesting "k8s.io/utils/clock/testing"

	"github.com/italypaleale/go-kit/testutils"
)

type testMessageProvider struct {
	message      string
	slackMessage SlackMessage
	ntfyMessage  NtfyMessage
	plainErr     error
	slackErr     error
	ntfyErr      error
}

func (tmp testMessageProvider) GetPlainMessage() (string, error) {
	if tmp.plainErr != nil {
		return "", tmp.plainErr
	}
	return tmp.message, nil
}

func (tmp testMessageProvider) GetSlackMessage() (SlackMessage, error) {
	if tmp.slackErr != nil {
		return SlackMessage{}, tmp.slackErr
	}
	if tmp.slackMessage != (SlackMessage{}) {
		return tmp.slackMessage, nil
	}
	return SlackMessage{
		Text: tmp.message,
	}, nil
}

func (tmp testMessageProvider) GetNtfyMessage() (NtfyMessage, error) {
	if tmp.ntfyErr != nil {
		return NtfyMessage{}, tmp.ntfyErr
	}
	if tmp.ntfyMessage.Message != "" || tmp.ntfyMessage.Title != "" {
		return tmp.ntfyMessage, nil
	}
	return NtfyMessage{
		Message: tmp.message,
	}, nil
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return fn(r)
}

func newTestWebhookClient(t *testing.T, opts NewWebhookOpts) (*webhookClient, *clocktesting.FakeClock) {
	t.Helper()

	clock := clocktesting.NewFakeClock(time.Now())
	if opts.URL == "" {
		opts.URL = "http://198.51.100.10/endpoint"
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	opts.clock = clock

	whAny, err := newWebhookInternal(opts)
	require.NoError(t, err, "Failed to create webhook client")

	wh := whAny.(*webhookClient) //nolint:forcetypeassert
	return wh, clock
}

func httpResponse(t *testing.T, statusCode int) *http.Response {
	r := &http.Response{
		StatusCode: statusCode,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     make(http.Header),
	}
	t.Cleanup(func() {
		_ = r.Body.Close()
	})
	return r
}

func TestNewWebhook(t *testing.T) {
	t.Run("validates required URL", func(t *testing.T) {
		wh, err := NewWebhook(NewWebhookOpts{})
		require.Nil(t, wh)
		require.Error(t, err)
		require.ErrorContains(t, err, "webhook URL is required")
	})

	t.Run("rejects malformed URLs", func(t *testing.T) {
		wh, err := NewWebhook(NewWebhookOpts{URL: "://bad url"})
		require.Nil(t, wh)
		require.Error(t, err)
		require.ErrorContains(t, err, "webhook URL validation failed")
	})

	t.Run("rejects disallowed schemes", func(t *testing.T) {
		wh, err := NewWebhook(NewWebhookOpts{URL: "file:///tmp/webhook"})
		require.Nil(t, wh)
		require.Error(t, err)
		require.ErrorContains(t, err, "only http and https are permitted")
	})

	t.Run("rejects invalid formats", func(t *testing.T) {
		wh, err := NewWebhook(NewWebhookOpts{URL: "https://hooks.example.com/endpoint", Format: WebhookFormat("teams")})
		require.Nil(t, wh)
		require.Error(t, err)
		require.ErrorContains(t, err, "invalid webhook format")
	})

	t.Run("defaults to plain format and configures the client", func(t *testing.T) {
		whAny, err := NewWebhook(NewWebhookOpts{URL: "https://hooks.example.com/endpoint"})
		require.NoError(t, err)

		wh := whAny.(*webhookClient) //nolint:forcetypeassert
		require.Equal(t, FormatPlain, wh.format)
		require.Equal(t, "https://hooks.example.com/endpoint", wh.webhookURL)
		require.NotNil(t, wh.log)
		require.NotNil(t, wh.httpClient)
		require.Equal(t, defaultTimeout, wh.httpClient.Timeout)
		require.NotNil(t, wh.httpClient.Transport)

		req, err := http.NewRequest(http.MethodGet, "https://example.com/next", nil)
		require.NoError(t, err)
		require.ErrorIs(t, wh.httpClient.CheckRedirect(req, nil), http.ErrUseLastResponse)
	})

	t.Run("appends the slack suffix for Discord webhooks", func(t *testing.T) {
		whAny, err := NewWebhook(NewWebhookOpts{URL: "https://discord.example.com/api/hooks/123", Format: FormatDiscord})
		require.NoError(t, err)

		wh := whAny.(*webhookClient) //nolint:forcetypeassert
		require.Equal(t, FormatDiscord, wh.format)
		require.Equal(t, "https://discord.example.com/api/hooks/123/slack", wh.webhookURL)
	})

	t.Run("does not duplicate the slack suffix for Discord webhooks", func(t *testing.T) {
		whAny, err := NewWebhook(NewWebhookOpts{URL: "https://discord.example.com/api/hooks/123/slack", Format: FormatDiscord})
		require.NoError(t, err)

		wh := whAny.(*webhookClient) //nolint:forcetypeassert
		require.Equal(t, "https://discord.example.com/api/hooks/123/slack", wh.webhookURL)
	})

	t.Run("adds the slack suffix to the path of Discord webhooks with a query", func(t *testing.T) {
		tests := []struct {
			name         string
			url          string
			expectedPath string
		}{
			{name: "without suffix", url: "https://discord.example.com/api/hooks/123/token?thread_id=456", expectedPath: "/api/hooks/123/token/slack"},
			{name: "with suffix", url: "https://discord.example.com/api/hooks/123/token/slack?thread_id=456", expectedPath: "/api/hooks/123/token/slack"},
			{name: "with trailing slash", url: "https://discord.example.com/api/hooks/123/token/?thread_id=456", expectedPath: "/api/hooks/123/token/slack"},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				whAny, err := NewWebhook(NewWebhookOpts{URL: tc.url, Format: FormatDiscord})
				require.NoError(t, err)

				wh := whAny.(*webhookClient) //nolint:forcetypeassert
				parsed, err := url.Parse(wh.webhookURL)
				require.NoError(t, err)
				require.Equal(t, tc.expectedPath, parsed.Path)
				require.Equal(t, url.Values{"thread_id": []string{"456"}}, parsed.Query())
			})
		}
	})

	t.Run("rejects malformed Discord webhook URLs", func(t *testing.T) {
		wh, err := NewWebhook(NewWebhookOpts{URL: "://bad url", Format: FormatDiscord})
		require.Nil(t, wh)
		require.Error(t, err)
		require.ErrorContains(t, err, "webhook URL validation failed")
	})
}

func TestWebhookRequestFormatting(t *testing.T) {
	t.Run("plain webhook uses a plain-text body and authorization header", func(t *testing.T) {
		wh, _ := newTestWebhookClient(t, NewWebhookOpts{
			Format: FormatPlain,
			Key:    "Bearer secret-token",
		})

		rtt := &testutils.RoundTripperTest{}
		reqCh := make(chan *http.Request, 1)
		resCh := make(chan *http.Response, 1)
		rtt.SetReqCh(reqCh)
		rtt.SetResponsesCh(resCh)
		resCh <- httpResponse(t, http.StatusNoContent) //nolint:bodyclose
		wh.httpClient.Transport = rtt

		err := wh.SendWebhook(t.Context(), testMessageProvider{message: "plain message body"})
		require.NoError(t, err)

		req := <-reqCh
		t.Cleanup(func() {
			_ = req.Body.Close()
		})
		require.Equal(t, http.MethodPost, req.Method)
		require.Equal(t, "http://198.51.100.10/endpoint", req.URL.String())
		require.Equal(t, "text/plain", req.Header.Get("Content-Type"))
		require.Equal(t, "Bearer secret-token", req.Header.Get("Authorization"))
		assertBodyEqual(t, req.Body, "plain message body")
	})

	t.Run("plain webhook uses the configured authorization header", func(t *testing.T) {
		wh, _ := newTestWebhookClient(t, NewWebhookOpts{
			Format:              FormatPlain,
			Key:                 "Bearer secret-token",
			AuthorizationHeader: "X-Webhook-Key",
		})

		rtt := &testutils.RoundTripperTest{}
		reqCh := make(chan *http.Request, 1)
		resCh := make(chan *http.Response, 1)
		rtt.SetReqCh(reqCh)
		rtt.SetResponsesCh(resCh)
		resCh <- httpResponse(t, http.StatusNoContent) //nolint:bodyclose
		wh.httpClient.Transport = rtt

		err := wh.SendWebhook(t.Context(), testMessageProvider{message: "plain message body"})
		require.NoError(t, err)

		req := <-reqCh
		t.Cleanup(func() {
			_ = req.Body.Close()
		})

		assert.Empty(t, req.Header.Get("Authorization"))
		assert.Equal(t, "Bearer secret-token", req.Header.Get("X-Webhook-Key"))
		assertBodyEqual(t, req.Body, "plain message body")
	})

	t.Run("slack-compatible webhooks send JSON without HTML escaping", func(t *testing.T) {
		wh, _ := newTestWebhookClient(t, NewWebhookOpts{
			Format: FormatSlack,
			Key:    "ApiKey test-key",
		})

		rtt := &testutils.RoundTripperTest{}
		reqCh := make(chan *http.Request, 1)
		resCh := make(chan *http.Response, 1)
		rtt.SetReqCh(reqCh)
		rtt.SetResponsesCh(resCh)
		resCh <- httpResponse(t, http.StatusOK) //nolint:bodyclose
		wh.httpClient.Transport = rtt

		err := wh.SendWebhook(t.Context(), testMessageProvider{message: "hello <team> & friends"})
		require.NoError(t, err)

		req := <-reqCh
		t.Cleanup(func() {
			_ = req.Body.Close()
		})
		require.Equal(t, http.MethodPost, req.Method)
		require.Equal(t, "application/json", req.Header.Get("Content-Type"))
		require.Equal(t, "ApiKey test-key", req.Header.Get("Authorization"))
		assertBodyEqual(t, req.Body, `{"text":"hello <team> & friends"}`+"\n")
	})

	t.Run("slack-compatible webhooks ignore the configured authorization header", func(t *testing.T) {
		wh, _ := newTestWebhookClient(t, NewWebhookOpts{
			Format:              FormatSlack,
			Key:                 "ApiKey test-key",
			AuthorizationHeader: "X-Webhook-Key",
		})

		rtt := &testutils.RoundTripperTest{}
		reqCh := make(chan *http.Request, 1)
		resCh := make(chan *http.Response, 1)
		rtt.SetReqCh(reqCh)
		rtt.SetResponsesCh(resCh)
		resCh <- httpResponse(t, http.StatusOK) //nolint:bodyclose
		wh.httpClient.Transport = rtt

		err := wh.SendWebhook(t.Context(), testMessageProvider{message: "hello slack"})
		require.NoError(t, err)

		req := <-reqCh
		t.Cleanup(func() {
			_ = req.Body.Close()
		})
		assert.Equal(t, "ApiKey test-key", req.Header.Get("Authorization"))
		assert.Empty(t, req.Header.Get("X-Webhook-Key"))
		assertBodyEqual(t, req.Body, `{"text":"hello slack"}`+"\n")
	})

	t.Run("Discord webhooks reuse the slack-compatible formatter", func(t *testing.T) {
		wh, _ := newTestWebhookClient(t, NewWebhookOpts{Format: FormatDiscord})

		req, err := wh.prepareSlackRequest(t.Context(), wh.webhookURL, testMessageProvider{message: "discord payload"})
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = req.Body.Close()
		})
		require.Equal(t, "application/json", req.Header.Get("Content-Type"))
		assertBodyEqual(t, req.Body, `{"text":"discord payload"}`+"\n")
	})

	t.Run("Discord webhooks ignore the configured authorization header", func(t *testing.T) {
		wh, _ := newTestWebhookClient(t, NewWebhookOpts{
			Format:              FormatDiscord,
			Key:                 "ApiKey test-key",
			AuthorizationHeader: "X-Webhook-Key",
		})

		rtt := &testutils.RoundTripperTest{}
		reqCh := make(chan *http.Request, 1)
		resCh := make(chan *http.Response, 1)
		rtt.SetReqCh(reqCh)
		rtt.SetResponsesCh(resCh)
		resCh <- httpResponse(t, http.StatusOK) //nolint:bodyclose
		wh.httpClient.Transport = rtt

		err := wh.SendWebhook(t.Context(), testMessageProvider{message: "hello discord"})
		require.NoError(t, err)

		req := <-reqCh
		t.Cleanup(func() {
			_ = req.Body.Close()
		})
		assert.Equal(t, "ApiKey test-key", req.Header.Get("Authorization"))
		assert.Empty(t, req.Header.Get("X-Webhook-Key"))
		assertBodyEqual(t, req.Body, `{"text":"hello discord"}`+"\n")
	})
}

func TestWebhookRequestErrors(t *testing.T) {
	t.Run("plain webhook returns a permanent error when the provider fails", func(t *testing.T) {
		wh, _ := newTestWebhookClient(t, NewWebhookOpts{Format: FormatPlain})

		err := wh.SendWebhook(t.Context(), testMessageProvider{plainErr: errors.New("plain provider failed")})
		require.Error(t, err)
		require.ErrorContains(t, err, "failed to create request")
		require.ErrorContains(t, err, "plain provider failed")
	})

	t.Run("slack webhook returns a permanent error when the provider fails", func(t *testing.T) {
		wh, _ := newTestWebhookClient(t, NewWebhookOpts{Format: FormatSlack})

		err := wh.SendWebhook(t.Context(), testMessageProvider{slackErr: errors.New("slack provider failed")})
		require.Error(t, err)
		require.ErrorContains(t, err, "failed to create request")
		require.ErrorContains(t, err, "slack provider failed")
	})

	t.Run("non-retryable status codes fail immediately", func(t *testing.T) {
		wh, _ := newTestWebhookClient(t, NewWebhookOpts{Format: FormatPlain})

		rtt := &testutils.RoundTripperTest{}
		reqCh := make(chan *http.Request, 1)
		resCh := make(chan *http.Response, 1)
		rtt.SetReqCh(reqCh)
		rtt.SetResponsesCh(resCh)
		resCh <- httpResponse(t, http.StatusBadRequest) //nolint:bodyclose
		wh.httpClient.Transport = rtt

		err := wh.SendWebhook(t.Context(), testMessageProvider{message: "no retries"})
		require.Error(t, err)
		require.ErrorContains(t, err, "failed to send webhook after 1 attempts")
		require.ErrorContains(t, err, "invalid response status code: 400")

		req := <-reqCh
		err = req.Body.Close()
		require.NoError(t, err)
	})
}

func TestWebhook(t *testing.T) {
	wh, clock := newTestWebhookClient(t, NewWebhookOpts{})

	// Create a roundtripper that captures the requests
	rtt := &testutils.RoundTripperTest{}
	wh.httpClient.Transport = rtt

	getWebhookData := func() *testMessageProvider {
		return &testMessageProvider{
			message: "hello world",
		}
	}

	t.Run("fail on 4xx status codes", func(t *testing.T) {
		reqCh := make(chan *http.Request, 1)
		rtt.SetReqCh(reqCh)
		resCh := make(chan *http.Response, 1)
		rtt.SetResponsesCh(resCh)
		resCh <- &http.Response{
			StatusCode: http.StatusForbidden,
			Body:       io.NopCloser(strings.NewReader("")),
		}
		defer func() {
			resCh = nil
		}()

		err := wh.SendWebhook(t.Context(), getWebhookData())
		require.Error(t, err)
		require.ErrorContains(t, err, "invalid response status code: 403")

		r := <-reqCh
		err = r.Body.Close()
		require.NoError(t, err)
	})

	t.Run("retry on 429 status codes without Retry-After header", func(t *testing.T) {
		reqCh := make(chan *http.Request)
		rtt.SetReqCh(reqCh)
		resCh := make(chan *http.Response, 2)
		rtt.SetResponsesCh(resCh)
		// Send a 429 status code twice
		resCh <- httpResponse(t, http.StatusTooManyRequests) //nolint:bodyclose
		resCh <- httpResponse(t, http.StatusTooManyRequests) //nolint:bodyclose
		defer func() {
			resCh = nil
		}()

		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		doneCh := assertRetries(ctx, clock, reqCh, 3, defaultRetryInterval)

		err := wh.SendWebhook(ctx, getWebhookData())
		require.NoError(t, err)

		// This will receive an error after 3 requests have come in, or the context timed out
		require.NoError(t, <-doneCh)
	})

	t.Run("retry on 429 status codes respects Retry-After header", func(t *testing.T) {
		reqCh := make(chan *http.Request)
		rtt.SetReqCh(reqCh)
		resCh := make(chan *http.Response, 2)
		rtt.SetResponsesCh(resCh)
		makeRes := func() *http.Response {
			res := httpResponse(t, http.StatusTooManyRequests)
			res.Header.Set("Retry-After", "5")
			return res
		}
		// Send a 429 status code twice but with a Retry-After header
		resCh <- makeRes() //nolint:bodyclose
		resCh <- makeRes() //nolint:bodyclose
		defer func() {
			resCh = nil
		}()

		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		doneCh := assertRetries(ctx, clock, reqCh, 3, 5*time.Second)

		err := wh.SendWebhook(ctx, getWebhookData())
		require.NoError(t, err)

		// This will receive an error after 3 requests have come in, or the context timed out
		require.NoError(t, <-doneCh)
	})

	t.Run("retry on 5xx status codes", func(t *testing.T) {
		reqCh := make(chan *http.Request)
		rtt.SetReqCh(reqCh)
		resCh := make(chan *http.Response, 1)
		rtt.SetResponsesCh(resCh)
		// Send a 500 status code once
		resCh <- httpResponse(t, http.StatusInternalServerError) //nolint:bodyclose
		defer func() {
			resCh = nil
		}()

		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		doneCh := assertRetries(ctx, clock, reqCh, 2, defaultRetryInterval)

		err := wh.SendWebhook(ctx, getWebhookData())
		require.NoError(t, err)

		// This will receive an error after 3 requests have come in, or the context timed out
		require.NoError(t, <-doneCh)
	})

	t.Run("too many failed attempts with 429 status codes", func(t *testing.T) {
		reqCh := make(chan *http.Request)
		rtt.SetReqCh(reqCh)
		resCh := make(chan *http.Response, 3)
		rtt.SetResponsesCh(resCh)
		// Send a 429 status code 3 times
		resCh <- httpResponse(t, http.StatusTooManyRequests) //nolint:bodyclose
		resCh <- httpResponse(t, http.StatusTooManyRequests) //nolint:bodyclose
		resCh <- httpResponse(t, http.StatusTooManyRequests) //nolint:bodyclose
		defer func() {
			resCh = nil
		}()

		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		doneCh := assertRetries(ctx, clock, reqCh, 3, defaultRetryInterval)

		err := wh.SendWebhook(ctx, getWebhookData())
		require.Error(t, err)
		require.ErrorContains(t, err, "invalid response status code: 429")

		// This will receive an error after 3 requests have come in, or the context timed out
		require.NoError(t, <-doneCh)
	})

	t.Run("too many failed attempts with 5xx status codes", func(t *testing.T) {
		reqCh := make(chan *http.Request)
		rtt.SetReqCh(reqCh)
		resCh := make(chan *http.Response, 3)
		rtt.SetResponsesCh(resCh)
		// Send a 429 status code 3 times
		resCh <- httpResponse(t, http.StatusInternalServerError) //nolint:bodyclose
		resCh <- httpResponse(t, http.StatusBadGateway)          //nolint:bodyclose
		resCh <- httpResponse(t, http.StatusBadGateway)          //nolint:bodyclose
		defer func() {
			resCh = nil
		}()

		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		doneCh := assertRetries(ctx, clock, reqCh, 3, defaultRetryInterval)

		err := wh.SendWebhook(ctx, getWebhookData())
		require.Error(t, err)
		require.ErrorContains(t, err, "invalid response status code: 502")

		// This will receive an error after 3 requests have come in, or the context timed out
		require.NoError(t, <-doneCh)
	})

	t.Run("retry on request timeout status codes", func(t *testing.T) {
		reqCh := make(chan *http.Request)
		rtt.SetReqCh(reqCh)
		resCh := make(chan *http.Response, 1)
		rtt.SetResponsesCh(resCh)
		resCh <- httpResponse(t, http.StatusRequestTimeout) //nolint:bodyclose
		defer func() {
			resCh = nil
		}()

		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		doneCh := assertRetries(ctx, clock, reqCh, 2, defaultRetryInterval)

		err := wh.SendWebhook(ctx, getWebhookData())
		require.NoError(t, err)
		require.NoError(t, <-doneCh)
	})

	t.Run("retry on network errors and fail after exhausting attempts", func(t *testing.T) {
		reqCh := make(chan *http.Request)
		wh.httpClient.Transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			reqCh <- r
			return nil, errors.New("dial tcp: i/o timeout")
		})

		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		doneCh := assertRetries(ctx, clock, reqCh, 3, defaultRetryInterval)

		err := wh.SendWebhook(ctx, getWebhookData())
		require.Error(t, err)
		require.ErrorContains(t, err, "failed to send webhook after 3 attempts")
		require.ErrorContains(t, err, "dial tcp: i/o timeout")
		require.NoError(t, <-doneCh)
	})

	t.Run("retry after values above the cap are clamped to the cap", func(t *testing.T) {
		reqCh := make(chan *http.Request)
		rtt.SetReqCh(reqCh)
		resCh := make(chan *http.Response, 1)
		rtt.SetResponsesCh(resCh)
		res := httpResponse(t, http.StatusTooManyRequests) //nolint:bodyclose
		res.Header.Set("Retry-After", "120")
		resCh <- res
		defer func() {
			resCh = nil
		}()

		wh.httpClient.Transport = rtt
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		doneCh := assertRetries(ctx, clock, reqCh, 2, maxRetryAfterSeconds*time.Second)

		err := wh.SendWebhook(ctx, getWebhookData())
		require.NoError(t, err)
		require.NoError(t, <-doneCh)
	})

	t.Run("redirects to private IPs are not followed", func(t *testing.T) {
		requestURLs := make([]string, 0, 2)
		wh.httpClient.Transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			requestURLs = append(requestURLs, r.URL.String())
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": []string{"http://192.168.1.1/internal"}},
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		})

		err := wh.SendWebhook(t.Context(), testMessageProvider{message: "test"})
		require.Error(t, err)
		require.ErrorContains(t, err, "invalid response status code: 302")
		assert.Equal(t, []string{"http://198.51.100.10/endpoint"}, requestURLs)
	})
}

func assertBodyEqual(t *testing.T, body io.ReadCloser, expect string) {
	t.Helper()

	read, err := io.ReadAll(body)
	require.NoError(t, err, "failed to read body")

	assert.Equal(t, expect, string(read))
}

// Asserts that the code retries the desired number of times
func assertRetries(
	ctx context.Context, clock *clocktesting.FakeClock, reqCh <-chan *http.Request,
	expectRequests int, retryDuration time.Duration,
) <-chan error {
	// We'll return this channel that resolves with nil when everything goes well
	doneCh := make(chan error)

	// Perform the waiting in background
	go func() {
		// Expect this to receive expectRequests requests
		for i := range expectRequests {
			select {
			case r := <-reqCh:
				_ = r.Body.Close()
			case <-ctx.Done():
				doneCh <- ctx.Err()
				return
			}

			if i < (expectRequests - 1) {
				// Sleep until we have a goroutine waiting or we wait too much (1s)
				// This is not ideal as we're depending on a wall clock but it's probably enough for now
				for range 20 {
					if !clock.HasWaiters() {
						time.Sleep(50 * time.Millisecond)
					}
				}

				// By now there should be waiters
				if !clock.HasWaiters() {
					doneCh <- errors.New("no waiters on clock")
					return
				}

				// Assert that the code sleeps for retryDuration
				start := clock.Now()
				err := stepUntilWaiters(clock, time.Second, retryDuration)
				if err != nil {
					doneCh <- err
					return
				}
				if clock.Now().Sub(start) < retryDuration {
					doneCh <- fmt.Errorf("waited less than %v", retryDuration)
					return
				}
			}
		}
		doneCh <- nil
	}()

	return doneCh
}

func stepUntilWaiters(clock *clocktesting.FakeClock, step time.Duration, max time.Duration) error {
	start := clock.Now()
	for clock.HasWaiters() {
		clock.Step(step)
		if clock.Now().Sub(start) > max {
			return fmt.Errorf("clock still has waiters after %d", clock.Now().Sub(start))
		}
	}
	return nil
}

func TestWebhookClientRefusesPrivateDestinations(t *testing.T) {
	// The webhook points at a third-party service, so a private destination is always a misconfiguration
	// This checks the wiring, not the guard itself, which internal/webhooktransport covers
	w, err := newWebhookInternal(NewWebhookOpts{
		URL:    "http://10.0.0.25/hook",
		Logger: slog.New(slog.DiscardHandler),
		clock:  clocktesting.NewFakeClock(time.Now()),
	})
	require.NoError(t, err)

	wh, ok := w.(*webhookClient)
	require.True(t, ok)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, wh.webhookURL, nil)
	require.NoError(t, err)

	res, err := wh.httpClient.Do(req)
	if res != nil {
		_ = res.Body.Close()
	}

	require.Error(t, err)
	require.ErrorContains(t, err, "refusing to dial private/internal IP")
}

func TestNewWebhookOptionsValidation(t *testing.T) {
	tests := []struct {
		name   string
		opts   NewWebhookOpts
		expect string
	}{
		{name: "negative timeout", opts: NewWebhookOpts{Timeout: -time.Second}, expect: "timeout must not be negative"},
		{name: "negative max attempts", opts: NewWebhookOpts{MaxAttempts: -1}, expect: "max attempts must not be negative"},
		{name: "negative retry interval", opts: NewWebhookOpts{RetryInterval: -time.Second}, expect: "retry interval must not be negative"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.opts.URL = "https://hooks.example.com/endpoint"
			wh, err := NewWebhook(tc.opts)
			require.Nil(t, wh)
			require.Error(t, err)
			require.ErrorContains(t, err, tc.expect)
		})
	}
}

func TestNewWebhookURLValidation(t *testing.T) {
	tests := []struct {
		name   string
		url    string
		format WebhookFormat
		// Empty when the URL must be accepted
		expectErr string
	}{
		// Host
		{name: "plain without host", url: "https:///x", format: FormatPlain, expectErr: "must include a host"},
		{name: "plain with opaque URL", url: "https:x", format: FormatPlain, expectErr: "must include a host"},
		{name: "default format without host", url: "https:///x", expectErr: "must include a host"},
		{name: "slack without host", url: "https:///x", format: FormatSlack, expectErr: "must include a host"},
		{name: "discord without host", url: "https:///x", format: FormatDiscord, expectErr: "must include a host"},
		{name: "ntfy without host", url: "https:///x", format: FormatNtfy, expectErr: "must include a host"},
		{name: "ntfy with opaque URL", url: "https:x", format: FormatNtfy, expectErr: "must include a host"},

		// ntfy topic
		{name: "ntfy without path", url: "https://ntfy.example.com", format: FormatNtfy, expectErr: "must include the topic"},
		{name: "ntfy with root path", url: "https://ntfy.example.com/", format: FormatNtfy, expectErr: "must include the topic"},
		{name: "ntfy with only slashes", url: "https://ntfy.example.com//", format: FormatNtfy, expectErr: "must include the topic"},
		{name: "ntfy with only a query", url: "https://ntfy.example.com/?auth=x", format: FormatNtfy, expectErr: "must include the topic"},
		{name: "ntfy with topic", url: "https://ntfy.example.com/ddup", format: FormatNtfy},
		{name: "ntfy with topic and trailing slash", url: "https://ntfy.example.com/ddup/", format: FormatNtfy},
		{name: "ntfy with path prefix and topic", url: "https://ntfy.example.com/prefix/topic", format: FormatNtfy},
		{name: "ntfy with port and topic", url: "http://ntfy.example.com:8080/ddup", format: FormatNtfy},

		// Other formats do not need a path
		{name: "plain without path", url: "https://hooks.example.com", format: FormatPlain},
		{name: "default format without path", url: "https://hooks.example.com"},
		{name: "slack without path", url: "https://hooks.example.com", format: FormatSlack},
		{name: "discord without path", url: "https://discord.example.com", format: FormatDiscord},
		{name: "plain with path", url: "https://hooks.example.com/endpoint", format: FormatPlain},
		{name: "slack with path", url: "https://hooks.example.com/services/a/b", format: FormatSlack},
		{name: "discord with path", url: "https://discord.example.com/api/hooks/123/token", format: FormatDiscord},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wh, err := NewWebhook(NewWebhookOpts{URL: tc.url, Format: tc.format})
			if tc.expectErr == "" {
				require.NoError(t, err)
				require.NotNil(t, wh)
				return
			}

			require.Nil(t, wh)
			require.ErrorContains(t, err, "webhook URL validation failed")
			require.ErrorContains(t, err, tc.expectErr)
			// URLs commonly contain secrets, so they must not appear in the error
			require.NotContains(t, err.Error(), tc.url)
		})
	}
}

func TestNewWebhookOptionsDefaults(t *testing.T) {
	// The zero value of every option must keep the behavior from before the options existed
	whAny, err := NewWebhook(NewWebhookOpts{URL: "https://hooks.example.com/endpoint"})
	require.NoError(t, err)

	wh := whAny.(*webhookClient) //nolint:forcetypeassert
	assert.Equal(t, 20*time.Second, wh.timeout)
	assert.Equal(t, 20*time.Second, wh.httpClient.Timeout)
	assert.Equal(t, 3, wh.maxAttempts)
	assert.Equal(t, 20*time.Second, wh.retryInterval)

	// Redirects are not followed
	req, err := http.NewRequest(http.MethodGet, "https://example.com/next", nil)
	require.NoError(t, err)
	err = wh.httpClient.CheckRedirect(req, nil)
	require.ErrorIs(t, err, http.ErrUseLastResponse)
}

func TestNewWebhookOptionsCustom(t *testing.T) {
	wh, _ := newTestWebhookClient(t, NewWebhookOpts{
		Timeout:       5 * time.Second,
		MaxAttempts:   7,
		RetryInterval: 2 * time.Second,
	})
	assert.Equal(t, 5*time.Second, wh.timeout)
	assert.Equal(t, 5*time.Second, wh.httpClient.Timeout)
	assert.Equal(t, 7, wh.maxAttempts)
	assert.Equal(t, 2*time.Second, wh.retryInterval)
}

func TestWebhookRetryOptions(t *testing.T) {
	// Replaces the transport with one that reports each request on a channel, then replies using the given function
	setup := func(t *testing.T, opts NewWebhookOpts, reply func(n int) (*http.Response, error)) (wh *webhookClient, clock *clocktesting.FakeClock, reqCh chan *http.Request, count func() int) {
		t.Helper()

		wh, clock = newTestWebhookClient(t, opts)
		reqCh = make(chan *http.Request)
		var counter atomic.Int32
		wh.httpClient.Transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			n := int(counter.Add(1))
			res, err := reply(n)
			reqCh <- r
			return res, err
		})
		return wh, clock, reqCh, func() int {
			return int(counter.Load())
		}
	}

	t.Run("max attempts of 1 disables retries", func(t *testing.T) {
		wh, _, reqCh, count := setup(t, NewWebhookOpts{MaxAttempts: 1}, func(int) (*http.Response, error) {
			return httpResponse(t, http.StatusInternalServerError), nil
		})
		go func() {
			r := <-reqCh
			_ = r.Body.Close()
		}()

		err := wh.SendWebhook(t.Context(), StringMessage("test"))
		require.Error(t, err)
		require.ErrorContains(t, err, "failed to send webhook after 1 attempts")
		require.ErrorContains(t, err, "invalid response status code: 500")
		assert.Equal(t, 1, count())
	})

	t.Run("default is 3 attempts", func(t *testing.T) {
		wh, clock, reqCh, count := setup(t, NewWebhookOpts{}, func(int) (*http.Response, error) {
			return httpResponse(t, http.StatusInternalServerError), nil
		})

		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		doneCh := assertRetries(ctx, clock, reqCh, 3, 20*time.Second)

		err := wh.SendWebhook(ctx, StringMessage("test"))
		require.Error(t, err)
		require.ErrorContains(t, err, "failed to send webhook after 3 attempts")
		require.NoError(t, <-doneCh)
		assert.Equal(t, 3, count())
	})

	t.Run("more attempts and custom interval on 5xx", func(t *testing.T) {
		wh, clock, reqCh, count := setup(t, NewWebhookOpts{MaxAttempts: 5, RetryInterval: 3 * time.Second}, func(n int) (*http.Response, error) {
			if n < 5 {
				return httpResponse(t, http.StatusBadGateway), nil
			}
			return httpResponse(t, http.StatusOK), nil
		})

		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		doneCh := assertRetries(ctx, clock, reqCh, 5, 3*time.Second)

		err := wh.SendWebhook(ctx, StringMessage("test"))
		require.NoError(t, err)
		require.NoError(t, <-doneCh)
		assert.Equal(t, 5, count())
	})

	t.Run("custom interval on 429 without Retry-After", func(t *testing.T) {
		wh, clock, reqCh, _ := setup(t, NewWebhookOpts{RetryInterval: 4 * time.Second}, func(n int) (*http.Response, error) {
			if n < 2 {
				return httpResponse(t, http.StatusTooManyRequests), nil
			}
			return httpResponse(t, http.StatusOK), nil
		})

		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		doneCh := assertRetries(ctx, clock, reqCh, 2, 4*time.Second)

		err := wh.SendWebhook(ctx, StringMessage("test"))
		require.NoError(t, err)
		require.NoError(t, <-doneCh)
	})

	t.Run("Retry-After takes precedence over the custom interval", func(t *testing.T) {
		wh, clock, reqCh, _ := setup(t, NewWebhookOpts{RetryInterval: 30 * time.Second}, func(n int) (*http.Response, error) {
			if n < 2 {
				res := httpResponse(t, http.StatusTooManyRequests)
				res.Header.Set("Retry-After", "2")
				return res, nil
			}
			return httpResponse(t, http.StatusOK), nil
		})

		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		doneCh := assertRetries(ctx, clock, reqCh, 2, 2*time.Second)

		err := wh.SendWebhook(ctx, StringMessage("test"))
		require.NoError(t, err)
		require.NoError(t, <-doneCh)
	})

	t.Run("network errors wait 20 seconds by default, like other retried failures", func(t *testing.T) {
		wh, clock, reqCh, _ := setup(t, NewWebhookOpts{}, func(n int) (*http.Response, error) {
			if n < 2 {
				return nil, errors.New("connection reset")
			}
			return httpResponse(t, http.StatusOK), nil
		})

		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		doneCh := assertRetries(ctx, clock, reqCh, 2, 20*time.Second)

		err := wh.SendWebhook(ctx, StringMessage("test"))
		require.NoError(t, err)
		require.NoError(t, <-doneCh)
	})

	t.Run("network errors use the custom interval", func(t *testing.T) {
		wh, clock, reqCh, _ := setup(t, NewWebhookOpts{RetryInterval: 2 * time.Second}, func(n int) (*http.Response, error) {
			if n < 2 {
				return nil, errors.New("connection reset")
			}
			return httpResponse(t, http.StatusOK), nil
		})

		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		doneCh := assertRetries(ctx, clock, reqCh, 2, 2*time.Second)

		err := wh.SendWebhook(ctx, StringMessage("test"))
		require.NoError(t, err)
		require.NoError(t, <-doneCh)
	})
}

func TestWebhookTimeoutOption(t *testing.T) {
	release := make(chan struct{})
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	wh, _ := newTestWebhookClient(t, NewWebhookOpts{
		URL:             srv.URL,
		Timeout:         100 * time.Millisecond,
		MaxAttempts:     1,
		AllowPrivateIPs: true,
	})

	start := time.Now()
	err := wh.SendWebhook(t.Context(), StringMessage("test"))
	require.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second)
	assert.Equal(t, int32(1), requests.Load())
}

func TestWebhookAllowPrivateIPsOption(t *testing.T) {
	var gotBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody.Store(string(body))
	}))
	defer srv.Close()

	t.Run("blocked by default", func(t *testing.T) {
		wh, _ := newTestWebhookClient(t, NewWebhookOpts{URL: srv.URL, MaxAttempts: 1})
		err := wh.SendWebhook(t.Context(), StringMessage("test"))
		require.Error(t, err)
		require.ErrorContains(t, err, "refusing to dial private/internal IP")
	})

	t.Run("allowed when enabled", func(t *testing.T) {
		wh, _ := newTestWebhookClient(t, NewWebhookOpts{URL: srv.URL, AllowPrivateIPs: true})
		err := wh.SendWebhook(t.Context(), StringMessage("private hello"))
		require.NoError(t, err)
		assert.Equal(t, "private hello", gotBody.Load())
	})
}
