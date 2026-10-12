// Package objstore is a small object store abstraction over the local file system, S3 (and S3-compatible stores), and Vercel Blob
//
// A backend is created from a single DSN, so the choice of store is a configuration value rather than a build-time decision, and objects are addressed by an opaque key that means the same thing in every backend
// The backends talk to their providers over plain HTTP, signing their own requests, so the package depends on nothing outside the standard library
package objstore

import (
	"context"
	"errors"
	"io"
	"time"
)

var (
	// ErrTooLarge is returned when an object is larger than the maximum size the caller allowed
	ErrTooLarge = errors.New("object is larger than allowed")

	// ErrNotFound is returned when an object key doesn't exist in the backend
	ErrNotFound = errors.New("object not found")

	// ErrDirectUploadUnsupported is returned by the backends that can't hand a client an upload target of its own
	ErrDirectUploadUnsupported = errors.New("direct upload is not supported by this storage backend")
)

// Backend is one object store objects are read from and written to
//
// Keys are relative and use forward slashes, so the same key addresses the same object in every backend
// A backend applies the key prefix its DSN configured on its own, which means callers deal in logical keys and never have to know whether a prefix is in play
type Backend interface {
	// Put writes an object, reading at most maxSize bytes from body and returning ErrTooLarge if there are more
	// Writes replace whatever is at the key already, since the callers of this package retry
	Put(ctx context.Context, key string, body io.Reader, maxSize int64, contentType string) (PutResult, error)

	// Get opens an object for reading, returning ErrNotFound if the key doesn't exist
	// The caller owns the returned reader and must close it
	Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error)

	// GetConsistent opens an object the way Get does, bypassing whatever cache sits between this process and the store
	// It's what a read that has to reflect a write that just happened uses, because a cached read can still be answered from before it
	// A backend with no cache in front of it reads consistently already, so for those this is Get
	GetConsistent(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error)

	// Exists reports whether an object is stored at a key
	// It's separate from Get because a backend can answer it from metadata alone, which keeps a lookup for an object that isn't there yet off the path its reads are served from
	Exists(ctx context.Context, key string) (bool, error)

	// Delete removes an object, and does nothing if the key doesn't exist
	Delete(ctx context.Context, key string) error

	// Description returns a short description of the backend, suitable for logging
	// It never includes credentials, since it ends up in the logs
	Description() string
}

// PutResult describes the object a write created
// URL is set only by the backends that address their objects publicly, and is the URL the object is served from
type PutResult struct {
	Size int64
	URL  string
}

// ObjectInfo is the metadata a backend returns alongside an object's content
// A backend that doesn't report a size returns -1, which is what an HTTP response with no Content-Length gives
type ObjectInfo struct {
	Size        int64
	ContentType string
}

// DirectUpload describes where a client should upload one object, so its body never travels through the process that authorized it
type DirectUpload struct {
	Key     string            `json:"key"`
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers,omitempty"`
}

// DirectUploadBackend is implemented by every backend, and reports whether it can issue an upload target a client uses directly
// It's a separate interface so a caller that has no use for direct uploads doesn't have to care that the capability exists
type DirectUploadBackend interface {
	// SupportsDirectUpload reports whether CreateDirectUpload returns a usable target
	SupportsDirectUpload() bool

	// CreateDirectUpload returns a short-lived target scoped to one exact key, or ErrDirectUploadUnsupported
	CreateDirectUpload(ctx context.Context, key string, maxSize int64, contentType string, expiresIn time.Duration) (DirectUpload, error)
}

// PublicAddressingBackend is implemented by every backend, and resolves stable URLs for objects that browsers can address directly
type PublicAddressingBackend interface {
	HasPublicAddressing() bool
	PublicURL(key string) (string, error)
}
