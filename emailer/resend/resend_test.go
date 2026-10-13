package resend

import (
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/italypaleale/go-kit/emailer/internal"
)

const testAPIKey = "re_AbCdEf123_secretKEY" //nolint:gosec

// capturedRequest is what the test server saw for one request
type capturedRequest struct {
	method string
	path   string
	auth   string
	ctype  string
	body   map[string]any
}

// newTestServer returns a server that records requests and replies with the given status and body
func newTestServer(t *testing.T, status int, respBody string, header http.Header) (*httptest.Server, chan capturedRequest) {
	t.Helper()

	reqCh := make(chan capturedRequest, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(raw, &parsed)
		reqCh <- capturedRequest{
			method: r.Method,
			path:   r.URL.Path,
			auth:   r.Header.Get("Authorization"),
			ctype:  r.Header.Get("Content-Type"),
			body:   parsed,
		}

		maps.Copy(w.Header(), header)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody))
	}))
	t.Cleanup(server.Close)
	return server, reqCh
}

func newTestEmailer(server *httptest.Server) *ResendEmailer {
	return &ResendEmailer{
		apiKey:     testAPIKey,
		from:       `"Sender Name" <sender@example.com>`,
		endpoint:   server.URL,
		httpClient: server.Client(),
	}
}

func TestInit(t *testing.T) {
	t.Run("valid connection string", func(t *testing.T) {
		connString, err := url.Parse("resend://" + testAPIKey + "?fromAddress=sender@example.com&fromName=Sender+Name")
		require.NoError(t, err)

		var e ResendEmailer
		err = e.Init(t.Context(), internal.InitOpts{ConnString: connString})
		require.NoError(t, err)

		// The key must survive parsing with its original case intact
		assert.Equal(t, testAPIKey, e.apiKey)
		assert.Equal(t, `"Sender Name" <sender@example.com>`, e.from)
		assert.Equal(t, "https://api.resend.com", e.endpoint)
	})

	t.Run("fromName is optional", func(t *testing.T) {
		connString, err := url.Parse("resend://" + testAPIKey + "?fromAddress=sender@example.com")
		require.NoError(t, err)

		var e ResendEmailer
		err = e.Init(t.Context(), internal.InitOpts{ConnString: connString})
		require.NoError(t, err)
		assert.Equal(t, "sender@example.com", e.from)
	})

	invalid := []struct {
		name    string
		conn    string
		wantErr string
	}{
		{name: "wrong scheme", conn: "sendgrid://" + testAPIKey + "?fromAddress=sender@example.com", wantErr: "invalid connection string scheme"},
		{name: "missing API key", conn: "resend://?fromAddress=sender@example.com", wantErr: "missing Resend API key"},
		{name: "missing from address", conn: "resend://" + testAPIKey, wantErr: "missing from address"},
		{name: "invalid from address", conn: "resend://" + testAPIKey + "?fromAddress=not-an-address", wantErr: "invalid from address"},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			connString, err := url.Parse(tt.conn)
			require.NoError(t, err)

			var e ResendEmailer
			err = e.Init(t.Context(), internal.InitOpts{ConnString: connString})
			require.ErrorContains(t, err, tt.wantErr)
			assert.NotContains(t, err.Error(), testAPIKey)
		})
	}
}

func TestSendEmail(t *testing.T) {
	tests := []struct {
		name    string
		to      internal.EmailAddress
		message internal.SendEmailMessage
		want    map[string]any
	}{
		{
			name:    "text only",
			to:      internal.EmailAddress{Address: "recipient@example.com"},
			message: internal.SendEmailMessage{Text: "Plain body"},
			want: map[string]any{
				"from":    `"Sender Name" <sender@example.com>`,
				"to":      []any{"recipient@example.com"},
				"subject": "Hello",
				"text":    "Plain body",
			},
		},
		{
			name:    "html only",
			to:      internal.EmailAddress{Address: "recipient@example.com"},
			message: internal.SendEmailMessage{HTML: "<p>HTML body</p>"},
			want: map[string]any{
				"from":    `"Sender Name" <sender@example.com>`,
				"to":      []any{"recipient@example.com"},
				"subject": "Hello",
				"html":    "<p>HTML body</p>",
			},
		},
		{
			name:    "text and html with recipient name",
			to:      internal.EmailAddress{Name: "Recipient Name", Address: "recipient@example.com"},
			message: internal.SendEmailMessage{Text: "Plain body", HTML: "<p>HTML body</p>"},
			want: map[string]any{
				"from":    `"Sender Name" <sender@example.com>`,
				"to":      []any{`"Recipient Name" <recipient@example.com>`},
				"subject": "Hello",
				"text":    "Plain body",
				"html":    "<p>HTML body</p>",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, reqCh := newTestServer(t, http.StatusOK, `{"id":"49a3999c-0ce1-4ea6-ab68-afcd6dc2e794"}`, nil)
			e := newTestEmailer(server)

			err := e.SendEmail(t.Context(), tt.to, "Hello", tt.message)
			require.NoError(t, err)

			req := <-reqCh
			assert.Equal(t, http.MethodPost, req.method)
			assert.Equal(t, "/emails", req.path)
			assert.Equal(t, "Bearer "+testAPIKey, req.auth)
			assert.Equal(t, "application/json", req.ctype)

			// Compare against raw JSON keys so a json-tag regression is caught
			assert.Equal(t, tt.want, req.body)
		})
	}
}

func TestSendEmailSeveralRecipients(t *testing.T) {
	server, reqCh := newTestServer(t, http.StatusOK, `{"id":"abc"}`, nil)
	e := newTestEmailer(server)

	// The Emailer interface takes one recipient per call, so callers with several recipients call SendEmail for each of them
	for _, addr := range []string{"one@example.com", "two@example.com"} {
		err := e.SendEmail(t.Context(), internal.EmailAddress{Address: addr}, "Hello", internal.SendEmailMessage{Text: "Body"})
		require.NoError(t, err)
	}

	assert.Equal(t, []any{"one@example.com"}, (<-reqCh).body["to"])
	assert.Equal(t, []any{"two@example.com"}, (<-reqCh).body["to"])
}

func TestSendEmailInvalidRecipient(t *testing.T) {
	server, reqCh := newTestServer(t, http.StatusOK, `{"id":"abc"}`, nil)
	e := newTestEmailer(server)

	err := e.SendEmail(t.Context(), internal.EmailAddress{Address: "nope"}, "Hello", internal.SendEmailMessage{Text: "Body"})
	require.ErrorContains(t, err, "invalid recipient address")
	assert.Empty(t, reqCh)
}

func TestSendEmailAPIErrors(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		header   http.Header
		contains []string
	}{
		{
			name:     "400 with error JSON",
			status:   http.StatusBadRequest,
			body:     `{"name":"validation_error","message":"Invalid ` + "`from`" + ` field","statusCode":400}`,
			contains: []string{"(400)", "validation_error", "Invalid `from` field"},
		},
		{
			name:     "401 with error JSON",
			status:   http.StatusUnauthorized,
			body:     `{"name":"missing_api_key","message":"Missing API key in the authorization header.","statusCode":401}`,
			contains: []string{"(401)", "missing_api_key", "Missing API key"},
		},
		{
			name:     "429 with error JSON",
			status:   http.StatusTooManyRequests,
			body:     `{"name":"rate_limit_exceeded","message":"Too many requests.","statusCode":429}`,
			contains: []string{"(429)", "rate_limit_exceeded", "Too many requests"},
		},
		{
			name:     "429 without body uses Retry-After",
			status:   http.StatusTooManyRequests,
			body:     ``,
			header:   http.Header{"Retry-After": []string{"3"}},
			contains: []string{"(429)", "retry after 3 seconds"},
		},
		{
			name:     "500 with non-JSON body",
			status:   http.StatusInternalServerError,
			body:     `upstream exploded`,
			contains: []string{"(500)", "upstream exploded"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, _ := newTestServer(t, tt.status, tt.body, tt.header)
			e := newTestEmailer(server)

			err := e.SendEmail(t.Context(), internal.EmailAddress{Address: "recipient@example.com"}, "Hello", internal.SendEmailMessage{Text: "Body"})
			require.Error(t, err)
			for _, s := range tt.contains {
				require.ErrorContains(t, err, s)
			}
			assert.NotContains(t, err.Error(), testAPIKey)
		})
	}
}

func TestSendEmailRedactsKeyEchoedByServer(t *testing.T) {
	server, _ := newTestServer(t, http.StatusUnauthorized, `{"name":"invalid_api_key","message":"API key `+testAPIKey+` is invalid"}`, nil)
	e := newTestEmailer(server)

	err := e.SendEmail(t.Context(), internal.EmailAddress{Address: "recipient@example.com"}, "Hello", internal.SendEmailMessage{Text: "Body"})
	require.ErrorContains(t, err, "[redacted]")
	assert.NotContains(t, err.Error(), testAPIKey)
}

func TestSendEmailDoesNotFollowRedirects(t *testing.T) {
	// The target would receive the Authorization header if the redirect were followed
	targetHit := make(chan struct{}, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHit <- struct{}{}
	}))
	t.Cleanup(target.Close)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(server.Close)

	// Use the production default client with only the endpoint overridden
	e := &ResendEmailer{apiKey: testAPIKey, from: "sender@example.com", endpoint: server.URL}

	err := e.SendEmail(t.Context(), internal.EmailAddress{Address: "recipient@example.com"}, "Hello", internal.SendEmailMessage{Text: "Body"})
	require.ErrorContains(t, err, "(307)")
	assert.Empty(t, targetHit)
}

func TestSendEmailCanceledContext(t *testing.T) {
	server, reqCh := newTestServer(t, http.StatusOK, `{"id":"abc"}`, nil)
	e := newTestEmailer(server)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := e.SendEmail(ctx, internal.EmailAddress{Address: "recipient@example.com"}, "Hello", internal.SendEmailMessage{Text: "Body"})
	require.ErrorIs(t, err, context.Canceled)
	assert.NotContains(t, err.Error(), testAPIKey)
	assert.Empty(t, reqCh)
}

func TestSendEmailContextDeadline(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	e := newTestEmailer(server)

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	err := e.SendEmail(ctx, internal.EmailAddress{Address: "recipient@example.com"}, "Hello", internal.SendEmailMessage{Text: "Body"})
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
