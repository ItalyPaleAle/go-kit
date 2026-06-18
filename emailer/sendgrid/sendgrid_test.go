package sendgrid

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/italypaleale/go-kit/emailer/internal"
	"github.com/italypaleale/go-kit/testutils"
)

const sendgridEndpoint = "https://api.sendgrid.com/v3/mail/send"

func newTestEmailer() (*SendGridEmailer, *testutils.RoundTripperTest) {
	rtt := &testutils.RoundTripperTest{}
	e := &SendGridEmailer{
		apiKey:     "test-key",
		from:       SendGridEmail{Name: "Sender Name", Address: "sender@example.com"},
		httpClient: &http.Client{Transport: rtt},
	}
	return e, rtt
}

func httpResponse(t *testing.T, statusCode int, body string) *http.Response {
	t.Helper()

	r := &http.Response{
		StatusCode: statusCode,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
	t.Cleanup(func() {
		_ = r.Body.Close()
	})
	return r
}

func TestInit(t *testing.T) {
	t.Run("valid connection string", func(t *testing.T) {
		// The documented format carries the API key as the authority, with no userinfo
		connString, err := url.Parse("sendgrid://SG.AbCdEf_12-34?fromAddress=sender@example.com&fromName=Sender+Name")
		require.NoError(t, err)

		var e SendGridEmailer
		err = e.Init(t.Context(), internal.InitOpts{ConnString: connString})
		require.NoError(t, err)

		// The key must survive parsing with its original case intact
		assert.Equal(t, "SG.AbCdEf_12-34", e.apiKey)
		assert.Equal(t, "sender@example.com", e.from.Address)
		assert.Equal(t, "Sender Name", e.from.Name)
	})

	t.Run("fromName is optional", func(t *testing.T) {
		connString, err := url.Parse("sendgrid://SG.key?fromAddress=sender@example.com")
		require.NoError(t, err)

		var e SendGridEmailer
		err = e.Init(t.Context(), internal.InitOpts{ConnString: connString})
		require.NoError(t, err)
		assert.Equal(t, "SG.key", e.apiKey)
		assert.Empty(t, e.from.Name)
	})

	t.Run("wrong scheme is rejected", func(t *testing.T) {
		connString, err := url.Parse("smtp://SG.key?fromAddress=sender@example.com")
		require.NoError(t, err)

		var e SendGridEmailer
		err = e.Init(t.Context(), internal.InitOpts{ConnString: connString})
		require.ErrorContains(t, err, "invalid connection string scheme")
	})

	t.Run("missing API key is rejected", func(t *testing.T) {
		connString, err := url.Parse("sendgrid://?fromAddress=sender@example.com")
		require.NoError(t, err)

		var e SendGridEmailer
		err = e.Init(t.Context(), internal.InitOpts{ConnString: connString})
		require.ErrorContains(t, err, "missing SendGrid API key")
	})

	t.Run("missing from address is rejected", func(t *testing.T) {
		connString, err := url.Parse("sendgrid://SG.key")
		require.NoError(t, err)

		var e SendGridEmailer
		err = e.Init(t.Context(), internal.InitOpts{ConnString: connString})
		require.ErrorContains(t, err, "missing from address")
	})
}

func TestSendEmail(t *testing.T) {
	e, rtt := newTestEmailer()
	reqCh := make(chan *http.Request, 1)
	resCh := make(chan *http.Response, 1)
	rtt.SetReqCh(reqCh)
	rtt.SetResponsesCh(resCh)
	resCh <- httpResponse(t, http.StatusAccepted, "") //nolint:bodyclose

	// Send both text and HTML so the content-array ordering is exercised
	err := e.SendEmail(t.Context(), "recipient@example.com", "Hello", internal.SendEmailMessage{
		Text: "Plain body",
		HTML: "<p>HTML body</p>",
	})
	require.NoError(t, err)

	req := <-reqCh
	defer req.Body.Close()

	// The request must always target the production SendGrid endpoint
	assert.Equal(t, http.MethodPost, req.Method)
	assert.Equal(t, sendgridEndpoint, req.URL.String())
	assert.Equal(t, "Bearer test-key", req.Header.Get("Authorization"))
	assert.Equal(t, "application/json", req.Header.Get("Content-Type"))

	body, err := io.ReadAll(req.Body)
	require.NoError(t, err)

	// Assert the wire shape against the raw JSON keys, not our Go structs, so a json-tag regression is caught
	var payload map[string]any
	err = json.Unmarshal(body, &payload)
	require.NoError(t, err)

	// Recipients must be nested under personalizations[].to[], not at the top level
	personalizations, ok := payload["personalizations"].([]any)
	require.True(t, ok, "personalizations must be an array")
	require.Len(t, personalizations, 1)
	p0, ok := personalizations[0].(map[string]any)
	require.True(t, ok)
	to, ok := p0["to"].([]any)
	require.True(t, ok, "to must be an array")
	require.Len(t, to, 1)
	to0, ok := to[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "recipient@example.com", to0["email"])

	// From is an object with email plus an optional name
	from, ok := payload["from"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "sender@example.com", from["email"])
	assert.Equal(t, "Sender Name", from["name"])

	assert.Equal(t, "Hello", payload["subject"])

	// Bodies must live in content[], with text/plain ordered before text/html
	content, ok := payload["content"].([]any)
	require.True(t, ok, "content must be an array")
	require.Len(t, content, 2)
	c0, ok := content[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "text/plain", c0["type"])
	assert.Equal(t, "Plain body", c0["value"])
	c1, ok := content[1].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "text/html", c1["type"])
	assert.Equal(t, "<p>HTML body</p>", c1["value"])
}

func TestSendEmailOmitsHTMLWhenEmpty(t *testing.T) {
	e, rtt := newTestEmailer()
	reqCh := make(chan *http.Request, 1)
	resCh := make(chan *http.Response, 1)
	rtt.SetReqCh(reqCh)
	rtt.SetResponsesCh(resCh)
	resCh <- httpResponse(t, http.StatusAccepted, "") //nolint:bodyclose

	// An empty HTML body must not produce a text/html content entry
	err := e.SendEmail(t.Context(), "recipient@example.com", "Hello", internal.SendEmailMessage{Text: "Plain body"})
	require.NoError(t, err)

	req := <-reqCh
	defer req.Body.Close()
	body, err := io.ReadAll(req.Body)
	require.NoError(t, err)

	var payload map[string]any
	err = json.Unmarshal(body, &payload)
	require.NoError(t, err)

	content, ok := payload["content"].([]any)
	require.True(t, ok, "content must be an array")
	require.Len(t, content, 1, "content must contain only the text/plain part when HTML is empty")
	c0, ok := content[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "text/plain", c0["type"])
}

func TestSendEmailReturnsRemoteErrors(t *testing.T) {
	e, rtt := newTestEmailer()
	reqCh := make(chan *http.Request, 1)
	resCh := make(chan *http.Response, 1)
	rtt.SetReqCh(reqCh)
	rtt.SetResponsesCh(resCh)
	// Return a SendGrid-like rejection so the test can verify the status code and body are surfaced
	resCh <- httpResponse(t, http.StatusBadRequest, `{"errors":[{"message":"bad request"}]}`) //nolint:bodyclose

	err := e.SendEmail(t.Context(), "recipient@example.com", "Hello", internal.SendEmailMessage{Text: "Body"})
	require.Error(t, err)
	require.ErrorContains(t, err, "failed to send email (400):")
	require.ErrorContains(t, err, "bad request")
}
