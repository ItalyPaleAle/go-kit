package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateFromStructValidatesDocsMarkersBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "config.go")
	err := os.WriteFile(srcPath, []byte(`package config

type Config struct {
	// Port to listen on
	// +default 8080
	Port int `+"`yaml:\"port\"`"+`
}
`), 0o600)
	require.NoError(t, err)

	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{
			name:    "missing begin marker",
			content: "# Docs\n<!-- END CONFIG TABLE -->\nFooter\n",
			wantErr: "missing \"<!-- BEGIN CONFIG TABLE -->\" marker",
		},
		{
			name:    "missing end marker",
			content: "# Docs\n<!-- BEGIN CONFIG TABLE -->\nFooter\n",
			wantErr: "missing \"<!-- END CONFIG TABLE -->\" marker",
		},
		{
			name:    "inverted markers",
			content: "# Docs\n<!-- END CONFIG TABLE -->\n<!-- BEGIN CONFIG TABLE -->\n",
			wantErr: "appears before",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			subDir := t.TempDir()
			docsPath := filepath.Join(subDir, "README.md")
			wErr := os.WriteFile(docsPath, []byte(tc.content), 0o600)
			require.NoError(t, wErr)

			genErr := generateFromStruct(
				srcPath,
				filepath.Join(subDir, "config.sample.yaml"),
				filepath.Join(subDir, "config.md"),
				docsPath,
			)
			require.Error(t, genErr)
			require.ErrorContains(t, genErr, tc.wantErr)

			// The existing docs file must remain untouched when markers are invalid
			got, rErr := os.ReadFile(docsPath) //nolint:gosec // Test-created path
			require.NoError(t, rErr)
			assert.Equal(t, tc.content, string(got))
		})
	}

	t.Run("replaces table between valid markers", func(t *testing.T) {
		subDir := t.TempDir()
		docsPath := filepath.Join(subDir, "README.md")
		initial := "# Header\n<!-- BEGIN CONFIG TABLE -->\nold\n<!-- END CONFIG TABLE -->\nFooter\n"
		wErr := os.WriteFile(docsPath, []byte(initial), 0o600)
		require.NoError(t, wErr)

		genErr := generateFromStruct(
			srcPath,
			filepath.Join(subDir, "config.sample.yaml"),
			filepath.Join(subDir, "config.md"),
			docsPath,
		)
		require.NoError(t, genErr)

		got, rErr := os.ReadFile(docsPath) //nolint:gosec // Test-created path
		require.NoError(t, rErr)
		assert.Contains(t, string(got), "# Header\n<!-- BEGIN CONFIG TABLE -->\n")
		assert.Contains(t, string(got), "`port`")
		assert.Contains(t, string(got), "\n<!-- END CONFIG TABLE -->\nFooter\n")
	})
}
