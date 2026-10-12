package objstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/italypaleale/go-kit/objstore/verceloidc"
)

var (
	_ Backend                 = (*VercelBlobBackend)(nil)
	_ DirectUploadBackend     = (*VercelBlobBackend)(nil)
	_ PublicAddressingBackend = (*VercelBlobBackend)(nil)
)

func TestParseVercelBlobDSN(t *testing.T) {
	t.Run("read-write token from the environment", func(t *testing.T) {
		t.Setenv("BLOB_READ_WRITE_TOKEN", "vercel_blob_rw_storeid_secret")

		backend, err := newVercelBlobBackendFromDSN("vercelblob:?access=public&prefix=thumbnails", "")
		require.NoError(t, err)

		// The store ID is the fourth segment of a read-write token
		assert.Equal(t, "storeid", backend.storeID)
		assert.Equal(t, accessPublic, backend.access)
		assert.Equal(t, "thumbnails", backend.prefix)

		endpoint, err := backend.objectURL("wf1/200x200.webp", false)
		require.NoError(t, err)
		assert.Equal(t, "https://storeid.public.blob.vercel-storage.com/thumbnails/wf1/200x200.webp", endpoint)

		// Blob honors the cache parameter for a private store only, so asking a public one to bypass its cache changes nothing
		endpoint, err = backend.objectURL("wf1/200x200.webp", true)
		require.NoError(t, err)
		assert.Equal(t, "https://storeid.public.blob.vercel-storage.com/thumbnails/wf1/200x200.webp", endpoint)
	})

	t.Run("OIDC with a store ID from the environment", func(t *testing.T) {
		t.Setenv("BLOB_ORIGINALS_STORE_ID", "store_abc123")
		t.Setenv(verceloidc.Environment, "an-oidc-token")

		backend, err := newVercelBlobBackendFromDSN("vercelblob:?auth=oidc&storeIdEnv=BLOB_ORIGINALS_STORE_ID", "")
		require.NoError(t, err)

		assert.Equal(t, "abc123", backend.storeID)
		// Private is the default, since the objects of a store are not necessarily meant to be served to browsers
		assert.Equal(t, accessPrivate, backend.access)

		// The token is resolved for every operation, because an OIDC token expires while the process keeps running
		token, err := backend.tokenFn(t.Context())
		require.NoError(t, err)
		assert.Equal(t, "an-oidc-token", token)

		endpoint, err := backend.objectURL("wf1.jpg", false)
		require.NoError(t, err)
		assert.Equal(t, "https://abc123.private.blob.vercel-storage.com/wf1.jpg", endpoint)

		// A read that must see the object as it is now goes to origin storage instead of the CDN
		endpoint, err = backend.objectURL("wf1.jpg", true)
		require.NoError(t, err)
		assert.Equal(t, "https://abc123.private.blob.vercel-storage.com/wf1.jpg?cache=0", endpoint)
	})

	t.Run("all the options", func(t *testing.T) {
		cfg, err := parseVercelBlobDSN("vercelblob:?auth=oidc&storeId=store_assets&oidcTokenEnv=PIXEL_BLOB_OIDC_TOKEN&access=public&prefix=images")
		require.NoError(t, err)

		assert.Equal(t, authOIDC, cfg.Auth)
		assert.Equal(t, "store_assets", cfg.StoreID)
		assert.Empty(t, cfg.StoreIDEnv)
		assert.Equal(t, "PIXEL_BLOB_OIDC_TOKEN", cfg.OIDCTokenEnv)
		assert.Equal(t, accessPublic, cfg.Access)
		assert.Equal(t, "images", cfg.KeyPrefix)
	})

	// A stale token left in the configuration must not silently take over from OIDC
	t.Run("rejects mixed credential modes", func(t *testing.T) {
		_, err := parseVercelBlobDSN("vercelblob:?auth=oidc&tokenEnv=BLOB_READ_WRITE_TOKEN")
		require.ErrorContains(t, err, "must not set 'token'")

		_, err = parseVercelBlobDSN("vercelblob:?auth=token&storeId=store_assets")
		require.ErrorContains(t, err, "must not set the OIDC store options")
	})
}

func TestVercelBlobOIDCTokenResolutionOrder(t *testing.T) {
	t.Setenv("PIXEL_BLOB_OIDC_TOKEN", "configured-token")
	t.Setenv(verceloidc.Environment, "official-environment-token")

	// The token on the request is the freshest one, since Vercel rotates the deployment token
	requestCtx := verceloidc.WithToken(t.Context(), "request-token")
	token, err := resolveVercelOIDCToken(requestCtx, "PIXEL_BLOB_OIDC_TOKEN")
	require.NoError(t, err)
	assert.Equal(t, "request-token", token)

	token, err = resolveVercelOIDCToken(t.Context(), "PIXEL_BLOB_OIDC_TOKEN")
	require.NoError(t, err)
	assert.Equal(t, "configured-token", token)

	t.Setenv("PIXEL_BLOB_OIDC_TOKEN", "")
	token, err = resolveVercelOIDCToken(t.Context(), "PIXEL_BLOB_OIDC_TOKEN")
	require.NoError(t, err)
	assert.Equal(t, "official-environment-token", token)

	t.Setenv(verceloidc.Environment, "")
	_, err = resolveVercelOIDCToken(t.Context(), "PIXEL_BLOB_OIDC_TOKEN")
	require.Error(t, err)
}

func TestNewVercelBlobOIDCBackendRequiresAStoreID(t *testing.T) {
	t.Setenv("PIXEL_TEST_BLOB_STORE_ID", "store_assets")

	backend, err := newVercelBlobBackend(vercelBlobConfig{
		Auth:       authOIDC,
		StoreIDEnv: "PIXEL_TEST_BLOB_STORE_ID",
		Access:     accessPublic,
	})
	require.NoError(t, err)
	// Signing a browser upload token needs the read-write token OIDC authentication doesn't have
	assert.False(t, backend.SupportsDirectUpload())

	_, err = newVercelBlobBackend(vercelBlobConfig{Auth: authOIDC, StoreIDEnv: "MISSING_BLOB_STORE_ID"})
	require.ErrorContains(t, err, "store ID")
}

func TestVercelBlobPut(t *testing.T) {
	t.Run("writes an object and reports the URL of a public one", func(t *testing.T) {
		var request *http.Request
		var received []byte
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			request = r.Clone(r.Context())
			var err error
			received, err = io.ReadAll(r.Body)
			assert.NoError(t, err)

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"url": "https://storeid.public.blob.vercel-storage.com/images/wf1.jpg"})
		}))
		t.Cleanup(server.Close)

		backend := newTestVercelBlobBackend(t, server, accessPublic, "images")

		result, err := backend.Put(t.Context(), "wf1.jpg", strings.NewReader("image"), 1024, "image/jpeg")
		require.NoError(t, err)
		assert.Equal(t, int64(5), result.Size)
		// The URL is what Vercel returned, since it's Vercel that decides it
		assert.Equal(t, "https://storeid.public.blob.vercel-storage.com/images/wf1.jpg", result.URL)
		assert.Equal(t, []byte("image"), received)

		// The prefix is the backend's own concern, so the pathname it writes carries it
		assert.Equal(t, "images/wf1.jpg", request.URL.Query().Get("pathname"))
		assert.Equal(t, "Bearer vercel_blob_rw_storeid_secret", request.Header.Get("Authorization"))
		assert.Equal(t, accessPublic, request.Header.Get("X-Vercel-Blob-Access"))
		assert.Equal(t, "image/jpeg", request.Header.Get("X-Content-Type"))
		// A retried write regenerates the same object at the same key
		assert.Equal(t, "1", request.Header.Get("X-Allow-Overwrite"))
		assert.Equal(t, "0", request.Header.Get("X-Add-Random-Suffix"))
		assert.Equal(t, int64(5), request.ContentLength)
	})

	t.Run("overwrites the canonical key when a write is retried", func(t *testing.T) {
		objects := make(map[string]string)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			pathname := r.URL.Query().Get("pathname")
			_, exists := objects[pathname]
			if exists && r.Header.Get("X-Allow-Overwrite") != "1" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"code":"bad_request","message":"blob already exists"}}`))
				return
			}

			body, err := io.ReadAll(r.Body)
			assert.NoError(t, err)
			objects[pathname] = string(body)
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(server.Close)

		backend := newTestVercelBlobBackend(t, server, accessPrivate, "")

		_, err := backend.Put(t.Context(), "wf1.jpg", strings.NewReader("first"), 1024, "image/jpeg")
		require.NoError(t, err)
		_, err = backend.Put(t.Context(), "wf1.jpg", strings.NewReader("second"), 1024, "image/jpeg")
		require.NoError(t, err)
		assert.Equal(t, "second", objects["wf1.jpg"])
	})

	// A private object has no URL of its own, so the response body isn't read at all
	t.Run("reports no URL for a private object", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(server.Close)

		backend := newTestVercelBlobBackend(t, server, accessPrivate, "")

		result, err := backend.Put(t.Context(), "wf1.jpg", strings.NewReader("image"), 1024, "image/jpeg")
		require.NoError(t, err)
		assert.Empty(t, result.URL)
	})

	t.Run("reports why a request was rejected", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"message":"the token is not valid"}}`))
		}))
		t.Cleanup(server.Close)

		backend := newTestVercelBlobBackend(t, server, accessPrivate, "")

		_, err := backend.Put(t.Context(), "wf1.jpg", strings.NewReader("image"), 1024, "image/jpeg")
		require.ErrorContains(t, err, "the token is not valid")
	})

	t.Run("rejects an object that is too large", func(t *testing.T) {
		backend := newTestVercelBlobBackend(t, httptest.NewServer(http.NotFoundHandler()), accessPrivate, "")

		_, err := backend.Put(t.Context(), "wf1.jpg", strings.NewReader("image"), 2, "image/jpeg")
		require.ErrorIs(t, err, ErrTooLarge)
	})
}

func TestVercelBlobExists(t *testing.T) {
	t.Run("looks the object up through the API rather than the domain reads go to", func(t *testing.T) {
		var request *http.Request
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			request = r.Clone(r.Context())
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"url": "https://storeid.private.blob.vercel-storage.com/images/wf1.jpg"})
		}))

		backend := newTestVercelBlobBackend(t, server, accessPrivate, "images")

		exists, err := backend.Exists(t.Context(), "wf1.jpg")
		require.NoError(t, err)
		assert.True(t, exists)

		// The lookup must not touch the CDN-fronted domain, because a "not found" cached there outlives the write that follows it
		assert.Equal(t, http.MethodGet, request.Method)
		assert.Equal(t, "https://storeid.private.blob.vercel-storage.com/images/wf1.jpg", request.URL.Query().Get("url"))
		assert.Equal(t, "Bearer vercel_blob_rw_storeid_secret", request.Header.Get("Authorization"))
	})

	// The Blob API says what went wrong with a code in the body rather than with the status alone
	t.Run("reports a key that is free to write", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"the requested blob does not exist"}}`))
		}))

		backend := newTestVercelBlobBackend(t, server, accessPrivate, "")

		exists, err := backend.Exists(t.Context(), "wf1.jpg")
		require.NoError(t, err)
		assert.False(t, exists)
	})

	// Anything that isn't that code is a failed lookup, and must not be read as a key nothing is stored at: that would let a write overwrite an object that does exist
	t.Run("fails a lookup that was rejected for another reason", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"forbidden","message":"the token is not valid"}}`))
		}))

		backend := newTestVercelBlobBackend(t, server, accessPrivate, "")

		_, err := backend.Exists(t.Context(), "wf1.jpg")
		require.ErrorContains(t, err, "the token is not valid")
	})
}

func TestVercelBlobCreateDirectUpload(t *testing.T) {
	t.Setenv("BLOB_READ_WRITE_TOKEN", "vercel_blob_rw_storeid_secret")

	backend, err := newVercelBlobBackendFromDSN("vercelblob:?access=public&prefix=images", "")
	require.NoError(t, err)
	assert.True(t, backend.SupportsDirectUpload())

	upload, err := backend.CreateDirectUpload(t.Context(), "wf1.jpg", 1024, "image/jpeg", 15*time.Minute)
	require.NoError(t, err)
	assert.Equal(t, "wf1.jpg", upload.Key)
	assert.Equal(t, http.MethodPut, upload.Method)
	assert.Contains(t, upload.URL, "pathname=images%2Fwf1.jpg")
	assert.Equal(t, accessPublic, upload.Headers["X-Vercel-Blob-Access"])
	assert.Equal(t, "1", upload.Headers["X-Allow-Overwrite"])
	assert.Equal(t, "0", upload.Headers["X-Add-Random-Suffix"])

	// The token authorizes one exact pathname, so the read-write token never has to leave the process
	token := strings.TrimPrefix(upload.Headers["Authorization"], "Bearer ")
	assert.True(t, strings.HasPrefix(token, "vercel_blob_client_storeid_"))

	secured, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(token, "vercel_blob_client_storeid_"))
	require.NoError(t, err)
	_, payloadEncoded, found := strings.Cut(string(secured), ".")
	require.True(t, found)
	payload, err := base64.StdEncoding.DecodeString(payloadEncoded)
	require.NoError(t, err)

	var claims vercelClientTokenPayload
	require.NoError(t, json.Unmarshal(payload, &claims))
	assert.Equal(t, "images/wf1.jpg", claims.Pathname)
	assert.Equal(t, int64(1024), claims.MaximumSizeInBytes)
	assert.Equal(t, []string{"image/jpeg"}, claims.AllowedContentTypes)
	assert.Greater(t, claims.ValidUntil, time.Now().UnixMilli())
}

func TestVercelBlobDescription(t *testing.T) {
	t.Setenv("BLOB_READ_WRITE_TOKEN", "vercel_blob_rw_storeid_secret")

	backend, err := newVercelBlobBackendFromDSN("vercelblob:?access=public&prefix=images", "")
	require.NoError(t, err)

	desc := backend.Description()
	assert.Contains(t, desc, "storeid")
	assert.Contains(t, desc, accessPublic)
	assert.Contains(t, desc, "images")
	// The token that authenticates the requests never reaches the logs
	assert.NotContains(t, desc, "secret")
}

func TestVercelBlobRejectsAnInvalidKey(t *testing.T) {
	backend := newTestVercelBlobBackend(t, httptest.NewServer(http.NotFoundHandler()), accessPrivate, "")

	_, err := backend.Put(t.Context(), "../escape.jpg", strings.NewReader("image"), 1024, "image/jpeg")
	require.Error(t, err)

	_, _, err = backend.Get(t.Context(), "../escape.jpg")
	require.Error(t, err)

	require.Error(t, backend.Delete(t.Context(), "../escape.jpg"))
}

// Point a backend at a test server, which is what lets the requests it makes be inspected
func newTestVercelBlobBackend(t *testing.T, server *httptest.Server, access string, prefix string) *VercelBlobBackend {
	t.Helper()
	t.Cleanup(server.Close)

	const token = "vercel_blob_rw_storeid_secret" //nolint:gosec // This is a fake token, shaped like a real one so the store ID can be read out of it

	return &VercelBlobBackend{
		storeID:        "storeid",
		access:         access,
		prefix:         prefix,
		readWriteToken: token,
		tokenFn: func(_ context.Context) (string, error) {
			return token, nil
		},
		apiEndpoint: server.URL,
		httpClient:  server.Client(),
	}
}
