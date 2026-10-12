package objstore

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSelectsTheFileSystem(t *testing.T) {
	dir := t.TempDir()

	tests := []string{
		filepath.Join(dir, "plain"),
		"fs:" + filepath.Join(dir, "fs"),
		"local:" + filepath.Join(dir, "local"),
	}

	for _, dsn := range tests {
		t.Run(dsn, func(t *testing.T) {
			backend, err := New(dsn, Options{})
			require.NoError(t, err)
			assert.IsType(t, &FSBackend{}, backend)
		})
	}
}

func TestNewErrors(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
	}{
		{name: "empty", dsn: ""},
		{name: "unsupported scheme", dsn: "gcs://bucket"},
		{name: "s3 without credentials", dsn: "s3://s3.us-east-1.amazonaws.com/bucket"},
		{name: "s3 without a bucket", dsn: "s3://key:secret@s3.us-east-1.amazonaws.com"},
		{name: "s3 without a region", dsn: "s3://key:secret@storage.example.com/bucket"},
		{name: "s3 with a public URL that isn't absolute", dsn: "s3://key:secret@s3.us-east-1.amazonaws.com/bucket?publicUrl=%2Fassets"},
		{name: "s3 with a public URL that isn't HTTPS", dsn: "s3://key:secret@s3.us-east-1.amazonaws.com/bucket?publicUrl=http%3A%2F%2Fassets.example.test"},
		{name: "vercel blob without a token", dsn: "vercelblob:?tokenEnv=A_VARIABLE_THAT_IS_NOT_SET"},
		{name: "vercel blob with an invalid access", dsn: "vercelblob:?access=shared"},
		{name: "vercel blob with an invalid auth", dsn: "vercelblob:?auth=iam"},
		{name: "vercel blob mixing credential modes", dsn: "vercelblob:?auth=oidc&tokenEnv=BLOB_READ_WRITE_TOKEN"},
		{name: "vercel blob with OIDC and no store", dsn: "vercelblob:?auth=oidc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.dsn, Options{})
			require.Error(t, err)
		})
	}
}

// The store ID of a Blob DSN that authenticates with OIDC has no default in the DSN itself, so the process supplies one
func TestNewAppliesTheDefaultVercelBlobStoreIDEnvironment(t *testing.T) {
	t.Setenv("PIXEL_TEST_ASSETS_STORE_ID", "store_assets")

	backend, err := New("vercelblob:?auth=oidc&access=public", Options{DefaultVercelBlobStoreIDEnv: "PIXEL_TEST_ASSETS_STORE_ID"})
	require.NoError(t, err)
	assert.IsType(t, &VercelBlobBackend{}, backend)

	// A DSN that names its own variable keeps it, since a process can talk to more than one store
	t.Setenv("PIXEL_TEST_CUSTOM_STORE_ID", "store_custom")
	backend, err = New("vercelblob:?auth=oidc&storeIdEnv=PIXEL_TEST_CUSTOM_STORE_ID&access=public", Options{DefaultVercelBlobStoreIDEnv: "PIXEL_TEST_ASSETS_STORE_ID"})
	require.NoError(t, err)
	blob, ok := backend.(*VercelBlobBackend)
	require.True(t, ok)
	assert.Equal(t, "custom", blob.storeID)

	// Without a default, and without one in the DSN, there is nothing to address the store with
	_, err = New("vercelblob:?auth=oidc&access=public", Options{})
	require.ErrorContains(t, err, "store ID")
}

func TestDSNHasPublicAddressing(t *testing.T) {
	tests := []struct {
		name   string
		dsn    string
		public bool
	}{
		{name: "filesystem", dsn: "fs:/tmp/pixel", public: false},
		{name: "private blob", dsn: "vercelblob:?token=fake&access=private", public: false},
		{name: "public blob", dsn: "vercelblob:?token=fake&access=public", public: true},
		{name: "public OIDC blob", dsn: "vercelblob:?auth=oidc&storeId=store_assets&access=public", public: true},
		{name: "private s3", dsn: "s3://key:secret@s3.us-east-1.amazonaws.com/bucket", public: false},
		{name: "public s3", dsn: "s3://key:secret@s3.us-east-1.amazonaws.com/bucket?publicUrl=https%3A%2F%2Fassets.example.test", public: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			public, err := DSNHasPublicAddressing(tt.dsn)
			require.NoError(t, err)
			assert.Equal(t, tt.public, public)
		})
	}

	// It answers from the DSN alone, so a store whose credentials aren't in the environment can still be validated
	t.Run("without the credentials in the environment", func(t *testing.T) {
		t.Setenv("BLOB_READ_WRITE_TOKEN", "")
		public, err := DSNHasPublicAddressing("vercelblob:?access=public")
		require.NoError(t, err)
		assert.True(t, public)
	})

	t.Run("errors", func(t *testing.T) {
		_, err := DSNHasPublicAddressing("")
		require.Error(t, err)

		_, err = DSNHasPublicAddressing("gcs://bucket")
		require.Error(t, err)
	})
}

func TestValidateKey(t *testing.T) {
	for _, key := range []string{"wf1/200x200.webp", "wf1.jpg"} {
		err := ValidateKey(key)
		require.NoError(t, err, key)
	}

	for _, key := range []string{"", "/absolute/key", "windows\\key", "../escape"} {
		err := ValidateKey(key)
		require.Error(t, err, key)
	}
}

func TestPrefixedKey(t *testing.T) {
	assert.Equal(t, "wf1.jpg", prefixedKey("", "wf1.jpg"))
	assert.Equal(t, "images/wf1.jpg", prefixedKey("images", "wf1.jpg"))
	assert.Equal(t, "images/wf1/200x200.webp", prefixedKey("images", "wf1/200x200.webp"))
}
