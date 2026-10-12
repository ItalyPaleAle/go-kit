package objstore

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// How long the presigned URLs this backend signs for its own requests stay valid
// They never leave the process, so they only need to outlive a single request
const inProcessPresignTTL = 30 * time.Second

type s3Config struct {
	Bucket    string
	Region    string
	Endpoint  string
	KeyPrefix string
	PublicURL string
	AccessKey string
	SecretKey string
	Session   string
}

// S3Backend stores objects in S3, or in any S3-compatible object store
//
// Requests are signed with SigV4 in the query string rather than in a header, which keeps the whole request to a presigned URL and an HTTP call, with no SDK to pull in
type S3Backend struct {
	cfg        s3Config
	httpClient *http.Client
}

func newS3BackendFromDSN(dsn string) (*S3Backend, error) {
	// Credentials, bucket, region and the optional key prefix all come from the single storage DSN
	cfg, err := parseS3DSN(dsn)
	if err != nil {
		return nil, err
	}

	return &S3Backend{
		cfg:        cfg,
		httpClient: http.DefaultClient,
	}, nil
}

func parseS3DSN(dsn string) (s3Config, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return s3Config{}, fmt.Errorf("failed to parse the S3 storage DSN: %w", err)
	}
	if u.User == nil {
		return s3Config{}, errors.New("the S3 storage DSN must include an access key and a secret key")
	}

	secretKey, hasSecret := u.User.Password()
	if !hasSecret || secretKey == "" {
		return s3Config{}, errors.New("the S3 storage DSN must include a secret key")
	}
	if u.User.Username() == "" {
		return s3Config{}, errors.New("the S3 storage DSN must include an access key")
	}

	bucket := strings.Trim(strings.TrimPrefix(u.Path, "/"), "/")
	if bucket == "" {
		return s3Config{}, errors.New("the S3 storage DSN must include a bucket in its path")
	}

	// The region is part of the signature, so it's either explicit or derived from an AWS-style endpoint
	query := u.Query()
	region := query.Get("region")
	if region == "" {
		region = regionFromS3Host(u.Hostname())
	}
	if region == "" {
		return s3Config{}, errors.New("the S3 storage DSN must include a region")
	}

	// The public URL is the base objects are served from when the bucket is fronted by a CDN, and it's validated here rather than at the first write
	publicURL := strings.TrimRight(query.Get("publicUrl"), "/")
	if publicURL != "" {
		parsed, err := url.Parse(publicURL)
		if err != nil {
			return s3Config{}, fmt.Errorf("failed to parse the 'publicUrl' option of the S3 storage DSN: %w", err)
		}
		if !parsed.IsAbs() || parsed.Scheme != "https" || parsed.Host == "" {
			return s3Config{}, errors.New("the 'publicUrl' option of the S3 storage DSN must be an absolute HTTPS URL")
		}
	}

	return s3Config{
		Bucket:    bucket,
		Region:    region,
		Endpoint:  endpointFromURL(u),
		KeyPrefix: strings.Trim(query.Get("prefix"), "/"),
		PublicURL: publicURL,
		AccessKey: u.User.Username(),
		SecretKey: secretKey,
		Session:   query.Get("sessionToken"),
	}, nil
}

// Description returns the bucket and region objects are stored in, without the credentials the DSN also carries
func (s *S3Backend) Description() string {
	desc := "S3, in bucket '" + s.cfg.Bucket + "' (region '" + s.cfg.Region + "')"
	if s.cfg.KeyPrefix != "" {
		desc += " under prefix '" + s.cfg.KeyPrefix + "'"
	}
	return desc
}

// Put uploads an object with a single PUT request
func (s *S3Backend) Put(ctx context.Context, key string, body io.Reader, maxSize int64, contentType string) (PutResult, error) {
	err := ValidateKey(key)
	if err != nil {
		return PutResult{}, err
	}

	// The body is spooled to a temporary file so the request can declare a Content-Length without the whole object staying in memory
	file, size, err := spoolBounded(body, maxSize)
	if err != nil {
		return PutResult{}, err
	}
	defer closeAndRemove(file)

	endpoint, err := s.presignedURL(http.MethodPut, key, contentType, inProcessPresignTTL)
	if err != nil {
		return PutResult{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, file)
	if err != nil {
		return PutResult{}, fmt.Errorf("failed to create the S3 put request: %w", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.ContentLength = size

	res, err := s.httpClient.Do(req)
	if err != nil {
		return PutResult{}, fmt.Errorf("failed to put the S3 object: %w", sanitizeS3RequestError(err))
	}
	defer res.Body.Close() //nolint:errcheck

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return PutResult{}, s3ResponseError("put", res)
	}

	result := PutResult{Size: size}
	if s.cfg.PublicURL != "" {
		result.URL, err = s.publicObjectURL(key)
		if err != nil {
			return PutResult{}, err
		}
	}

	return result, nil
}

// Get downloads an object with a single GET request
func (s *S3Backend) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	err := ValidateKey(key)
	if err != nil {
		return nil, ObjectInfo{}, err
	}

	endpoint, err := s.presignedURL(http.MethodGet, key, "", inProcessPresignTTL)
	if err != nil {
		return nil, ObjectInfo{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, ObjectInfo{}, fmt.Errorf("failed to create the S3 get request: %w", err)
	}

	res, err := s.httpClient.Do(req)
	if err != nil {
		return nil, ObjectInfo{}, fmt.Errorf("failed to get the S3 object: %w", sanitizeS3RequestError(err))
	}

	switch {
	case res.StatusCode == http.StatusNotFound:
		res.Body.Close() //nolint:errcheck,gosec
		return nil, ObjectInfo{}, ErrNotFound
	case res.StatusCode < 200 || res.StatusCode >= 300:
		defer res.Body.Close() //nolint:errcheck
		return nil, ObjectInfo{}, s3ResponseError("get", res)
	}

	// The caller owns the body from here on, since it's the object's content
	return res.Body, ObjectInfo{Size: res.ContentLength, ContentType: res.Header.Get("Content-Type")}, nil
}

// GetConsistent downloads an object with a single GET request
// The request goes straight to the bucket, which is read-after-write consistent, so there is no cache for this to bypass
// A bucket fronted by a CDN is only read through it by the browsers the public URL is handed to, never by this process
func (s *S3Backend) GetConsistent(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	return s.Get(ctx, key)
}

// Exists reports whether an object is stored at a key, with a HEAD request that retrieves no content
func (s *S3Backend) Exists(ctx context.Context, key string) (bool, error) {
	err := ValidateKey(key)
	if err != nil {
		return false, err
	}

	endpoint, err := s.presignedURL(http.MethodHead, key, "", inProcessPresignTTL)
	if err != nil {
		return false, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, endpoint, nil)
	if err != nil {
		return false, fmt.Errorf("failed to create the S3 head request: %w", err)
	}

	res, err := s.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("failed to look up the S3 object: %w", sanitizeS3RequestError(err))
	}
	defer res.Body.Close() //nolint:errcheck

	switch {
	case res.StatusCode == http.StatusNotFound:
		return false, nil
	case res.StatusCode < 200 || res.StatusCode >= 300:
		return false, s3ResponseError("head", res)
	}

	return true, nil
}

// Delete removes an object
// S3 deletes are idempotent, so a key that doesn't exist isn't an error
func (s *S3Backend) Delete(ctx context.Context, key string) error {
	err := ValidateKey(key)
	if err != nil {
		return err
	}

	endpoint, err := s.presignedURL(http.MethodDelete, key, "", inProcessPresignTTL)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return fmt.Errorf("failed to create the S3 delete request: %w", err)
	}

	res, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to delete the S3 object: %w", sanitizeS3RequestError(err))
	}
	defer res.Body.Close() //nolint:errcheck

	if res.StatusCode != http.StatusNotFound && (res.StatusCode < 200 || res.StatusCode >= 300) {
		return s3ResponseError("delete", res)
	}

	return nil
}

// SupportsDirectUpload reports that a client can be handed a presigned URL to upload an object with
func (s *S3Backend) SupportsDirectUpload() bool {
	return true
}

// CreateDirectUpload returns a presigned PUT scoped to one exact object key
func (s *S3Backend) CreateDirectUpload(_ context.Context, key string, _ int64, contentType string, expiresIn time.Duration) (DirectUpload, error) {
	err := ValidateKey(key)
	if err != nil {
		return DirectUpload{}, err
	}

	endpoint, err := s.presignedURL(http.MethodPut, key, contentType, expiresIn)
	if err != nil {
		return DirectUpload{}, err
	}

	return DirectUpload{
		Key:     key,
		URL:     endpoint,
		Method:  http.MethodPut,
		Headers: map[string]string{"Content-Type": contentType},
	}, nil
}

// HasPublicAddressing reports whether the DSN configured a base URL objects are served from
func (s *S3Backend) HasPublicAddressing() bool {
	return s.cfg.PublicURL != ""
}

// PublicURL returns the stable CDN URL configured for an object
func (s *S3Backend) PublicURL(key string) (string, error) {
	if !s.HasPublicAddressing() {
		return "", nil
	}
	err := ValidateKey(key)
	if err != nil {
		return "", err
	}

	return s.publicObjectURL(key)
}

// Build the URL an object is served from when the bucket is fronted by a CDN
func (s *S3Backend) publicObjectURL(key string) (string, error) {
	publicURL, err := url.Parse(s.cfg.PublicURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse the configured S3 public URL: %w", err)
	}

	publicURL.Path = path.Join(publicURL.Path, prefixedKey(s.cfg.KeyPrefix, key))

	return publicURL.String(), nil
}

// Sign one request by moving the SigV4 authentication into the query string, which keeps every request to a plain HTTP call
// The same signature is what a client uses for a direct upload, which is why the lifetime is a parameter rather than a constant
func (s *S3Backend) presignedURL(method string, key string, contentType string, expiresIn time.Duration) (string, error) {
	now := time.Now().UTC()
	endpoint, err := s.objectURL(key)
	if err != nil {
		return "", err
	}

	credentialDate := now.Format("20060102")
	credentialScope := credentialDate + "/" + s.cfg.Region + "/s3/aws4_request"

	signedHeaders := []string{"host"}
	if contentType != "" {
		signedHeaders = append(signedHeaders, "content-type")
	}
	sort.Strings(signedHeaders)

	query := endpoint.Query()
	query.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	query.Set("X-Amz-Credential", s.cfg.AccessKey+"/"+credentialScope)
	query.Set("X-Amz-Date", now.Format("20060102T150405Z"))
	query.Set("X-Amz-Expires", strconv.Itoa(int(expiresIn.Seconds())))
	query.Set("X-Amz-SignedHeaders", strings.Join(signedHeaders, ";"))
	if s.cfg.Session != "" {
		query.Set("X-Amz-Security-Token", s.cfg.Session)
	}
	endpoint.RawQuery = query.Encode()

	canonicalRequest := strings.Join([]string{
		method,
		endpoint.EscapedPath(),
		canonicalQuery(endpoint.Query()),
		canonicalHeaders(signedHeaders, endpoint.Host, contentType),
		strings.Join(signedHeaders, ";"),
		"UNSIGNED-PAYLOAD",
	}, "\n")

	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		now.Format("20060102T150405Z"),
		credentialScope,
		hex.EncodeToString(sha256Bytes([]byte(canonicalRequest))),
	}, "\n")

	signature := hex.EncodeToString(hmacBytes(s.signingKey(credentialDate), []byte(stringToSign)))

	query = endpoint.Query()
	query.Set("X-Amz-Signature", signature)
	endpoint.RawQuery = query.Encode()

	return endpoint.String(), nil
}

// Build the URL of an object, using the configured endpoint when the DSN pointed at an S3-compatible store
func (s *S3Backend) objectURL(key string) (*url.URL, error) {
	key = prefixedKey(s.cfg.KeyPrefix, key)

	if s.cfg.Endpoint != "" {
		endpoint, err := url.Parse(strings.TrimRight(s.cfg.Endpoint, "/"))
		if err != nil {
			return nil, fmt.Errorf("failed to parse the S3 endpoint: %w", err)
		}

		endpoint.Path = path.Join(endpoint.Path, s.cfg.Bucket, key)
		return endpoint, nil
	}

	return &url.URL{
		Scheme: "https",
		Host:   s.cfg.Bucket + ".s3." + s.cfg.Region + ".amazonaws.com",
		Path:   "/" + strings.TrimLeft(key, "/"),
	}, nil
}

func (s *S3Backend) signingKey(date string) []byte {
	dateKey := hmacBytes([]byte("AWS4"+s.cfg.SecretKey), []byte(date))
	regionKey := hmacBytes(dateKey, []byte(s.cfg.Region))
	serviceKey := hmacBytes(regionKey, []byte("s3"))
	return hmacBytes(serviceKey, []byte("aws4_request"))
}

func canonicalHeaders(signedHeaders []string, host string, contentType string) string {
	var b strings.Builder
	for _, header := range signedHeaders {
		switch header {
		case "content-type":
			b.WriteString("content-type:")
			b.WriteString(strings.TrimSpace(contentType))
			b.WriteByte('\n')
		case "host":
			b.WriteString("host:")
			b.WriteString(host)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func canonicalQuery(values url.Values) string {
	parts := make([]string, 0, len(values))
	for key, vals := range values {
		sort.Strings(vals)
		for _, value := range vals {
			parts = append(parts, url.QueryEscape(key)+"="+url.QueryEscape(value))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, "&")
}

func regionFromS3Host(host string) string {
	parts := strings.Split(host, ".")
	if len(parts) >= 4 && parts[0] == "s3" && parts[2] == "amazonaws" {
		return parts[1]
	}

	return ""
}

func endpointFromURL(u *url.URL) string {
	if u.Host == "" {
		return ""
	}

	return "https://" + u.Host
}

// Build the error for a response S3 rejected, including the beginning of the body since that's where S3 puts the reason
func s3ResponseError(operation string, res *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	return fmt.Errorf("S3 %s object returned %s: %s", operation, res.Status, strings.TrimSpace(string(body)))
}

// Strip the query string off a failed request's URL, since it's where the signature and the session token are
func sanitizeS3RequestError(err error) error {
	var urlErr *url.Error
	ok := errors.As(err, &urlErr)
	if !ok {
		return err
	}

	parsed, parseErr := url.Parse(urlErr.URL)
	if parseErr != nil {
		return urlErr.Err
	}
	parsed.RawQuery = ""

	return &url.Error{Op: urlErr.Op, URL: parsed.String(), Err: urlErr.Err}
}

func sha256Bytes(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}

func hmacBytes(key []byte, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(data)
	return mac.Sum(nil)
}
