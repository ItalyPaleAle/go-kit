package awsses

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// signRequest adds the SigV4 headers required for the SES SendEmail REST API
func (a AWSSES) signRequest(req *http.Request, payload []byte, requestTime time.Time) error {
	// Refuse to sign requests that were built from an incomplete AWSSES instance
	if a.accessKeyID == "" || a.secretAccessKey == "" || a.region == "" {
		return errors.New("AWS SES client is not initialized")
	}

	// Derive the timestamp and payload digest once because both are reused across the signature inputs
	requestTime = requestTime.UTC()
	amzDate := requestTime.Format("20060102T150405Z")
	dateStamp := requestTime.Format("20060102")
	payloadHash := sha256Hex(payload)

	// These headers become part of the canonical request that SES verifies server-side
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Host", req.URL.Host)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	req.Header.Set("X-Amz-Date", amzDate)
	if a.sessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", a.sessionToken)
	}

	// Canonicalize the full request exactly as AWS expects before deriving the signature
	canonicalHeaders, signedHeaders := canonicalHeaders(req)
	credentialScope := dateStamp + "/" + a.region + "/ses/aws4_request"
	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI(req.URL),
		canonicalQueryString(req.URL),
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")
	signature := hex.EncodeToString(hmacSHA256(deriveSigningKey(a.secretAccessKey, dateStamp, a.region, "ses"), stringToSign))

	// Send the final SigV4 authorization header after every signed component is fixed in place
	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		a.accessKeyID,
		credentialScope,
		signedHeaders,
		signature,
	))

	return nil
}

// canonicalHeaders returns the normalized header block and signed header list used by SigV4
func canonicalHeaders(req *http.Request) (string, string) {
	// Ignore Authorization because it is the output of the signing process, not an input
	headers := map[string]string{}
	for name, values := range req.Header {
		lowerName := strings.ToLower(name)
		if lowerName == "authorization" {
			continue
		}
		headers[lowerName] = normalizeHeaderValue(strings.Join(values, ","))
	}

	// Ensure host is always signed even if the caller did not populate the header map explicitly
	_, hasHost := headers["host"]
	if !hasHost {
		headers["host"] = req.URL.Host
	}

	// Sort header names so the canonical form is stable across runs and platforms
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)

	// Build both canonical outputs together so they cannot drift apart
	var canonical strings.Builder
	for _, name := range names {
		canonical.WriteString(name)
		canonical.WriteByte(':')
		canonical.WriteString(headers[name])
		canonical.WriteByte('\n')
	}

	return canonical.String(), strings.Join(names, ";")
}

// canonicalURI returns the escaped request path in the form expected by SigV4
func canonicalURI(u *url.URL) string {
	path := u.EscapedPath()
	if path == "" {
		return "/"
	}
	return path
}

// canonicalQueryString sorts and escapes query parameters using AWS canonicalization rules
func canonicalQueryString(u *url.URL) string {
	// Most SES calls do not use a query string, so return early for the common case
	if u.RawQuery == "" {
		return ""
	}

	// Fall back to an empty canonical query string if the URL is already malformed
	queryValues, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return ""
	}

	// Sort both keys and values so repeated parameters produce deterministic signatures
	parts := make([]string, 0, len(queryValues))
	keys := make([]string, 0, len(queryValues))
	for key := range queryValues {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		values := append([]string(nil), queryValues[key]...)
		sort.Strings(values)
		encodedKey := awsURLEscape(key)
		if len(values) == 0 {
			parts = append(parts, encodedKey+"=")
			continue
		}
		for _, value := range values {
			parts = append(parts, encodedKey+"="+awsURLEscape(value))
		}
	}

	return strings.Join(parts, "&")
}

// awsURLEscape applies the byte-level escaping rules required by AWS SigV4
func awsURLEscape(value string) string {
	// Go's standard query escaping does not match AWS's byte-for-byte uppercase hex rules
	var out strings.Builder
	out.Grow(int(float64(len(value)) * 1.2))
	for i := range len(value) {
		b := value[i]
		if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '-' || b == '.' || b == '_' || b == '~' {
			out.WriteByte(b)
			continue
		}
		fmt.Fprintf(&out, "%%%02X", b)
	}
	return out.String()
}

// normalizeHeaderValue collapses internal whitespace so header values match AWS canonical form
func normalizeHeaderValue(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

// sha256Hex returns a lowercase hexadecimal SHA-256 digest
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// hmacSHA256 computes an HMAC-SHA256 digest for the provided data
func hmacSHA256(key []byte, data string) []byte {
	// SigV4 builds every derived key and final signature from the same HMAC primitive
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(data))
	return h.Sum(nil)
}

// deriveSigningKey builds the scoped SigV4 signing key for the SES service
func deriveSigningKey(secretAccessKey string, dateStamp string, region string, service string) []byte {
	// Scope the key by date, region, and service so a signature is only valid for this SES request class
	dateKey := hmacSHA256([]byte("AWS4"+secretAccessKey), dateStamp)
	regionKey := hmacSHA256(dateKey, region)
	serviceKey := hmacSHA256(regionKey, service)
	return hmacSHA256(serviceKey, "aws4_request")
}
