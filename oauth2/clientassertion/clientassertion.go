// Package clientassertion obtains client assertions for OAuth2 clients that authenticate with federated credentials rather than a client secret
//
// The assertion is sent to the token endpoint as a JWT bearer client assertion, as defined by RFC 7523 section 2.2: the request carries client_assertion_type set to AssertionType and client_assertion set to the value a provider returns, and no client secret
package clientassertion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"golang.org/x/sync/singleflight"
)

// AssertionType is the value of the client_assertion_type parameter that accompanies an assertion obtained from a provider
const AssertionType = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"

const kubernetesServiceAccountTokenPathDefault = "/var/run/secrets/kubernetes.io/serviceaccount/token" //nolint:gosec

// ProviderFn returns the client assertion to include in a request to the token endpoint
// It is invoked for every token request, so it returns either a fresh assertion or one it cached while it is still valid
type ProviderFn func(ctx context.Context) (string, error)

// NewProvider returns the provider selected by a clientAssertion configuration value, which is matched case-insensitively
//
// These values are supported:
//
//   - `AzureManagedIdentity`: uses Azure Managed Identity with a system-assigned identity
//   - `AzureManagedIdentity=client-id`: uses Azure Managed Identity with a user-assigned identity whose client id is "client-id"
//   - `AzureWorkloadIdentity`: uses Azure Workload Identity, e.g. in Kubernetes
//   - `KubernetesServiceAccountToken=path`: uses a token read from a Kubernetes service account token file, defaulting to `/var/run/secrets/kubernetes.io/serviceaccount/token` when `path` is omitted
//   - `tsiam=endpoint`: uses tsiam to obtain Workload Identity from nodes that use Tailscale, requesting a token for audience as its resource
//
// An empty value returns a nil provider and no error, because it means the client authenticates with a client secret instead
// The audience is only used by tsiam
func NewProvider(clientAssertion string, audience string) (ProviderFn, error) {
	clientAssertionLC := strings.ToLower(clientAssertion)
	switch {
	// If there's no client assertion, return
	case clientAssertionLC == "":
		return nil, nil

	// Azure with user-assigned managed identity
	case strings.HasPrefix(clientAssertionLC, "azuremanagedidentity="):
		fic, err := azidentity.NewManagedIdentityCredential(&azidentity.ManagedIdentityCredentialOptions{
			ID: azidentity.ClientID(clientAssertion[len("azuremanagedidentity="):]),
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create Azure managed Identity credential object: %w", err)
		}
		return azureProvider(fic), nil

	// Azure with system-assigned managed identity
	case clientAssertionLC == "azuremanagedidentity":
		fic, err := azidentity.NewManagedIdentityCredential(nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create Azure Managed Identity credential object: %w", err)
		}
		return azureProvider(fic), nil

	// Azure Workload Identity
	case clientAssertionLC == "azureworkloadidentity":
		fic, err := azidentity.NewWorkloadIdentityCredential(nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create Azure Workload Identity credential object: %w", err)
		}
		return azureProvider(fic), nil

	// tsiam
	case strings.HasPrefix(clientAssertionLC, "tsiam="):
		fn, err := tsiamProvider(clientAssertion[len("tsiam="):], audience, http.DefaultClient)
		if err != nil {
			return nil, fmt.Errorf("failed to create tsiam client assertion provider: %w", err)
		}
		return fn, nil

	// Kubernetes service account token with default path
	case clientAssertionLC == "kubernetesserviceaccounttoken":
		fn, err := kubernetesServiceAccountTokenProvider(kubernetesServiceAccountTokenPathDefault)
		if err != nil {
			return nil, fmt.Errorf("failed to create Kubernetes service account token client assertion provider: %w", err)
		}
		return fn, nil

	// Kubernetes service account token with custom path
	case strings.HasPrefix(clientAssertionLC, "kubernetesserviceaccounttoken="):
		fn, err := kubernetesServiceAccountTokenProvider(clientAssertion[len("kubernetesserviceaccounttoken="):])
		if err != nil {
			return nil, fmt.Errorf("failed to create Kubernetes service account token client assertion provider: %w", err)
		}
		return fn, nil

	// Invalid value supplied
	default:
		return nil, fmt.Errorf("invalid value for configuration option 'clientAssertion': '%s'", clientAssertion)
	}
}

func azureProvider(fic azcore.TokenCredential) ProviderFn {
	return func(ctx context.Context) (string, error) {
		// Get the client assertion
		clientAssertion, err := fic.GetToken(ctx, policy.TokenRequestOptions{
			// This is a constant value
			Scopes: []string{"api://AzureADTokenExchange"},
		})
		if err != nil {
			return "", fmt.Errorf("failed to obtain client assertion: %w", err)
		}

		return clientAssertion.Token, nil
	}
}

func tsiamProvider(endpoint string, audience string, client *http.Client) (ProviderFn, error) {
	if audience == "" {
		return nil, errors.New("audience is empty")
	}

	// Parse the URL
	u, err := url.Parse(strings.TrimSuffix(endpoint, "/") + "/token")
	if err != nil {
		return nil, fmt.Errorf("failed to parse endpoint URL: %w", err)
	}
	q := u.Query()
	q.Set("resource", audience)
	u.RawQuery = q.Encode()
	reqUrl := u.String()

	type tokenResponse struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   string `json:"expires_in"`
	}

	// Token cache state
	var (
		cacheMu     sync.Mutex
		cachedToken string
		cacheExpiry time.Time
		sfGroup     singleflight.Group
	)

	// Create the function
	fn := func(parentCtx context.Context) (string, error) {
		// Return the cached token if it is still valid
		cacheMu.Lock()
		if cachedToken != "" && time.Now().Before(cacheExpiry) {
			defer cacheMu.Unlock()
			return cachedToken, nil
		}
		cacheMu.Unlock()

		// Condense concurrent refresh requests into a single HTTP call
		res, err, _ := sfGroup.Do("", func() (any, error) {
			// Re-check the cache: another goroutine may have already refreshed it while we were waiting to enter the singleflight group
			cacheMu.Lock()
			if cachedToken != "" && time.Now().Before(cacheExpiry) {
				defer cacheMu.Unlock()
				return cachedToken, nil
			}
			cacheMu.Unlock()

			// Use a background context so that a single caller's cancellation does not abort a shared in-flight request that other callers are also waiting on
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			req, rErr := http.NewRequestWithContext(ctx, http.MethodPost, reqUrl, nil)
			if rErr != nil {
				return "", fmt.Errorf("failed to create request for URL '%s': %w", reqUrl, rErr)
			}
			req.Header.Set("X-Tsiam", "1")

			resp, rErr := client.Do(req)
			if rErr != nil {
				return "", fmt.Errorf("error requesting token: %w", rErr)
			}
			defer resp.Body.Close() //nolint:errcheck

			if resp.StatusCode != http.StatusOK {
				return "", fmt.Errorf("error requesting token: response status is not OK: %d", resp.StatusCode)
			}

			var tokenResp tokenResponse
			rErr = json.NewDecoder(resp.Body).Decode(&tokenResp)
			if rErr != nil {
				return "", fmt.Errorf("error parsing response: %w", rErr)
			}

			// Cache the token only when the effective TTL (expiry minus 1m) is positive
			if tokenResp.ExpiresIn != "" {
				expiresInSec, rErr := strconv.ParseInt(tokenResp.ExpiresIn, 10, 64)
				if rErr == nil {
					effectiveExpiry := time.Now().Add(time.Duration(expiresInSec)*time.Second - time.Minute)
					if effectiveExpiry.After(time.Now()) {
						cacheMu.Lock()
						cachedToken = tokenResp.AccessToken
						cacheExpiry = effectiveExpiry
						cacheMu.Unlock()
					}
				}
			}

			return tokenResp.AccessToken, nil
		})
		if err != nil {
			return "", err
		}

		return res.(string), nil //nolint:forcetypeassert
	}

	return fn, nil
}

func kubernetesServiceAccountTokenProvider(tokenPath string) (ProviderFn, error) {
	if tokenPath == "" {
		return nil, errors.New("token path is empty")
	}

	// The file is read on every call because Kubernetes rotates projected tokens in place
	return func(_ context.Context) (string, error) {
		tokenBytes, err := os.ReadFile(tokenPath)
		if err != nil {
			return "", fmt.Errorf("failed to read Kubernetes service account token from '%s': %w", tokenPath, err)
		}

		token := strings.TrimSpace(string(tokenBytes))
		if token == "" {
			return "", fmt.Errorf("the Kubernetes service account token file '%s' is empty", tokenPath)
		}

		return token, nil
	}, nil
}
