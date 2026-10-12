package objstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/italypaleale/go-kit/objstore/verceloidc"
)

const (
	// Control-plane endpoint every write goes through
	vercelBlobAPIEndpoint = "https://vercel.com/api/blob"
	// Version of the Blob API this client speaks, sent on every request
	vercelBlobAPIVersion = "12"
	// Environment variable a read-write token is read from when the DSN doesn't name another one
	vercelBlobTokenEnvironment = "BLOB_READ_WRITE_TOKEN" // #nosec G101 -- This is the name of an environment variable, not a token
	// Access levels a Blob store can be configured with, which decide both the write request and the domain reads go to
	accessPrivate = "private"
	accessPublic  = "public"
	// Authentication modes the DSN can select
	authToken = "token"
	authOIDC  = "oidc"
	// Query parameter that sends a read to origin storage instead of the CDN cache, which Blob honors for a private store only
	vercelBlobCacheParam = "cache"
	// Code the Blob API reports for an object that isn't in the store
	vercelBlobErrorNotFound = "not_found"
	// How much of a rejected response's body is read, which is where the reason for the rejection is
	maxErrorBodySize = 4096
)

type vercelBlobConfig struct {
	Auth         string
	Token        string
	TokenEnv     string
	OIDCTokenEnv string
	StoreID      string
	StoreIDEnv   string
	Access       string
	KeyPrefix    string
}

// VercelBlobBackend stores objects in Vercel Blob
//
// Writes go through the Blob control-plane API and reads go straight to the store's own domain, which is the same split the official TypeScript SDK uses
type VercelBlobBackend struct {
	storeID string
	access  string
	prefix  string
	// Control-plane endpoint writes go through, which is a field only so the tests can point it at a server of their own
	apiEndpoint string
	// Read-write token, when the DSN configured one
	// It's kept because it's the key a browser upload token is signed with, which OIDC authentication can't do
	readWriteToken string
	// Resolves the bearer token for one operation, since an OIDC token is short-lived and must not be captured once at startup
	tokenFn    func(ctx context.Context) (string, error)
	httpClient *http.Client
}

func newVercelBlobBackendFromDSN(dsn string, defaultStoreIDEnv string) (*VercelBlobBackend, error) {
	cfg, err := parseVercelBlobDSN(dsn)
	if err != nil {
		return nil, err
	}

	// The store ID has no default in the DSN itself, because a process that talks to more than one store needs a different variable for each
	if cfg.Auth == authOIDC && cfg.StoreID == "" && cfg.StoreIDEnv == "" {
		cfg.StoreIDEnv = defaultStoreIDEnv
	}

	return newVercelBlobBackend(cfg)
}

func parseVercelBlobDSN(dsn string) (vercelBlobConfig, error) {
	// The DSN carries no host, so it's normalized into one url.Parse can read the query string of
	withoutScheme := strings.TrimPrefix(dsn, SchemeVercelBlob+":")
	withoutScheme = strings.TrimPrefix(withoutScheme, "//")
	if withoutScheme == "" {
		withoutScheme = "?tokenEnv=" + vercelBlobTokenEnvironment
	}

	u, err := url.Parse(SchemeVercelBlob + "://" + withoutScheme)
	if err != nil {
		return vercelBlobConfig{}, fmt.Errorf("failed to parse the Vercel Blob storage DSN: %w", err)
	}

	query := u.Query()
	auth := strings.ToLower(query.Get("auth"))
	switch auth {
	case "", authToken:
		auth = authToken
	case authOIDC:
		// OIDC resolves a request-scoped bearer token and needs an explicit store ID, since the token doesn't carry one
	default:
		return vercelBlobConfig{}, errors.New("the 'auth' option of the Vercel Blob storage DSN must be 'token' or 'oidc'")
	}

	access := query.Get("access")
	switch access {
	case "":
		access = accessPrivate
	case accessPrivate, accessPublic:
		// The access level is part of both the write request and the domain reads go to, so it's kept as it is
	default:
		return vercelBlobConfig{}, errors.New("the 'access' option of the Vercel Blob storage DSN must be 'private' or 'public'")
	}

	token := query.Get("token")
	if token == "" && u.User != nil {
		token = u.User.Username()
	}
	tokenEnv := query.Get("tokenEnv")
	if tokenEnv == "" && auth == authToken {
		tokenEnv = vercelBlobTokenEnvironment
	}

	storeID := query.Get("storeId")
	storeIDEnv := query.Get("storeIdEnv")
	oidcTokenEnv := query.Get("oidcTokenEnv")

	// Mixing the two credential modes is rejected, so a stale token left in the configuration can't silently take over from OIDC
	if auth == authOIDC {
		mixed := token != "" || query.Has("tokenEnv") || u.User != nil
		if mixed {
			return vercelBlobConfig{}, errors.New("the Vercel Blob storage DSN must not set 'token' or 'tokenEnv' when using OIDC authentication")
		}
	} else {
		mixed := storeID != "" || query.Has("storeIdEnv") || query.Has("oidcTokenEnv")
		if mixed {
			return vercelBlobConfig{}, errors.New("the Vercel Blob storage DSN must not set the OIDC store options when using token authentication")
		}
	}

	return vercelBlobConfig{
		Auth:         auth,
		Token:        token,
		TokenEnv:     tokenEnv,
		OIDCTokenEnv: oidcTokenEnv,
		StoreID:      storeID,
		StoreIDEnv:   storeIDEnv,
		Access:       access,
		KeyPrefix:    strings.Trim(query.Get("prefix"), "/"),
	}, nil
}

func newVercelBlobBackend(cfg vercelBlobConfig) (*VercelBlobBackend, error) {
	backend := &VercelBlobBackend{
		access:      cfg.Access,
		prefix:      cfg.KeyPrefix,
		apiEndpoint: vercelBlobAPIEndpoint,
		httpClient:  http.DefaultClient,
	}

	// OIDC tokens expire, so the token is looked up on every operation rather than captured here
	if cfg.Auth == authOIDC {
		storeID := cfg.StoreID
		if storeID == "" && cfg.StoreIDEnv != "" {
			storeID = strings.TrimSpace(os.Getenv(cfg.StoreIDEnv))
		}
		if storeID == "" {
			return nil, errors.New("the Vercel Blob storage DSN must include a store ID when using OIDC authentication")
		}

		backend.storeID = normalizeVercelStoreID(storeID)
		backend.tokenFn = func(ctx context.Context) (string, error) {
			return resolveVercelOIDCToken(ctx, cfg.OIDCTokenEnv)
		}

		return backend, nil
	}

	// A read-write token is preferred from the environment, so the configuration file doesn't have to carry a secret
	token := cfg.Token
	if token == "" && cfg.TokenEnv != "" {
		token = strings.TrimSpace(os.Getenv(cfg.TokenEnv))
	}
	if token == "" {
		return nil, errors.New("the Vercel Blob storage DSN requires a read-write token")
	}

	backend.storeID = vercelStoreIDFromToken(token)
	backend.readWriteToken = token
	backend.tokenFn = func(_ context.Context) (string, error) {
		return token, nil
	}

	return backend, nil
}

// Description returns the store objects are written to, without the token that authenticates the requests
func (v *VercelBlobBackend) Description() string {
	desc := "Vercel Blob, in store '" + v.storeID + "' with " + v.access + " access"
	if v.prefix != "" {
		desc += " under prefix '" + v.prefix + "'"
	}

	return desc
}

// Put uploads an object through the Blob control-plane API
func (v *VercelBlobBackend) Put(ctx context.Context, key string, body io.Reader, maxSize int64, contentType string) (PutResult, error) {
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

	endpoint, err := url.Parse(v.apiEndpoint)
	if err != nil {
		return PutResult{}, fmt.Errorf("failed to parse the Vercel Blob endpoint: %w", err)
	}
	query := endpoint.Query()
	query.Set("pathname", prefixedKey(v.prefix, key))
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint.String(), file)
	if err != nil {
		return PutResult{}, fmt.Errorf("failed to create the Vercel Blob put request: %w", err)
	}
	req.ContentLength = size

	err = v.setCommonHeaders(ctx, req)
	if err != nil {
		return PutResult{}, err
	}

	req.Header.Set("X-Vercel-Blob-Access", v.access)
	req.Header.Set("X-Add-Random-Suffix", "0")
	// Overwrites are allowed because a retried write regenerates the same object at the same key
	req.Header.Set("X-Allow-Overwrite", "1")
	if contentType != "" {
		req.Header.Set("X-Content-Type", contentType)
	}

	res, err := v.httpClient.Do(req)
	if err != nil {
		return PutResult{}, fmt.Errorf("failed to put the Vercel Blob object: %w", err)
	}
	defer res.Body.Close() //nolint:errcheck

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return PutResult{}, vercelBlobResponseError(res)
	}

	result := PutResult{Size: size}

	// The URL of a public object is read back from the response rather than built, since it's Vercel that decides it
	if v.access == accessPublic {
		var blob vercelBlob
		err = json.NewDecoder(res.Body).Decode(&blob)
		if err != nil {
			return PutResult{}, fmt.Errorf("failed to decode the Vercel Blob put response: %w", err)
		}
		result.URL = blob.URL
	}

	return result, nil
}

// Get downloads an object from the store's own domain, which is where Blob serves object content from
func (v *VercelBlobBackend) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	return v.get(ctx, key, false)
}

// GetConsistent downloads an object from origin storage instead of the CDN cache the reads of a store are served through
// Blob keeps a cached response for as long as a minute, so this is what a read that must see the current object uses
func (v *VercelBlobBackend) GetConsistent(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	return v.get(ctx, key, true)
}

func (v *VercelBlobBackend) get(ctx context.Context, key string, bypassCache bool) (io.ReadCloser, ObjectInfo, error) {
	err := ValidateKey(key)
	if err != nil {
		return nil, ObjectInfo{}, err
	}

	endpoint, err := v.objectURL(key, bypassCache)
	if err != nil {
		return nil, ObjectInfo{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, ObjectInfo{}, fmt.Errorf("failed to create the Vercel Blob get request: %w", err)
	}

	err = v.setCommonHeaders(ctx, req)
	if err != nil {
		return nil, ObjectInfo{}, err
	}

	res, err := v.httpClient.Do(req)
	if err != nil {
		return nil, ObjectInfo{}, fmt.Errorf("failed to get the Vercel Blob object: %w", err)
	}

	switch {
	case res.StatusCode == http.StatusNotFound:
		res.Body.Close() //nolint:errcheck,gosec
		return nil, ObjectInfo{}, ErrNotFound
	case res.StatusCode < 200 || res.StatusCode >= 300:
		defer res.Body.Close() //nolint:errcheck
		return nil, ObjectInfo{}, vercelBlobResponseError(res)
	}

	// The caller owns the body from here on, since it's the object's content
	return res.Body, ObjectInfo{Size: res.ContentLength, ContentType: res.Header.Get("Content-Type")}, nil
}

// Exists reports whether an object is stored at a key, asking the control-plane API rather than the store's own domain
//
// The lookup deliberately avoids the domain reads go to: that domain is fronted by the CDN, so asking it for an object that hasn't been written yet lets the "not found" it answers with be cached, and a read that follows the write can still be served that cached answer
// The API endpoint has no cache in front of it, which makes this safe to call immediately before a write
func (v *VercelBlobBackend) Exists(ctx context.Context, key string) (bool, error) {
	err := ValidateKey(key)
	if err != nil {
		return false, err
	}

	// The API resolves an object from its URL, which identifies it unambiguously under either access level
	objectURL, err := v.objectURL(key, false)
	if err != nil {
		return false, err
	}

	endpoint, err := url.Parse(v.apiEndpoint)
	if err != nil {
		return false, fmt.Errorf("failed to parse the Vercel Blob endpoint: %w", err)
	}
	query := endpoint.Query()
	query.Set("url", objectURL)
	endpoint.RawQuery = query.Encode()

	// The metadata lookup is a GET rather than a HEAD, because its outcome is carried by the response body
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return false, fmt.Errorf("failed to create the Vercel Blob head request: %w", err)
	}

	err = v.setCommonHeaders(ctx, req)
	if err != nil {
		return false, err
	}

	res, err := v.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("failed to look up the Vercel Blob object: %w", err)
	}
	defer res.Body.Close() //nolint:errcheck

	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return true, nil
	}

	// Blob reports what went wrong with a code in the body rather than with the status alone, and it's that code, like the official client, that tells an object which isn't there apart from a request that failed
	// Anything else is an error, so a rejected or misrouted lookup can never be mistaken for a key that is free to write
	body, _ := io.ReadAll(io.LimitReader(res.Body, maxErrorBodySize))
	if vercelBlobErrorCode(body) == vercelBlobErrorNotFound {
		return false, nil
	}

	return false, vercelBlobError(res.Status, body)
}

// Delete removes an object
// The Blob API accepts a pathname or a full URL, and deleting a key that doesn't exist isn't an error
func (v *VercelBlobBackend) Delete(ctx context.Context, key string) error {
	err := ValidateKey(key)
	if err != nil {
		return err
	}

	payload, err := json.Marshal(map[string]any{"urls": []string{prefixedKey(v.prefix, key)}})
	if err != nil {
		return fmt.Errorf("failed to encode the Vercel Blob delete request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.apiEndpoint+"/delete", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to create the Vercel Blob delete request: %w", err)
	}

	err = v.setCommonHeaders(ctx, req)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := v.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to delete the Vercel Blob object: %w", err)
	}
	defer res.Body.Close() //nolint:errcheck

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return vercelBlobResponseError(res)
	}

	return nil
}

// SupportsDirectUpload reports whether a browser upload token can be signed, which needs the read-write token OIDC authentication doesn't have
func (v *VercelBlobBackend) SupportsDirectUpload() bool {
	return v.readWriteToken != ""
}

// CreateDirectUpload returns a client token scoped to one exact Blob pathname
func (v *VercelBlobBackend) CreateDirectUpload(_ context.Context, key string, maxSize int64, contentType string, expiresIn time.Duration) (DirectUpload, error) {
	err := ValidateKey(key)
	if err != nil {
		return DirectUpload{}, err
	}

	pathname := prefixedKey(v.prefix, key)
	token, err := v.clientUploadToken(pathname, contentType, maxSize, expiresIn)
	if err != nil {
		return DirectUpload{}, err
	}

	// The browser PUT goes to the same control-plane endpoint the server writes through
	endpoint, err := url.Parse(v.apiEndpoint)
	if err != nil {
		return DirectUpload{}, fmt.Errorf("failed to parse the Vercel Blob endpoint: %w", err)
	}
	query := endpoint.Query()
	query.Set("pathname", pathname)
	endpoint.RawQuery = query.Encode()

	return DirectUpload{
		Key:    key,
		URL:    endpoint.String(),
		Method: http.MethodPut,
		Headers: map[string]string{
			"Authorization":        "Bearer " + token,
			"X-API-Version":        vercelBlobAPIVersion,
			"X-Vercel-Blob-Access": v.access,
			"X-Content-Type":       contentType,
			"X-Add-Random-Suffix":  "0",
			"X-Allow-Overwrite":    "1",
		},
	}, nil
}

// HasPublicAddressing reports whether the objects of this store are served from a stable Vercel Blob URL
func (v *VercelBlobBackend) HasPublicAddressing() bool {
	return v.access == accessPublic
}

// PublicURL returns the stable per-store URL of a public Blob object
func (v *VercelBlobBackend) PublicURL(key string) (string, error) {
	if !v.HasPublicAddressing() {
		return "", nil
	}
	err := ValidateKey(key)
	if err != nil {
		return "", err
	}

	return v.objectURL(key, false)
}

// Sign a short-lived token that authorizes a browser to upload one exact pathname, so the read-write token never leaves the process
func (v *VercelBlobBackend) clientUploadToken(pathname string, contentType string, maxSize int64, expiresIn time.Duration) (string, error) {
	if v.readWriteToken == "" {
		return "", fmt.Errorf("%w: a Vercel Blob read-write token is required to authorize a client upload", ErrDirectUploadUnsupported)
	}
	if v.storeID == "" {
		return "", errors.New("the Vercel Blob token does not contain a store ID")
	}

	payload := vercelClientTokenPayload{
		Pathname:            pathname,
		AllowedContentTypes: []string{contentType},
		MaximumSizeInBytes:  maxSize,
		ValidUntil:          time.Now().Add(expiresIn).UnixMilli(),
		AllowOverwrite:      true,
		AddRandomSuffix:     false,
		CacheControlMaxAge:  0,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to encode the Vercel Blob client upload token: %w", err)
	}

	payloadEncoded := base64.StdEncoding.EncodeToString(payloadJSON)
	signature := hex.EncodeToString(hmacBytes([]byte(v.readWriteToken), []byte(payloadEncoded)))
	secured := base64.StdEncoding.EncodeToString([]byte(signature + "." + payloadEncoded))

	return "vercel_blob_client_" + v.storeID + "_" + secured, nil
}

// Build the URL object content is served from, which is a per-store domain that depends on the access level
// A request that must not be answered from the CDN carries the parameter that sends it to origin storage, which Blob only honors for a private store
func (v *VercelBlobBackend) objectURL(key string, bypassCache bool) (string, error) {
	if v.storeID == "" {
		return "", errors.New("the Vercel Blob token does not contain a store ID")
	}

	host := v.storeID + ".public.blob.vercel-storage.com"
	if v.access == accessPrivate {
		host = v.storeID + ".private.blob.vercel-storage.com"
	}

	endpoint := &url.URL{
		Scheme: "https",
		Host:   host,
		Path:   "/" + prefixedKey(v.prefix, key),
	}
	if bypassCache && v.access == accessPrivate {
		endpoint.RawQuery = url.Values{vercelBlobCacheParam: []string{"0"}}.Encode()
	}

	return endpoint.String(), nil
}

// Set the headers every Blob request carries, resolving the bearer token for this operation
func (v *VercelBlobBackend) setCommonHeaders(ctx context.Context, req *http.Request) error {
	token, err := v.tokenFn(ctx)
	if err != nil {
		return err
	}
	if token == "" {
		return errors.New("the Vercel Blob token is empty")
	}

	// These headers mirror the request envelope of the official SDK
	req.Header.Set("Authorization", "Bearer "+token)
	if v.storeID != "" {
		req.Header.Set("X-Vercel-Blob-Store-Id", v.storeID)
	}
	req.Header.Set("X-Api-Version", vercelBlobAPIVersion)
	req.Header.Set("X-Api-Blob-Request-Id", v.requestID())
	req.Header.Set("X-Api-Blob-Request-Attempt", "0")
	req.Header.Set("User-Agent", "objstore-vercel-blob/1")

	return nil
}

// Build the request ID Vercel expects on Blob API calls
// It's diagnostic only, so a failure to read random bytes falls back to the timestamp alone
func (v *VercelBlobBackend) requestID() string {
	now := strconv.FormatInt(time.Now().UnixMilli(), 10)

	var raw [8]byte
	_, err := rand.Read(raw[:])
	if err != nil {
		return v.storeID + ":" + now
	}

	return v.storeID + ":" + now + ":" + hex.EncodeToString(raw[:])
}

// Resolve the Vercel OIDC token, preferring the one attached to the current request over the environment
// Vercel rotates the deployment token, and the request header is the freshest source of it, which is what the official SDK reads too
func resolveVercelOIDCToken(ctx context.Context, configuredEnvironment string) (string, error) {
	token := verceloidc.TokenFromContext(ctx)
	if token != "" {
		return token, nil
	}

	// The variable the DSN names takes precedence over Vercel's standard one
	environments := []string{configuredEnvironment, verceloidc.Environment}
	for _, environment := range environments {
		environment = strings.TrimSpace(environment)
		if environment == "" {
			continue
		}

		token = strings.TrimSpace(os.Getenv(environment))
		if token != "" {
			return token, nil
		}
	}

	return "", errors.New("the Vercel OIDC token is required in the request context or in the environment")
}

// Read-write tokens carry the store ID as their fourth underscore-delimited segment
func vercelStoreIDFromToken(token string) string {
	parts := strings.Split(token, "_")
	if len(parts) < 4 {
		return ""
	}

	return parts[3]
}

func normalizeVercelStoreID(storeID string) string {
	return strings.TrimPrefix(strings.TrimSpace(storeID), "store_")
}

// Build the error for a response Vercel Blob rejected, including the beginning of the body since that's where the reason is
func vercelBlobResponseError(res *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(res.Body, maxErrorBodySize))
	return vercelBlobError(res.Status, body)
}

func vercelBlobError(status string, body []byte) error {
	return fmt.Errorf("vercel blob returned %s: %s", status, strings.TrimSpace(string(body)))
}

// Read the code out of a rejected response, which is how the Blob API says what went wrong
// A body that isn't one of its error envelopes has no code, and is reported as the failure it is rather than being interpreted
func vercelBlobErrorCode(body []byte) string {
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}

	err := json.Unmarshal(body, &envelope)
	if err != nil {
		return ""
	}

	return envelope.Error.Code
}

// The subset of the metadata a Blob write returns that this package uses
type vercelBlob struct {
	URL string `json:"url"`
}

type vercelClientTokenPayload struct {
	Pathname            string   `json:"pathname"`
	AllowedContentTypes []string `json:"allowedContentTypes,omitempty"`
	MaximumSizeInBytes  int64    `json:"maximumSizeInBytes,omitempty"`
	ValidUntil          int64    `json:"validUntil"`
	AllowOverwrite      bool     `json:"allowOverwrite"`
	AddRandomSuffix     bool     `json:"addRandomSuffix"`
	CacheControlMaxAge  int      `json:"cacheControlMaxAge"`
}
