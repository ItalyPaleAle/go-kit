package objstore

import (
	"errors"
	"fmt"
	"strings"
)

// Schemes the DSN of a backend can use
const (
	// SchemeFS and SchemeLocal both select the local file system, and so does a DSN with no scheme at all
	SchemeFS    = "fs"
	SchemeLocal = "local"
	// SchemeS3 selects S3 or any S3-compatible object store
	SchemeS3 = "s3"
	// SchemeVercelBlob selects Vercel Blob
	SchemeVercelBlob = "vercelblob"
)

// Options are the settings that can't be expressed in a DSN, because they're properties of the process rather than of the store
type Options struct {
	// Environment variable the Vercel Blob store ID is read from when a DSN that authenticates with OIDC doesn't name one
	// It's an option rather than a default because a process that talks to more than one Blob store needs a different variable for each
	DefaultVercelBlobStoreIDEnv string
}

// New creates a backend from its DSN
//
// Local file system: `fs:/path/to/folder`, `local:/path/to/folder`, or a path with no scheme at all
// S3: `s3://<access-key>:<secret-key>@s3.<region>.amazonaws.com/<bucket>?prefix=<key-prefix>&sessionToken=<token>&publicUrl=<base-url>`
// Vercel Blob: `vercelblob:?tokenEnv=<environment-variable>&access=<public|private>&prefix=<key-prefix>`, or `vercelblob:?auth=oidc&storeIdEnv=<environment-variable>&…`
func New(dsn string, opts Options) (Backend, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return nil, errors.New("the storage DSN is empty")
	}

	// Only the schemes this package owns are split off, so a plain file system path such as C:\data stays a path
	scheme, value := Scheme(dsn)
	switch scheme {
	case "", SchemeFS, SchemeLocal:
		return newFSBackend(value)
	case SchemeS3:
		return newS3BackendFromDSN(dsn)
	case SchemeVercelBlob:
		return newVercelBlobBackendFromDSN(dsn, opts.DefaultVercelBlobStoreIDEnv)
	default:
		return nil, fmt.Errorf("unsupported storage DSN scheme '%s'", scheme)
	}
}

// DSNHasPublicAddressing reports whether the backend a DSN describes serves its objects from stable public URLs
//
// It answers from the DSN alone, without creating the backend, so it can be called during validation of a configuration whose credentials aren't in the environment yet
func DSNHasPublicAddressing(dsn string) (bool, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return false, errors.New("the storage DSN is empty")
	}

	scheme, _ := Scheme(dsn)
	switch scheme {
	case "", SchemeFS, SchemeLocal:
		return false, nil
	case SchemeS3:
		cfg, err := parseS3DSN(dsn)
		if err != nil {
			return false, err
		}
		return cfg.PublicURL != "", nil
	case SchemeVercelBlob:
		cfg, err := parseVercelBlobDSN(dsn)
		if err != nil {
			return false, err
		}
		return cfg.Access == accessPublic, nil
	default:
		return false, fmt.Errorf("unsupported storage DSN scheme '%s'", scheme)
	}
}

// Scheme splits the scheme off a DSN, returning an empty scheme for the ones that are plain file system paths
//
// A single-character prefix is a Windows drive letter rather than a scheme, which is why the separator has to be past the second character
// The value is what the backend for that scheme parses: the path alone for the file system, and the whole DSN for the ones that read it as a URL
func Scheme(dsn string) (scheme string, value string) {
	idx := strings.IndexByte(dsn, ':')
	if idx <= 1 {
		return "", dsn
	}

	scheme = strings.ToLower(dsn[:idx])
	switch {
	case scheme == SchemeFS, scheme == SchemeLocal:
		return scheme, dsn[idx+1:]
	case scheme == SchemeS3, scheme == SchemeVercelBlob:
		return scheme, dsn
	case strings.HasPrefix(dsn[idx:], "://"):
		// A DSN that's shaped like a URL was meant to name a backend, so an unknown one is reported rather than quietly turned into a folder with that name
		return scheme, dsn
	default:
		return "", dsn
	}
}
