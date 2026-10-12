package objstore

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	_ Backend                 = (*S3Backend)(nil)
	_ DirectUploadBackend     = (*S3Backend)(nil)
	_ PublicAddressingBackend = (*S3Backend)(nil)
)

// Point a backend at a test server, which is what lets the request it signs be inspected
func newTestS3Backend(t *testing.T, server *httptest.Server, cfg s3Config) *S3Backend {
	t.Helper()

	cfg.Endpoint = server.URL
	if cfg.Bucket == "" {
		cfg.Bucket = "bucket"
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	cfg.AccessKey = "key"
	cfg.SecretKey = "secret"

	return &S3Backend{cfg: cfg, httpClient: server.Client()}
}

func TestParseS3DSN(t *testing.T) {
	t.Run("AWS endpoint", func(t *testing.T) {
		cfg, err := parseS3DSN("s3://key:secret@s3.eu-central-1.amazonaws.com/thumbnails?prefix=imageoptim&sessionToken=session")
		require.NoError(t, err)

		assert.Equal(t, "thumbnails", cfg.Bucket)
		// The region is derived from the AWS-style host when the DSN doesn't spell it out
		assert.Equal(t, "eu-central-1", cfg.Region)
		assert.Equal(t, "imageoptim", cfg.KeyPrefix)
		assert.Equal(t, "key", cfg.AccessKey)
		assert.Equal(t, "secret", cfg.SecretKey)
		assert.Equal(t, "session", cfg.Session)
		assert.Empty(t, cfg.PublicURL)
	})

	t.Run("S3-compatible endpoint", func(t *testing.T) {
		cfg, err := parseS3DSN("s3://key:secret@storage.example.com/thumbnails?region=us-west-1")
		require.NoError(t, err)

		assert.Equal(t, "https://storage.example.com", cfg.Endpoint)
		assert.Equal(t, "us-west-1", cfg.Region)
	})

	t.Run("a bucket fronted by a CDN", func(t *testing.T) {
		cfg, err := parseS3DSN("s3://key:secret@s3.us-east-1.amazonaws.com/bucket?publicUrl=https%3A%2F%2Fassets.example.test%2Fbase%2F")
		require.NoError(t, err)

		// The trailing slash is dropped, since the key is joined onto this
		assert.Equal(t, "https://assets.example.test/base", cfg.PublicURL)
	})
}

// The request an object is read with must be signed, and must address the right bucket and key
func TestS3ObjectURL(t *testing.T) {
	backend, err := newS3BackendFromDSN("s3://key:secret@s3.us-east-1.amazonaws.com/thumbnails?prefix=imageoptim")
	require.NoError(t, err)

	// The DSN carries an endpoint, so the bucket is addressed as the first path segment, and the configured prefix goes in front of the key
	endpoint, err := backend.objectURL("wf1/200x200.webp")
	require.NoError(t, err)
	assert.Equal(t, "https://s3.us-east-1.amazonaws.com/thumbnails/imageoptim/wf1/200x200.webp", endpoint.String())

	// A DSN with no endpoint at all addresses the bucket as a subdomain instead
	virtualHosted, err := newS3BackendFromDSN("s3://key:secret@/thumbnails?region=us-east-1")
	require.NoError(t, err)
	endpoint, err = virtualHosted.objectURL("wf1/200x200.webp")
	require.NoError(t, err)
	assert.Equal(t, "https://thumbnails.s3.us-east-1.amazonaws.com/wf1/200x200.webp", endpoint.String())

	signed, err := backend.presignedURL(http.MethodGet, "wf1/200x200.webp", "", inProcessPresignTTL)
	require.NoError(t, err)
	assert.Contains(t, signed, "X-Amz-Signature=")
	assert.Contains(t, signed, "X-Amz-Credential=key%2F")
	assert.Contains(t, signed, "X-Amz-Expires=30")
}

func TestS3PutStreamsBoundedBodyAndReturnsPublicURL(t *testing.T) {
	var received []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		assert.Equal(t, "image/webp", r.Header.Get("Content-Type"))
		// The body is spooled first, so the request declares a length rather than being chunked
		assert.Equal(t, int64(5), r.ContentLength)
		var err error
		received, err = io.ReadAll(r.Body)
		assert.NoError(t, err)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	backend := newTestS3Backend(t, server, s3Config{PublicURL: "https://assets.example.test/base"})

	result, err := backend.Put(t.Context(), "site/image.webp", strings.NewReader("image"), 5, "image/webp")
	require.NoError(t, err)
	assert.Equal(t, int64(5), result.Size)
	assert.Equal(t, "https://assets.example.test/base/site/image.webp", result.URL)
	assert.Equal(t, []byte("image"), received)
	assert.True(t, backend.HasPublicAddressing())
}

// The prefix is the backend's own concern, so callers pass logical keys and the prefix appears in the request and in the public URL
func TestS3AppliesTheKeyPrefix(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	backend := newTestS3Backend(t, server, s3Config{KeyPrefix: "images", PublicURL: "https://assets.example.test"})

	result, err := backend.Put(t.Context(), "site/image.webp", strings.NewReader("image"), 5, "image/webp")
	require.NoError(t, err)
	assert.Equal(t, "/bucket/images/site/image.webp", path)
	assert.Equal(t, "https://assets.example.test/images/site/image.webp", result.URL)
}

func TestS3PutRejectsAnOversizeObject(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		assert.Fail(t, "the request must not be sent for an object that is too large")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	backend := newTestS3Backend(t, server, s3Config{})

	_, err := backend.Put(t.Context(), "site/image.webp", strings.NewReader("image"), 2, "image/webp")
	require.ErrorIs(t, err, ErrTooLarge)
}

func TestS3Get(t *testing.T) {
	t.Run("returns the object and its metadata", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodGet, r.Method)
			w.Header().Set("Content-Type", "image/webp")
			_, _ = w.Write([]byte("thumbnail"))
		}))
		t.Cleanup(server.Close)

		backend := newTestS3Backend(t, server, s3Config{})

		body, info, err := backend.Get(t.Context(), "site/image.webp")
		require.NoError(t, err)
		defer body.Close() //nolint:errcheck
		assert.Equal(t, "image/webp", info.ContentType)
		assert.Equal(t, int64(9), info.Size)

		read, err := io.ReadAll(body)
		require.NoError(t, err)
		assert.Equal(t, "thumbnail", string(read))
	})

	t.Run("reports a missing object", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		t.Cleanup(server.Close)

		backend := newTestS3Backend(t, server, s3Config{})

		_, _, err := backend.Get(t.Context(), "site/image.webp")
		require.ErrorIs(t, err, ErrNotFound)
	})

	// The reason S3 rejected a request is in the body, so it belongs in the error
	t.Run("reports why a request was rejected", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("<Error><Code>AccessDenied</Code></Error>"))
		}))
		t.Cleanup(server.Close)

		backend := newTestS3Backend(t, server, s3Config{})

		_, _, err := backend.Get(t.Context(), "site/image.webp")
		require.ErrorContains(t, err, "AccessDenied")
	})
}

func TestS3Exists(t *testing.T) {
	t.Run("looks an object up without retrieving it", func(t *testing.T) {
		var request *http.Request
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			request = r.Clone(r.Context())
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(server.Close)

		backend := newTestS3Backend(t, server, s3Config{KeyPrefix: "images"})

		exists, err := backend.Exists(t.Context(), "site/image.webp")
		require.NoError(t, err)
		assert.True(t, exists)

		// A HEAD retrieves no content, which is the whole point of asking this rather than reading the object
		assert.Equal(t, http.MethodHead, request.Method)
		assert.Equal(t, "/bucket/images/site/image.webp", request.URL.Path)
		assert.Contains(t, request.URL.RawQuery, "X-Amz-Signature=")
	})

	t.Run("reports a key that is free to write", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		t.Cleanup(server.Close)

		backend := newTestS3Backend(t, server, s3Config{})

		exists, err := backend.Exists(t.Context(), "site/image.webp")
		require.NoError(t, err)
		assert.False(t, exists)
	})

	// A rejected lookup must not be read as a key nothing is stored at, since that would let a write overwrite an object that does exist
	t.Run("fails a lookup that was rejected", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		t.Cleanup(server.Close)

		backend := newTestS3Backend(t, server, s3Config{})

		_, err := backend.Exists(t.Context(), "site/image.webp")
		require.Error(t, err)
	})
}

// A delete of a key that doesn't exist isn't an error, because the operation that failed may have been retried
func TestS3DeleteIsIdempotent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	backend := newTestS3Backend(t, server, s3Config{})

	require.NoError(t, backend.Delete(t.Context(), "site/image.webp"))
}

func TestS3CreateDirectUpload(t *testing.T) {
	backend, err := newS3BackendFromDSN("s3://key:secret@s3.us-east-1.amazonaws.com/bucket?prefix=images")
	require.NoError(t, err)
	assert.True(t, backend.SupportsDirectUpload())

	upload, err := backend.CreateDirectUpload(t.Context(), "site/image.webp", 1024, "image/webp", 15*time.Minute)
	require.NoError(t, err)
	assert.Equal(t, "site/image.webp", upload.Key)
	assert.Equal(t, http.MethodPut, upload.Method)
	assert.Equal(t, "image/webp", upload.Headers["Content-Type"])
	// The URL is signed for the lifetime the caller asked for, not for the one the backend uses internally
	assert.Contains(t, upload.URL, "X-Amz-Expires=900")
	assert.Contains(t, upload.URL, "/bucket/images/site/image.webp")
	assert.Contains(t, upload.URL, "X-Amz-Signature=")
}

// A failed request carries the signature and the session token in its URL, so they must not reach a log
func TestSanitizeS3RequestError(t *testing.T) {
	err := &url.Error{
		Op:  "Put",
		URL: "https://s3.us-east-1.amazonaws.com/bucket/key?X-Amz-Signature=abcdef&X-Amz-Security-Token=session",
		Err: errors.New("connection refused"),
	}

	sanitized := sanitizeS3RequestError(err)
	assert.NotContains(t, sanitized.Error(), "abcdef")
	assert.NotContains(t, sanitized.Error(), "session")
	assert.Contains(t, sanitized.Error(), "https://s3.us-east-1.amazonaws.com/bucket/key")
}

func TestS3Description(t *testing.T) {
	backend, err := newS3BackendFromDSN("s3://key:secret@s3.us-east-1.amazonaws.com/bucket?prefix=images")
	require.NoError(t, err)

	desc := backend.Description()
	assert.Contains(t, desc, "bucket")
	assert.Contains(t, desc, "us-east-1")
	assert.Contains(t, desc, "images")
	// The credentials the DSN carries never reach the logs
	assert.NotContains(t, desc, "secret")
}
