package webhook

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	clocktesting "k8s.io/utils/clock/testing"

	"github.com/italypaleale/go-kit/testutils"
)

type testMessageProvider struct {
	message      string
	slackMessage SlackMessage
	plainErr     error
	slackErr     error
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
		require.Equal(t, webhookTimeout, wh.httpClient.Timeout)
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
		defer req.Body.Close()
		require.Equal(t, http.MethodPost, req.Method)
		require.Equal(t, "http://198.51.100.10/endpoint", req.URL.String())
		require.Equal(t, "text/plain", req.Header.Get("Content-Type"))
		require.Equal(t, "Bearer secret-token", req.Header.Get("Authorization"))
		requireBodyEqual(t, req.Body, "plain message body")
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
		defer req.Body.Close()
		require.Equal(t, http.MethodPost, req.Method)
		require.Equal(t, "application/json", req.Header.Get("Content-Type"))
		require.Equal(t, "ApiKey test-key", req.Header.Get("Authorization"))
		requireBodyEqual(t, req.Body, `{"text":"hello <team> & friends"}`+"\n")
	})

	t.Run("Discord webhooks reuse the slack-compatible formatter", func(t *testing.T) {
		wh, _ := newTestWebhookClient(t, NewWebhookOpts{Format: FormatDiscord})

		req, err := wh.prepareSlackRequest(t.Context(), wh.webhookURL, testMessageProvider{message: "discord payload"})
		require.NoError(t, err)
		defer req.Body.Close()
		require.Equal(t, "application/json", req.Header.Get("Content-Type"))
		requireBodyEqual(t, req.Body, `{"text":"discord payload"}`+"\n")
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
		req.Body.Close()
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
		r.Body.Close()
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
		doneCh := assertRetries(ctx, clock, reqCh, 3, retryIntervalSeconds*time.Second)

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
		doneCh := assertRetries(ctx, clock, reqCh, 2, retryIntervalSeconds*time.Second)

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
		doneCh := assertRetries(ctx, clock, reqCh, 3, retryIntervalSeconds*time.Second)

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
		doneCh := assertRetries(ctx, clock, reqCh, 3, retryIntervalSeconds*time.Second)

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
		doneCh := assertRetries(ctx, clock, reqCh, 2, retryIntervalSeconds*time.Second)

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
		doneCh := assertRetries(ctx, clock, reqCh, 3, 15*time.Second)

		err := wh.SendWebhook(ctx, getWebhookData())
		require.Error(t, err)
		require.ErrorContains(t, err, "failed to send webhook after 3 attempts")
		require.ErrorContains(t, err, "dial tcp: i/o timeout")
		require.NoError(t, <-doneCh)
	})

	t.Run("retry after values above the maximum are clamped", func(t *testing.T) {
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
		doneCh := assertRetries(ctx, clock, reqCh, 2, retryIntervalSeconds*time.Second)

		err := wh.SendWebhook(ctx, getWebhookData())
		require.NoError(t, err)
		require.NoError(t, <-doneCh)
	})
}

func requireBodyEqual(t *testing.T, body io.ReadCloser, expect string) {
	t.Helper()

	read, err := io.ReadAll(body)
	require.NoError(t, err, "failed to read body")

	require.Equal(t, expect, string(read))
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
				r.Body.Close()
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
