package webhook

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type ntfyRequest struct {
	method string
	path   string
	header http.Header
	body   string
}

// Starts a server that records the requests it receives
func newNtfyTestServer(t *testing.T) (srv *httptest.Server, requests func() []ntfyRequest) {
	t.Helper()

	var (
		mu   sync.Mutex
		reqs []ntfyRequest
	)
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		reqs = append(reqs, ntfyRequest{
			method: r.Method,
			path:   r.URL.Path,
			header: r.Header.Clone(),
			body:   string(body),
		})
	}))
	t.Cleanup(srv.Close)

	return srv, func() []ntfyRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]ntfyRequest(nil), reqs...)
	}
}

func newNtfyTestClient(t *testing.T, url string, opts NewWebhookOpts) *webhookClient {
	t.Helper()

	opts.URL = url
	opts.Format = FormatNtfy
	opts.AllowPrivateIPs = true
	wh, _ := newTestWebhookClient(t, opts)
	return wh
}

// Returns the headers that are set explicitly, ignoring the ones added by the HTTP stack
func ntfyHeaders(h http.Header) map[string]string {
	res := map[string]string{}
	for _, k := range []string{"Title", "Priority", "Tags", "Click", "Icon", "Authorization", "Content-Type"} {
		v := h.Get(k)
		if v != "" {
			res[k] = v
		}
	}
	return res
}

func TestStringMessageNtfy(t *testing.T) {
	message, err := StringMessage("hello ntfy").GetNtfyMessage()
	require.NoError(t, err)
	require.Equal(t, NtfyMessage{Message: "hello ntfy"}, message)
}

func TestNewWebhookNtfyFormat(t *testing.T) {
	whAny, err := NewWebhook(NewWebhookOpts{URL: "https://ntfy.example.com/alerts", Format: FormatNtfy})
	require.NoError(t, err)

	wh := whAny.(*webhookClient) //nolint:forcetypeassert
	require.Equal(t, FormatNtfy, wh.format)
	require.Equal(t, "https://ntfy.example.com/alerts", wh.webhookURL)
}

func TestWebhookNtfy(t *testing.T) {
	t.Run("sends the message as the body and the fields as headers", func(t *testing.T) {
		srv, requests := newNtfyTestServer(t)
		wh := newNtfyTestClient(t, srv.URL+"/alerts", NewWebhookOpts{Key: "Bearer tk_abc123"})

		err := wh.SendWebhook(t.Context(), testMessageProvider{ntfyMessage: NtfyMessage{
			Message:  "Disk usage is at 95%",
			Title:    "Disk full on nas",
			Priority: NtfyPriorityHigh,
			Tags:     []string{"warning", "floppy_disk"},
			Click:    "https://status.example.com/nas",
			Icon:     "https://status.example.com/icon.png",
		}})
		require.NoError(t, err)

		reqs := requests()
		require.Len(t, reqs, 1)
		assert.Equal(t, http.MethodPost, reqs[0].method)
		assert.Equal(t, "/alerts", reqs[0].path)
		assert.Equal(t, "Disk usage is at 95%", reqs[0].body)
		assert.Equal(t, map[string]string{
			"Content-Type":  "text/plain; charset=utf-8",
			"Authorization": "Bearer tk_abc123",
			"Title":         "Disk full on nas",
			"Priority":      "4",
			"Tags":          "warning,floppy_disk",
			"Click":         "https://status.example.com/nas",
			"Icon":          "https://status.example.com/icon.png",
		}, ntfyHeaders(reqs[0].header))
	})

	t.Run("a message only sends the body", func(t *testing.T) {
		srv, requests := newNtfyTestServer(t)
		wh := newNtfyTestClient(t, srv.URL+"/alerts", NewWebhookOpts{})

		err := wh.SendWebhook(t.Context(), StringMessage("just text"))
		require.NoError(t, err)

		reqs := requests()
		require.Len(t, reqs, 1)
		assert.Equal(t, "just text", reqs[0].body)
		assert.Equal(t, map[string]string{"Content-Type": "text/plain; charset=utf-8"}, ntfyHeaders(reqs[0].header))
	})

	t.Run("custom authorization header", func(t *testing.T) {
		srv, requests := newNtfyTestServer(t)
		wh := newNtfyTestClient(t, srv.URL+"/alerts", NewWebhookOpts{Key: "Basic dGVzdDp0ZXN0", AuthorizationHeader: "X-Auth"})

		err := wh.SendWebhook(t.Context(), StringMessage("text"))
		require.NoError(t, err)

		reqs := requests()
		require.Len(t, reqs, 1)
		assert.Equal(t, "Basic dGVzdDp0ZXN0", reqs[0].header.Get("X-Auth"))
		assert.Empty(t, reqs[0].header.Get("Authorization"))
	})

	t.Run("non-ASCII values are sent as RFC 2047 encoded-words", func(t *testing.T) {
		srv, requests := newNtfyTestServer(t)
		wh := newNtfyTestClient(t, srv.URL+"/alerts", NewWebhookOpts{})

		title := "🚨 Café down: Ünïcode título"
		err := wh.SendWebhook(t.Context(), testMessageProvider{ntfyMessage: NtfyMessage{
			Message: "Le service est arrêté 🔥",
			Title:   title,
			Tags:    []string{"rotating_light", "café"},
			Click:   "https://status.example.com/café?q=ü",
		}})
		require.NoError(t, err)

		reqs := requests()
		require.Len(t, reqs, 1)
		h := reqs[0].header

		// The headers are plain ASCII, and decode to the original values as ntfy does
		for _, k := range []string{"Title", "Tags", "Click"} {
			for _, c := range []byte(h.Get(k)) {
				assert.Less(t, c, byte(0x80), "header %s is not ASCII", k)
			}
		}
		assert.Contains(t, h.Get("Title"), "=?UTF-8?q?")
		dec := new(mime.WordDecoder)
		decoded, err := dec.DecodeHeader(h.Get("Title"))
		require.NoError(t, err)
		assert.Equal(t, title, decoded)

		decoded, err = dec.DecodeHeader(h.Get("Tags"))
		require.NoError(t, err)
		assert.Equal(t, "rotating_light,café", decoded)

		assert.Equal(t, "https://status.example.com/caf%C3%A9?q=%C3%BC", h.Get("Click"))

		// The body is sent as-is in UTF-8
		assert.Equal(t, "Le service est arrêté 🔥", reqs[0].body)
	})

	t.Run("header injection attempts do not create headers", func(t *testing.T) {
		srv, requests := newNtfyTestServer(t)
		wh := newNtfyTestClient(t, srv.URL+"/alerts", NewWebhookOpts{})

		err := wh.SendWebhook(t.Context(), testMessageProvider{ntfyMessage: NtfyMessage{
			Message: "body\r\nX-Body-Header: ok",
			Title:   "title\r\nX-Evil: 1\nX-Evil2: 2\r",
			Tags:    []string{"tag1\r\nX-Evil3: 3", "\x00tag2\t"},
		}})
		require.NoError(t, err)

		reqs := requests()
		require.Len(t, reqs, 1)
		h := reqs[0].header
		assert.Empty(t, h.Get("X-Evil"))
		assert.Empty(t, h.Get("X-Evil2"))
		assert.Empty(t, h.Get("X-Evil3"))
		assert.Empty(t, h.Get("X-Body-Header"))
		assert.Equal(t, "title  X-Evil: 1 X-Evil2: 2", h.Get("Title"))
		assert.Equal(t, "tag1  X-Evil3: 3,tag2", h.Get("Tags"))
		assert.Equal(t, "body\r\nX-Body-Header: ok", reqs[0].body)
	})

	t.Run("invalid messages are rejected without sending a request", func(t *testing.T) {
		tests := []struct {
			name   string
			msg    NtfyMessage
			expect string
		}{
			{name: "priority too high", msg: NtfyMessage{Message: "x", Priority: 6}, expect: "invalid priority 6"},
			{name: "negative priority", msg: NtfyMessage{Message: "x", Priority: -1}, expect: "invalid priority -1"},
			{name: "tag with a comma", msg: NtfyMessage{Message: "x", Tags: []string{"a,b"}}, expect: "must not contain commas"},
			{name: "click with a line break", msg: NtfyMessage{Message: "x", Click: "https://example.com/\r\nX-Evil: 1"}, expect: "invalid click URL"},
			{name: "click without a scheme", msg: NtfyMessage{Message: "x", Click: "example.com/page"}, expect: "must include a scheme"},
			{name: "malformed icon", msg: NtfyMessage{Message: "x", Icon: "://bad"}, expect: "invalid icon URL"},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				srv, requests := newNtfyTestServer(t)
				wh := newNtfyTestClient(t, srv.URL+"/alerts", NewWebhookOpts{})

				err := wh.SendWebhook(t.Context(), testMessageProvider{ntfyMessage: tc.msg})
				require.Error(t, err)
				require.ErrorContains(t, err, "failed to create request")
				require.ErrorContains(t, err, tc.expect)
				assert.Empty(t, requests())
			})
		}
	})

	t.Run("all valid priorities are accepted", func(t *testing.T) {
		for _, p := range []NtfyPriority{NtfyPriorityMin, NtfyPriorityLow, NtfyPriorityNormal, NtfyPriorityHigh, NtfyPriorityMax} {
			srv, requests := newNtfyTestServer(t)
			wh := newNtfyTestClient(t, srv.URL+"/alerts", NewWebhookOpts{})

			err := wh.SendWebhook(t.Context(), testMessageProvider{ntfyMessage: NtfyMessage{Message: "x", Priority: p}})
			require.NoError(t, err)

			reqs := requests()
			require.Len(t, reqs, 1)
			assert.Equal(t, strconv.Itoa(int(p)), reqs[0].header.Get("Priority"))
		}
	})

	t.Run("empty tags are skipped", func(t *testing.T) {
		srv, requests := newNtfyTestServer(t)
		wh := newNtfyTestClient(t, srv.URL+"/alerts", NewWebhookOpts{})

		err := wh.SendWebhook(t.Context(), testMessageProvider{ntfyMessage: NtfyMessage{Message: "x", Tags: []string{"", "  ", "a", "b"}}})
		require.NoError(t, err)

		reqs := requests()
		require.Len(t, reqs, 1)
		assert.Equal(t, "a,b", reqs[0].header.Get("Tags"))
	})

	t.Run("errors from the provider are permanent", func(t *testing.T) {
		srv, requests := newNtfyTestServer(t)
		wh := newNtfyTestClient(t, srv.URL+"/alerts", NewWebhookOpts{})

		err := wh.SendWebhook(t.Context(), testMessageProvider{ntfyErr: errors.New("boom")})
		require.Error(t, err)
		require.ErrorContains(t, err, "error getting ntfy message")
		assert.Empty(t, requests())
	})

	t.Run("error responses are reported", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()
		wh := newNtfyTestClient(t, srv.URL+"/alerts", NewWebhookOpts{})

		err := wh.SendWebhook(t.Context(), StringMessage("x"))
		require.Error(t, err)
		require.ErrorContains(t, err, "invalid response status code: 403")
	})
}
