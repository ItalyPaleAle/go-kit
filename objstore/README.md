# objstore

A small object store abstraction for Go, over the local file system, S3 (and S3-compatible stores), and Vercel Blob.

A backend is created from a single DSN, so which store a program uses is a configuration value rather than a build-time decision, and objects are addressed by an opaque key that means the same thing in every backend. The backends talk to their providers over plain HTTP and sign their own requests, so the package depends on nothing outside the standard library.

```go
store, err := objstore.New("s3://key:secret@s3.us-east-1.amazonaws.com/bucket?prefix=images", objstore.Options{})
if err != nil {
	return err
}

result, err := store.Put(ctx, "site/photo.webp", body, maxSize, "image/webp")
```

## Connection strings

| Backend | DSN |
|---|---|
| Local file system | `/path/to/folder`, `fs:/path/to/folder`, or `local:/path/to/folder` |
| S3, or an S3-compatible store | `s3://<access-key>:<secret-key>@s3.<region>.amazonaws.com/<bucket>?prefix=<key-prefix>&sessionToken=<token>&publicUrl=<base-url>` |
| Vercel Blob, with a read-write token | `vercelblob:?tokenEnv=<environment-variable>&access=<private\|public>&prefix=<key-prefix>` |
| Vercel Blob, with OIDC | `vercelblob:?auth=oidc&storeIdEnv=<environment-variable>&access=<private\|public>&prefix=<key-prefix>` |

A DSN with no scheme at all is a file system path, and a single-character scheme is read as a Windows drive letter rather than as a backend.

The `prefix` option is the backend's own concern: it's applied to every key on the way to the provider, so callers pass logical keys and never have to know whether a prefix is in play.

## What a backend does

Every backend implements `Backend`, which includes `Put`, `Get`, `Delete`. There are also two optional methods:

- `PublicAddressingBackend` reports whether objects are served from stable URLs. When they are, `Put` returns the URL of the object it wrote, and a program can hand that URL out instead of proxying the object itself. S3 answers yes when the DSN configures a `publicUrl`, and Vercel Blob when the store is `public`.
- `DirectUploadBackend` issues a short-lived, single-key upload target, so a browser can upload straight to the store and the body never travels through the process that authorized it. S3 signs a presigned `PUT`. Vercel Blob signs a client token, which needs the read-write token that OIDC authentication doesn't have. The file system has no such target and returns `ErrDirectUploadUnsupported`.

Both are implemented by every backend in this package, so a type assertion always succeeds and the answer is in the return value.

## Notes

- Writes replace whatever is at the key already, and deletes of a key that doesn't exist succeed, so an operation that's retried after a failure lands in the same place.
- The two HTTP backends spool a bounded body to a temporary file before sending it, which is what lets them declare a `Content-Length` without holding the whole object in memory. Reading stops one byte past the limit, so an object exactly at the limit is told apart from one over it.
- A Vercel OIDC token is resolved for every operation rather than captured once, because it expires while a process keeps running. It's read from the request context first (attach it with `verceloidc.WithToken`) and from the environment after that.
