package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fileRefsTestConfig struct {
	Token   string            `yaml:"token"`
	Other   string            `yaml:"other"`
	List    []string          `yaml:"list"`
	Map     map[string]string `yaml:"map"`
	Timeout time.Duration     `yaml:"timeout"`
	Port    int               `yaml:"port"`
}

func TestLoadConfigFile_FileRefs(t *testing.T) {
	// The config file and the files it references are in a "conf" folder, while the working directory is its parent, to check that relative paths are resolved against the config file's directory
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)
	confDir := filepath.Join(tmpDir, "conf")

	files := map[string]string{ //nolint:gosec // These are fake secrets used as test fixtures
		"token.txt":            "s3cr3t\n",
		"crlf.txt":             "s3cr3t\r\n",
		"spaces.txt":           "  padded  \n",
		"multiline.txt":        "line1\nline2\n\n",
		"special.txt":          `quote " backslash \ hash # colon: dollar $HOME unicode ✓ tab` + "\t",
		"path with spaces.txt": "spaced",
		"duration.txt":         "90s\n",
		"empty.txt":            "",
		"secrets/nested.txt":   "nested",
		"secrets.env":          "TOKEN=from-env\nOTHER='single $quoted'\nURL=\"https://example.com/a?b=c#d\"\n",
		"env with spaces.env":  "TOKEN=spaced-env\n",
		"invalid.env":          "TOKEN=ok\nNOT VALID\n",
	}
	for name, content := range files {
		path := filepath.Join(confDir, name)
		err := os.MkdirAll(filepath.Dir(path), 0o700)
		require.NoError(t, err)
		err = os.WriteFile(path, []byte(content), 0o600)
		require.NoError(t, err)
	}

	tests := []struct {
		name string
		// Occurrences of "{{dir}}" are replaced with the directory of the config file
		yaml string
		want fileRefsTestConfig
		// If set, the error must contain all these strings
		wantErr []string
		// If set, the error must not contain this string
		errNotContains string
	}{
		{
			name: "file with relative path",
			yaml: "token: !file token.txt\n",
			want: fileRefsTestConfig{Token: "s3cr3t"},
		},
		{
			name: "file with absolute path",
			yaml: "token: !file {{dir}}/token.txt\n",
			want: fileRefsTestConfig{Token: "s3cr3t"},
		},
		{
			name: "file in a subfolder",
			yaml: "token: !file secrets/nested.txt\n",
			want: fileRefsTestConfig{Token: "nested"},
		},
		{
			name: "file with double-quoted path",
			yaml: `token: !file "token.txt"` + "\n",
			want: fileRefsTestConfig{Token: "s3cr3t"},
		},
		{
			name: "file with single-quoted path",
			yaml: "token: !file 'token.txt'\n",
			want: fileRefsTestConfig{Token: "s3cr3t"},
		},
		{
			name: "file with spaces in path",
			yaml: "token: !file path with spaces.txt\nother: !file \"path with spaces.txt\"\n",
			want: fileRefsTestConfig{Token: "spaced", Other: "spaced"},
		},
		{
			name: "file contents with trailing CRLF",
			yaml: "token: !file crlf.txt\n",
			want: fileRefsTestConfig{Token: "s3cr3t"},
		},
		{
			name: "file contents keep spaces",
			yaml: "token: !file spaces.txt\n",
			want: fileRefsTestConfig{Token: "  padded  "},
		},
		{
			name: "file contents with multiple lines",
			yaml: "token: !file multiline.txt\n",
			want: fileRefsTestConfig{Token: "line1\nline2"},
		},
		{
			name: "file contents with special characters",
			yaml: "token: !file special.txt\n",
			want: fileRefsTestConfig{Token: `quote " backslash \ hash # colon: dollar $HOME unicode ✓ tab` + "\t"},
		},
		{
			name: "empty file",
			yaml: "token: !file empty.txt\n",
			want: fileRefsTestConfig{Token: ""},
		},
		{
			name: "comment after the tagged value",
			yaml: "token: !file token.txt # comment\nother: x\n",
			want: fileRefsTestConfig{Token: "s3cr3t", Other: "x"},
		},
		{
			name: "flow sequence",
			yaml: "list: [a, !file token.txt, !envfile secrets.env TOKEN, b]\n",
			want: fileRefsTestConfig{List: []string{"a", "s3cr3t", "from-env", "b"}},
		},
		{
			name: "block sequence",
			yaml: "list:\n  - !file token.txt\n  - b\n",
			want: fileRefsTestConfig{List: []string{"s3cr3t", "b"}},
		},
		{
			name: "map values",
			yaml: "map:\n  k1: !file token.txt\n  k2: !envfile secrets.env OTHER\n",
			want: fileRefsTestConfig{Map: map[string]string{"k1": "s3cr3t", "k2": "single $quoted"}},
		},
		{
			name: "non-ASCII characters before the tag",
			yaml: "map:\n  clé✓: !file token.txt\n",
			want: fileRefsTestConfig{Map: map[string]string{"clé✓": "s3cr3t"}},
		},
		{
			name: "anchor and alias",
			yaml: "token: &tok !file token.txt\nother: *tok\n",
			want: fileRefsTestConfig{Token: "s3cr3t", Other: "s3cr3t"},
		},
		{
			name: "tag before anchor",
			yaml: "token: !file &tok token.txt\nother: *tok\n",
			want: fileRefsTestConfig{Token: "s3cr3t", Other: "s3cr3t"},
		},
		{
			name: "types decoded from strings",
			yaml: "timeout: !file duration.txt\n",
			want: fileRefsTestConfig{Timeout: 90 * time.Second},
		},
		{
			name: "CRLF line endings in config file",
			yaml: "token: !file token.txt\r\nother: x\r\n",
			want: fileRefsTestConfig{Token: "s3cr3t", Other: "x"},
		},
		{
			name: "byte order mark in config file",
			yaml: "\ufefftoken: !file token.txt\n",
			want: fileRefsTestConfig{Token: "s3cr3t"},
		},
		{
			name: "envfile",
			yaml: "token: !envfile secrets.env TOKEN\nother: !envfile secrets.env URL\n",
			want: fileRefsTestConfig{Token: "from-env", Other: "https://example.com/a?b=c#d"},
		},
		{
			name: "envfile with absolute path",
			yaml: "token: !envfile {{dir}}/secrets.env TOKEN\n",
			want: fileRefsTestConfig{Token: "from-env"},
		},
		{
			name: "envfile with spaces in path",
			yaml: "token: !envfile env with spaces.env TOKEN\n",
			want: fileRefsTestConfig{Token: "spaced-env"},
		},
		{
			name: "envfile with quoted value",
			yaml: "token: !envfile \"env with spaces.env   TOKEN\"\nother: !envfile 'secrets.env TOKEN'\n",
			want: fileRefsTestConfig{Token: "spaced-env", Other: "from-env"},
		},
		{
			name:    "file does not exist",
			yaml:    "other: x\ntoken: !file missing.txt\n",
			wantErr: []string{"failed to resolve file references in config file", "line 2: failed to read file referenced by !file", "missing.txt"},
		},
		{
			name:    "file without path",
			yaml:    `token: !file ""` + "\n",
			wantErr: []string{"line 1: !file requires a file path"},
		},
		{
			name:    "envfile does not exist",
			yaml:    "token: !envfile missing.env TOKEN\n",
			wantErr: []string{"line 1: failed to read env file referenced by !envfile", "missing.env"},
		},
		{
			name:           "envfile variable does not exist",
			yaml:           "token: !envfile secrets.env NOPE\n",
			wantErr:        []string{"line 1: variable 'NOPE' not found in env file", "secrets.env"},
			errNotContains: "from-env",
		},
		{
			name:    "envfile without variable name",
			yaml:    "token: !envfile secrets.env\n",
			wantErr: []string{"line 1: !envfile requires a file path and a variable name, separated by a space"},
		},
		{
			name:           "envfile with invalid syntax",
			yaml:           "token: !envfile invalid.env TOKEN\n",
			wantErr:        []string{"line 1: failed to parse env file", "invalid.env", "invalid line 2: expected KEY=VALUE"},
			errNotContains: "NOT VALID",
		},
		{
			name:    "value on the next line",
			yaml:    "token: !file\n  token.txt\n",
			wantErr: []string{"line 1: the value tagged with !file must be on the same line as the tag"},
		},
		{
			name:    "plain value continuing on the next line",
			yaml:    "token: !file token\n  .txt\n",
			wantErr: []string{"line 1: the value tagged with !file must be on the same line as the tag"},
		},
		{
			name:    "quoted value continuing on the next line",
			yaml:    "token: !envfile \"secrets.env\n  TOKEN\"\n",
			wantErr: []string{"line 1: the value tagged with !envfile must be on the same line as the tag"},
		},
		{
			name:    "block scalar",
			yaml:    "token: !file |\n  token.txt\n",
			wantErr: []string{"line 1: the value tagged with !file must be on the same line as the tag"},
		},
		{
			name:    "tag on a mapping",
			yaml:    "map: !file\n  k: v\n",
			wantErr: []string{"line 1: !file can only be used on a string value"},
		},
		{
			name:    "tag on a sequence",
			yaml:    "list: !envfile [a, b]\n",
			wantErr: []string{"line 1: !envfile can only be used on a string value"},
		},
		{
			name:    "non-string field",
			yaml:    "port: !file token.txt\n",
			wantErr: []string{"failed to decode config file", "line 1: cannot unmarshal !!str"},
		},
		{
			name:    "unknown fields are still rejected with the original line numbers",
			yaml:    "# comment\n\ntoken: !file token.txt\n\nother: !envfile secrets.env TOKEN\nnope: 1\n",
			wantErr: []string{"failed to decode config file", "line 6: field nope not found"},
		},
		{
			name:    "syntax errors are reported by the decoder",
			yaml:    "token: !file token.txt\nlist: [a\n",
			wantErr: []string{"failed to decode config file"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			configPath := filepath.Join(confDir, "config.yaml")
			err := os.WriteFile(configPath, []byte(strings.ReplaceAll(tc.yaml, "{{dir}}", confDir)), 0o600)
			require.NoError(t, err)

			cfg := fileRefsTestConfig{}
			err = loadConfigFile(&cfg, filepath.Join("conf", "config.yaml"))
			if len(tc.wantErr) > 0 {
				require.Error(t, err)
				for _, msg := range tc.wantErr {
					require.ErrorContains(t, err, msg)
				}
				if tc.errNotContains != "" {
					assert.NotContains(t, err.Error(), tc.errNotContains)
				}
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.want, cfg)
		})
	}
}

func TestResolveFileRefs_NoTags(t *testing.T) {
	// Documents without tags are returned unchanged, without being parsed
	data := []byte("foo: bar\nbaz: [\n")
	got, err := resolveFileRefs(data, t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, data, got)
}

func TestResolveFileRefs_PreservesLines(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("multi\nline\nsecret\n"), 0o600)
	require.NoError(t, err)

	data := []byte("a: 1\n\nb: !file secret.txt # comment\nc: [x, !file 'secret.txt']\n")
	got, err := resolveFileRefs(data, dir)
	require.NoError(t, err)
	assert.Equal(t, "a: 1\n\nb: \"multi\\nline\\nsecret\" # comment\nc: [x, \"multi\\nline\\nsecret\"]\n", string(got))
}
