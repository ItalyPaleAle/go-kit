// Package verceloidc carries the Vercel OIDC token of the current request to the object store backends that authenticate with it
package verceloidc

import (
	"context"
	"strings"
)

const (
	// Header is the request header Vercel uses to provide the current deployment OIDC token
	Header = "X-Vercel-Oidc-Token"
	// Environment is the official process-level fallback used by Vercel's OIDC tooling
	Environment = "VERCEL_OIDC_TOKEN"
)

type tokenContextKey struct{}

// WithToken attaches the current request's Vercel OIDC token to downstream operations
func WithToken(ctx context.Context, token string) context.Context {
	token = strings.TrimSpace(token)
	if token == "" {
		return ctx
	}

	return context.WithValue(ctx, tokenContextKey{}, token)
}

// TokenFromContext returns the Vercel OIDC token attached to the current request
func TokenFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}

	token, ok := ctx.Value(tokenContextKey{}).(string)
	if !ok {
		return ""
	}

	return token
}
