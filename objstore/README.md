# objstore

Object store abstraction over the local file system, S3 (and S3-compatible stores), and Vercel Blob, using only the Go standard library.

```go
store, err := objstore.New("s3://key:secret@s3.us-east-1.amazonaws.com/bucket?prefix=images", objstore.Options{})
if err != nil {
	return err
}

result, err := store.Put(ctx, "site/photo.webp", body, maxSize, "image/webp")
```

## Connection strings

| Backend | DSN |
| --- | --- |
| Local file system | `/path/to/folder`, `fs:/path/to/folder`, or `local:/path/to/folder` (optional: `?fileMode=0644&dirMode=0755`) |
| S3 / S3-compatible | `s3://<access-key>:<secret-key>@<host>/<bucket>?region=<region>&prefix=<prefix>&sessionToken=<token>&publicUrl=<https-url>` |
| Vercel Blob (token) | `vercelblob:?tokenEnv=<env-var>&access=<private\|public>&prefix=<prefix>` |
| Vercel Blob (OIDC) | `vercelblob:?auth=oidc&storeIdEnv=<env-var>&access=<private\|public>&prefix=<prefix>` |

- Plain paths without a scheme (including Windows drive paths such as `C:\data`) select the local file system backend.
- `prefix` is prepended by the backend to every object key automatically.
- Local file system permissions default to `0600` for files (minimum `0600`) and `0750` for newly created folders (minimum `0700`), bypassing `umask`. Existing folders keep their permissions.
- On S3, `region` can be omitted when the host has the `s3.<region>.amazonaws.com` form.
- On Vercel Blob, `access` defaults to `private`, and `tokenEnv` defaults to `BLOB_READ_WRITE_TOKEN`. In OIDC mode, tokens are resolved per operation from `verceloidc.WithToken(ctx, ...)` first and then from `oidcTokenEnv` or `VERCEL_OIDC_TOKEN`.

## Interfaces and behavior

- `Backend` provides `Put`, `Get`, `GetConsistent`, `Exists`, `Delete`, and `Description`. `Put` overwrites existing objects and returns `ErrTooLarge` if the body exceeds `maxSize`. HTTP backends spool uploads to a temporary file so `Content-Length` is set without buffering the full body in memory. `Delete` succeeds if the key does not exist.
- `PublicAddressingBackend` (`HasPublicAddressing`, `PublicURL`) is implemented by all backends and enabled when S3 sets `publicUrl` or Vercel Blob uses `access=public`. When enabled, `Put` also populates `PutResult.URL`.
- `DirectUploadBackend` (`SupportsDirectUpload`, `CreateDirectUpload`) is implemented by all backends for browser/client direct uploads:
  - S3 returns a presigned `PUT` URL scoped to the key, content type, and TTL (`maxSize` cannot be enforced server-side by S3 `PUT`).
  - Vercel Blob returns a signed client upload token enforcing key, content type, `maxSize`, and TTL (requires read-write token auth).
  - Local file system and Vercel Blob with OIDC return `ErrDirectUploadUnsupported`.
