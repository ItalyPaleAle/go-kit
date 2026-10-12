package clientassertion

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// Write a token file and return its path
func writeTokenFile(t *testing.T, content string) string {
	t.Helper()

	tokenFile, err := os.CreateTemp(t.TempDir(), "k8s-sa-token-*")
	require.NoError(t, err)

	_, err = tokenFile.WriteString(content)
	require.NoError(t, err)

	err = tokenFile.Close()
	require.NoError(t, err)

	return tokenFile.Name()
}

func TestNewProvider(t *testing.T) {
	t.Run("returns no provider when the value is empty", func(t *testing.T) {
		fn, err := NewProvider("", "audience-not-used")
		require.NoError(t, err)
		assert.Nil(t, fn)
	})

	t.Run("fails on an unknown value", func(t *testing.T) {
		_, err := NewProvider("NotAProvider", "audience-not-used")
		require.Error(t, err)
		require.ErrorContains(t, err, "invalid value for configuration option 'clientAssertion'")
	})

	t.Run("supports an explicit Kubernetes token path", func(t *testing.T) {
		tokenPath := writeTokenFile(t, "token-via-new-provider\n")

		fn, err := NewProvider("KubernetesServiceAccountToken="+tokenPath, "audience-not-used")
		require.NoError(t, err)
		require.NotNil(t, fn)

		token, err := fn(t.Context())
		require.NoError(t, err)
		assert.Equal(t, "token-via-new-provider", token)
	})

	t.Run("supports an omitted Kubernetes token path", func(t *testing.T) {
		fn, err := NewProvider("KubernetesServiceAccountToken", "audience-not-used")
		require.NoError(t, err)
		require.NotNil(t, fn)
	})

	t.Run("matches values case-insensitively", func(t *testing.T) {
		tokenPath := writeTokenFile(t, "token-lowercase\n")

		fn, err := NewProvider("kubernetesserviceaccounttoken="+tokenPath, "audience-not-used")
		require.NoError(t, err)
		require.NotNil(t, fn)

		token, err := fn(t.Context())
		require.NoError(t, err)
		assert.Equal(t, "token-lowercase", token)
	})

	t.Run("tsiam requires an audience", func(t *testing.T) {
		_, err := NewProvider("tsiam=https://tsiam.example.com", "")
		require.Error(t, err)
		require.ErrorContains(t, err, "audience is empty")
	})
}

func TestTsiamProvider(t *testing.T) {
	t.Run("fails when audience is empty", func(t *testing.T) {
		_, err := tsiamProvider("https://tsiam.example.com", "", http.DefaultClient)
		require.Error(t, err)
		require.ErrorContains(t, err, "audience is empty")
	})

	t.Run("fails when endpoint is invalid", func(t *testing.T) {
		_, err := tsiamProvider("http://[::1", "api://AzureADTokenExchange", http.DefaultClient)
		require.Error(t, err)
		require.ErrorContains(t, err, "failed to parse endpoint URL")
	})

	t.Run("success and caches token", func(t *testing.T) {
		type observedRequest struct {
			method   string
			header   string
			scheme   string
			host     string
			path     string
			resource string
		}

		callCount := atomic.Int32{}
		reqInfoCh := make(chan observedRequest, 1)
		client := &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				if callCount.Add(1) == 1 {
					reqInfoCh <- observedRequest{
						method:   req.Method,
						header:   req.Header.Get("X-Tsiam"),
						scheme:   req.URL.Scheme,
						host:     req.URL.Host,
						path:     req.URL.Path,
						resource: req.URL.Query().Get("resource"),
					}
				}

				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"access_token":"token-1","expires_in":"3600"}`)),
				}, nil
			}),
		}

		fn, err := tsiamProvider("https://tsiam.example.com/", "api://AzureADTokenExchange", client)
		require.NoError(t, err)

		token1, err := fn(t.Context())
		require.NoError(t, err)
		assert.Equal(t, "token-1", token1)

		token2, err := fn(t.Context())
		require.NoError(t, err)
		assert.Equal(t, "token-1", token2)
		assert.EqualValues(t, 1, callCount.Load())

		reqInfo := <-reqInfoCh
		assert.Equal(t, http.MethodPost, reqInfo.method)
		assert.Equal(t, "1", reqInfo.header)
		assert.Equal(t, "https", reqInfo.scheme)
		assert.Equal(t, "tsiam.example.com", reqInfo.host)
		assert.Equal(t, "/token", reqInfo.path)
		assert.Equal(t, "api://AzureADTokenExchange", reqInfo.resource)
	})

	t.Run("fails when response status is not ok", func(t *testing.T) {
		client := &http.Client{
			Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusInternalServerError,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("")),
				}, nil
			}),
		}

		fn, err := tsiamProvider("https://tsiam.example.com", "api://AzureADTokenExchange", client)
		require.NoError(t, err)

		_, err = fn(t.Context())
		require.Error(t, err)
		require.ErrorContains(t, err, "response status is not OK: 500")
	})

	t.Run("fails on invalid JSON body", func(t *testing.T) {
		client := &http.Client{
			Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("{")),
				}, nil
			}),
		}

		fn, err := tsiamProvider("https://tsiam.example.com", "api://AzureADTokenExchange", client)
		require.NoError(t, err)

		_, err = fn(t.Context())
		require.Error(t, err)
		require.ErrorContains(t, err, "error parsing response")
	})

	t.Run("does not cache short or invalid ttl", func(t *testing.T) {
		testCases := []struct {
			name      string
			expiresIn string
		}{
			{name: "short ttl", expiresIn: "30"},
			{name: "invalid ttl", expiresIn: "not-a-number"},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				callCount := atomic.Int32{}
				client := &http.Client{
					Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
						callNum := callCount.Add(1)
						body := fmt.Sprintf(`{"access_token":"token-%d","expires_in":"%s"}`, callNum, tc.expiresIn)
						return &http.Response{
							StatusCode: http.StatusOK,
							Header:     make(http.Header),
							Body:       io.NopCloser(strings.NewReader(body)),
						}, nil
					}),
				}

				fn, err := tsiamProvider("https://tsiam.example.com", "api://AzureADTokenExchange", client)
				require.NoError(t, err)

				token1, err := fn(t.Context())
				require.NoError(t, err)
				token2, err := fn(t.Context())
				require.NoError(t, err)

				assert.Equal(t, "token-1", token1)
				assert.Equal(t, "token-2", token2)
				assert.EqualValues(t, 2, callCount.Load())
			})
		}
	})

	t.Run("singleflight deduplicates concurrent requests", func(t *testing.T) {
		callCount := atomic.Int32{}
		client := &http.Client{
			Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				select {
				case <-time.After(200 * time.Millisecond):
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}

				callCount.Add(1)

				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"access_token":"token-shared","expires_in":"3600"}`)),
				}, nil
			}),
		}

		fn, err := tsiamProvider("https://tsiam.example.com", "api://AzureADTokenExchange", client)
		require.NoError(t, err)

		const workers = 20
		start := make(chan struct{})
		results := make(chan string, workers)
		errs := make(chan error, workers)

		var wg sync.WaitGroup
		for range workers {
			wg.Go(func() {
				<-start

				token, rErr := fn(t.Context())
				if rErr != nil {
					errs <- rErr
					return
				}
				results <- token
			})
		}

		close(start)
		wg.Wait()
		close(results)
		close(errs)

		for callErr := range errs {
			require.NoError(t, callErr)
		}

		for token := range results {
			assert.Equal(t, "token-shared", token)
		}
		assert.EqualValues(t, 1, callCount.Load())
	})
}

func TestKubernetesServiceAccountTokenProvider(t *testing.T) {
	t.Run("fails when token path is empty", func(t *testing.T) {
		_, err := kubernetesServiceAccountTokenProvider("")
		require.Error(t, err)
		require.ErrorContains(t, err, "token path is empty")
	})

	t.Run("reads and trims token from explicit path", func(t *testing.T) {
		tokenPath := writeTokenFile(t, "token-abc123\n")

		fn, err := kubernetesServiceAccountTokenProvider(tokenPath)
		require.NoError(t, err)

		token, err := fn(t.Context())
		require.NoError(t, err)
		assert.Equal(t, "token-abc123", token)
	})

	t.Run("rereads the file so rotated tokens are picked up", func(t *testing.T) {
		tokenPath := writeTokenFile(t, "token-before\n")

		fn, err := kubernetesServiceAccountTokenProvider(tokenPath)
		require.NoError(t, err)

		token, err := fn(t.Context())
		require.NoError(t, err)
		assert.Equal(t, "token-before", token)

		err = os.WriteFile(tokenPath, []byte("token-after\n"), 0o600)
		require.NoError(t, err)

		token, err = fn(t.Context())
		require.NoError(t, err)
		assert.Equal(t, "token-after", token)
	})

	t.Run("fails when token file does not exist", func(t *testing.T) {
		fn, err := kubernetesServiceAccountTokenProvider(t.TempDir() + "/missing-token")
		require.NoError(t, err)

		_, err = fn(t.Context())
		require.Error(t, err)
		require.ErrorContains(t, err, "failed to read Kubernetes service account token")
	})

	t.Run("fails when token file is empty", func(t *testing.T) {
		tokenPath := writeTokenFile(t, "")

		fn, err := kubernetesServiceAccountTokenProvider(tokenPath)
		require.NoError(t, err)

		_, err = fn(t.Context())
		require.Error(t, err)
		require.ErrorContains(t, err, "is empty")
	})
}
