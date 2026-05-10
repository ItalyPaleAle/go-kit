package webhook

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	clocktesting "k8s.io/utils/clock/testing"

	"github.com/italypaleale/go-kit/testutils"
)

type testMessageProvider struct {
	message string
}

func (tmp testMessageProvider) GetPlainMessage() (string, error) {
	return tmp.message, nil
}

func (tmp testMessageProvider) GetSlackMessage() (SlackMessage, error) {
	return SlackMessage{
		Text: tmp.message,
	}, nil
}

func TestWebhook(t *testing.T) {
	clock := clocktesting.NewFakeClock(time.Now())
	whAny, err := newWebhookInternal(NewWebhookOpts{
		URL:    "http://198.51.100.10/endpoint",
		Logger: slog.New(slog.DiscardHandler),
		clock:  clock,
	})
	require.NoError(t, err, "Failed to create webhook client")

	wh := whAny.(*webhookClient) //nolint:forcetypeassert

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
		resCh <- &http.Response{StatusCode: http.StatusTooManyRequests}
		resCh <- &http.Response{StatusCode: http.StatusTooManyRequests}
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
			res := &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     make(http.Header),
			}
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
		resCh <- &http.Response{StatusCode: http.StatusInternalServerError}
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
		resCh <- &http.Response{StatusCode: http.StatusTooManyRequests}
		resCh <- &http.Response{StatusCode: http.StatusTooManyRequests}
		resCh <- &http.Response{StatusCode: http.StatusTooManyRequests}
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
		resCh <- &http.Response{StatusCode: http.StatusInternalServerError}
		resCh <- &http.Response{StatusCode: http.StatusBadGateway}
		resCh <- &http.Response{StatusCode: http.StatusBadGateway}
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
}

// TODO
//
//nolint:unused
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
