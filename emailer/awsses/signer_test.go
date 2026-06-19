package awsses

import (
	"encoding/hex"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSignRequest(t *testing.T) {
	// Freeze time so the generated scope and headers are stable enough for exact assertions
	fixedTime := time.Date(2026, time.May, 12, 14, 30, 0, 0, time.UTC)
	payload := []byte(`{"example":"payload"}`)
	req, err := http.NewRequest(http.MethodPost, "https://email.us-east-1.amazonaws.com/v2/email/outbound-emails", nil)
	require.NoError(t, err)

	// Populate just enough configuration for the signer to build a complete authorization header
	emailer := AWSSES{
		accessKeyID:     "access-key",
		secretAccessKey: "secret-key",
		region:          "us-east-1",
	}

	err = emailer.signRequest(req, payload, fixedTime)
	require.NoError(t, err)

	// Assert the signer populated the canonical headers and authorization metadata together
	assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
	assert.Equal(t, fixedTime.Format("20060102T150405Z"), req.Header.Get("X-Amz-Date"))
	assert.Equal(t, sha256Hex(payload), req.Header.Get("X-Amz-Content-Sha256"))
	assert.Contains(t, req.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=access-key/20260512/us-east-1/ses/aws4_request")
	assert.Contains(t, req.Header.Get("Authorization"), "SignedHeaders=content-type;host;x-amz-content-sha256;x-amz-date")
	assert.Contains(t, req.Header.Get("Authorization"), "Signature=")
	assert.NotEmpty(t, req.Header.Get("Host"))
}

func TestSignRequestGoldenSignature(t *testing.T) {
	// Known values computed independently for the frozen inputs below
	const (
		wantSigningKey = "4cb77752e027a0f4f3b5493cecf66b4c1aacc44bcbf4e0f8de3885644b9d2a09"
		wantSignature  = "199d0455b92f060236ce51185dd05bc2309338d85cc622bc2ec903732b11aa6d"
	)

	fixedTime := time.Date(2026, time.May, 12, 14, 30, 0, 0, time.UTC)
	payload := []byte(`{"example":"payload"}`)
	req, err := http.NewRequest(http.MethodPost, "https://email.us-east-1.amazonaws.com/v2/email/outbound-emails", nil)
	require.NoError(t, err)

	emailer := AWSSES{
		accessKeyID:     "access-key",
		secretAccessKey: "secret-key",
		region:          "us-east-1",
	}

	err = emailer.signRequest(req, payload, fixedTime)
	require.NoError(t, err)

	// The scoped signing key (date -> region -> service -> aws4_request HMAC chain) must match the reference
	signingKey := deriveSigningKey("secret-key", "20260512", "us-east-1", "ses")
	assert.Equal(t, wantSigningKey, hex.EncodeToString(signingKey))

	// The full Authorization header must carry exactly the reference signature
	wantAuth := "AWS4-HMAC-SHA256 Credential=access-key/20260512/us-east-1/ses/aws4_request, " +
		"SignedHeaders=content-type;host;x-amz-content-sha256;x-amz-date, " +
		"Signature=" + wantSignature
	assert.Equal(t, wantAuth, req.Header.Get("Authorization"))
}

func TestSignRequestIncludesSessionToken(t *testing.T) {
	// Session credentials must be signed too or AWS will reject the request
	fixedTime := time.Date(2026, time.May, 12, 14, 30, 0, 0, time.UTC)
	req, err := http.NewRequest(http.MethodPost, "https://email.us-east-1.amazonaws.com/v2/email/outbound-emails", nil)
	require.NoError(t, err)

	// Include a session token so the test covers the temporary-credential path
	emailer := AWSSES{
		accessKeyID:     "access-key",
		secretAccessKey: "secret-key",
		sessionToken:    "session-token",
		region:          "us-east-1",
	}

	err = emailer.signRequest(req, []byte(`{}`), fixedTime)
	require.NoError(t, err)

	// The token must be present both as a header and in the signed header list
	assert.Equal(t, "session-token", req.Header.Get("X-Amz-Security-Token"))
	assert.Contains(t, req.Header.Get("Authorization"), "SignedHeaders=content-type;host;x-amz-content-sha256;x-amz-date;x-amz-security-token")
}
