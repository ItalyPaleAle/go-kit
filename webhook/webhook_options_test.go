package webhook

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	clocktesting "k8s.io/utils/clock/testing"
)

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

func TestNewWebhookOptionsDefaults(t *testing.T) {
	// The zero value of every option must keep the behavior from before the options existed
	whAny, err := NewWebhook(NewWebhookOpts{URL: "https://hooks.example.com/endpoint"})
	require.NoError(t, err)

	wh := whAny.(*webhookClient) //nolint:forcetypeassert
	assert.Equal(t, 20*time.Second, wh.timeout)
	assert.Equal(t, 20*time.Second, wh.httpClient.Timeout)
	assert.Equal(t, 3, wh.maxAttempts)
	assert.Equal(t, 20*time.Second, wh.retryInterval)
	assert.Equal(t, 15*time.Second, wh.networkRetryDelay)

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
	assert.Equal(t, 2*time.Second, wh.networkRetryDelay)
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
		return wh, clock, reqCh, func() int { return int(counter.Load()) }
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

	t.Run("network errors wait 15 seconds by default", func(t *testing.T) {
		wh, clock, reqCh, _ := setup(t, NewWebhookOpts{}, func(n int) (*http.Response, error) {
			if n < 2 {
				return nil, errors.New("connection reset")
			}
			return httpResponse(t, http.StatusOK), nil
		})

		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		doneCh := assertRetries(ctx, clock, reqCh, 2, 15*time.Second)

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

func TestWebhookFollowRedirectsOption(t *testing.T) {
	const authHeader = "X-Webhook-Key"

	type seen struct {
		path   string
		method string
		body   string
		auth   string
		xKey   string
	}
	record := func(ch chan seen) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			ch <- seen{
				path:   r.URL.Path,
				method: r.Method,
				body:   string(body),
				auth:   r.Header.Get("Authorization"),
				xKey:   r.Header.Get(authHeader),
			}
		}
	}

	t.Run("not followed by default", func(t *testing.T) {
		target := make(chan seen, 1)
		dst := httptest.NewServer(record(target))
		defer dst.Close()
		src := httptest.NewServer(http.RedirectHandler(dst.URL+"/final", http.StatusTemporaryRedirect))
		defer src.Close()

		wh, _ := newTestWebhookClient(t, NewWebhookOpts{URL: src.URL, AllowPrivateIPs: true})
		err := wh.SendWebhook(t.Context(), StringMessage("test"))
		require.Error(t, err)
		require.ErrorContains(t, err, "invalid response status code: 307")
		assert.Empty(t, target)
	})

	t.Run("followed when enabled, preserving method and body", func(t *testing.T) {
		target := make(chan seen, 1)
		dst := httptest.NewServer(record(target))
		defer dst.Close()
		for _, code := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
			src := httptest.NewServer(http.RedirectHandler(dst.URL+"/final", code))
			defer src.Close()

			wh, _ := newTestWebhookClient(t, NewWebhookOpts{URL: src.URL, AllowPrivateIPs: true, FollowRedirects: true})
			err := wh.SendWebhook(t.Context(), StringMessage("redirected"))
			require.NoError(t, err)

			got := <-target
			assert.Equal(t, seen{path: "/final", method: http.MethodPost, body: "redirected"}, got)
		}
	})

	t.Run("redirects that would turn the POST into a GET are not followed", func(t *testing.T) {
		for _, code := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther} {
			target := make(chan seen, 1)
			dst := httptest.NewServer(record(target))
			defer dst.Close()
			src := httptest.NewServer(http.RedirectHandler(dst.URL+"/final", code))
			defer src.Close()

			wh, _ := newTestWebhookClient(t, NewWebhookOpts{URL: src.URL, AllowPrivateIPs: true, FollowRedirects: true})
			err := wh.SendWebhook(t.Context(), StringMessage("test"))
			require.Error(t, err)
			assert.Empty(t, target)
		}
	})

	t.Run("stops after too many redirects", func(t *testing.T) {
		var hits atomic.Int32
		var srv *httptest.Server
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			http.Redirect(w, r, srv.URL+"/again", http.StatusTemporaryRedirect)
		}))
		defer srv.Close()

		wh, _ := newTestWebhookClient(t, NewWebhookOpts{URL: srv.URL, AllowPrivateIPs: true, FollowRedirects: true, MaxAttempts: 1})
		err := wh.SendWebhook(t.Context(), StringMessage("test"))
		require.Error(t, err)
		require.ErrorContains(t, err, "stopped after 5 redirects")
		assert.Equal(t, int32(maxRedirects), hits.Load())
	})

	t.Run("key is kept on a redirect to the same host", func(t *testing.T) {
		target := make(chan seen, 1)
		var srv *httptest.Server
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/start" {
				http.Redirect(w, r, srv.URL+"/final", http.StatusPermanentRedirect)
				return
			}
			record(target)(w, r)
		}))
		defer srv.Close()

		wh, _ := newTestWebhookClient(t, NewWebhookOpts{
			URL:                 srv.URL + "/start",
			Key:                 "secret",
			AuthorizationHeader: authHeader,
			AllowPrivateIPs:     true,
			FollowRedirects:     true,
		})
		err := wh.SendWebhook(t.Context(), StringMessage("test"))
		require.NoError(t, err)

		got := <-target
		assert.Equal(t, "secret", got.xKey)
	})

	t.Run("key is dropped on a redirect to a different host", func(t *testing.T) {
		for _, header := range []string{"", authHeader} {
			target := make(chan seen, 1)
			dst := httptest.NewServer(record(target))
			defer dst.Close()
			src := httptest.NewServer(http.RedirectHandler(dst.URL+"/final", http.StatusTemporaryRedirect))
			defer src.Close()

			wh, _ := newTestWebhookClient(t, NewWebhookOpts{
				URL:                 src.URL,
				Key:                 "secret",
				AuthorizationHeader: header,
				AllowPrivateIPs:     true,
				FollowRedirects:     true,
			})
			err := wh.SendWebhook(t.Context(), StringMessage("test"))
			require.NoError(t, err)

			got := <-target
			assert.Equal(t, "/final", got.path)
			assert.Empty(t, got.auth, "header %q", header)
			assert.Empty(t, got.xKey, "header %q", header)
		}
	})

	t.Run("private destinations are still blocked when following redirects", func(t *testing.T) {
		dst := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		defer dst.Close()

		// The first hop is private, so it's refused before any redirect happens
		wh, _ := newTestWebhookClient(t, NewWebhookOpts{URL: dst.URL, FollowRedirects: true, MaxAttempts: 1})
		err := wh.SendWebhook(t.Context(), StringMessage("test"))
		require.Error(t, err)
		require.ErrorContains(t, err, "refusing to dial private/internal IP")
	})
}

func TestCheckRedirectFuncPolicy(t *testing.T) {
	newReq := func(t *testing.T, rawURL string, status int) *http.Request {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, rawURL, strings.NewReader(""))
		require.NoError(t, err)
		req.Response = &http.Response{StatusCode: status}
		req.Header.Set("Authorization", "secret")
		req.Header.Set("X-Key", "secret")
		return req
	}

	check := checkRedirectFunc(NewWebhookOpts{FollowRedirects: true, AuthorizationHeader: "X-Key"})
	via := []*http.Request{newReq(t, "https://a.example.com/hook", 0)}

	t.Run("drops keys when the scheme changes", func(t *testing.T) {
		req := newReq(t, "http://a.example.com/hook", http.StatusTemporaryRedirect)
		err := check(req, via)
		require.NoError(t, err)
		assert.Empty(t, req.Header.Get("Authorization"))
		assert.Empty(t, req.Header.Get("X-Key"))
	})

	t.Run("drops keys when the port changes", func(t *testing.T) {
		req := newReq(t, "https://a.example.com:8443/hook", http.StatusTemporaryRedirect)
		err := check(req, via)
		require.NoError(t, err)
		assert.Empty(t, req.Header.Get("Authorization"))
		assert.Empty(t, req.Header.Get("X-Key"))
	})

	t.Run("keeps keys on the same origin", func(t *testing.T) {
		req := newReq(t, "https://a.example.com/other", http.StatusPermanentRedirect)
		err := check(req, via)
		require.NoError(t, err)
		assert.Equal(t, "secret", req.Header.Get("Authorization"))
		assert.Equal(t, "secret", req.Header.Get("X-Key"))
	})
}
