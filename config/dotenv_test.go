package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDotenv(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    map[string]string
		wantErr string
	}{
		{
			name:  "empty file",
			input: "",
			want:  map[string]string{},
		},
		{
			name:  "unquoted values",
			input: "A=1\nB=two words\n",
			want:  map[string]string{"A": "1", "B": "two words"},
		},
		{
			name:  "comments and blank lines",
			input: "# comment\n\n   # indented comment\nA=1\n\n",
			want:  map[string]string{"A": "1"},
		},
		{
			name:  "export prefix",
			input: "export A=1\nexport\tB=2\n",
			want:  map[string]string{"A": "1", "B": "2"},
		},
		{
			name:  "variable named export",
			input: "export=1\n",
			want:  map[string]string{"export": "1"},
		},
		{
			name:  "whitespace around key and value",
			input: "  A  =  1  \n",
			want:  map[string]string{"A": "1"},
		},
		{
			name:  "empty values",
			input: "A=\nB=   \nC=''\nD=\"\"\n",
			want:  map[string]string{"A": "", "B": "", "C": "", "D": ""},
		},
		{
			name:  "inline comments in unquoted values",
			input: "A=abc # comment\nB=abc#123\nC= # comment\nD=#abc\nE=abc\t# comment\n",
			want:  map[string]string{"A": "abc", "B": "abc#123", "C": "", "D": "#abc", "E": "abc"},
		},
		{
			name:  "single-quoted values are literal",
			input: `A='a $B \n "x" # y '` + "\n",
			want:  map[string]string{"A": `a $B \n "x" # y `},
		},
		{
			name:  "double-quoted values with escapes",
			input: `A="line1\nline2\t\"q\" \\ \r C:\path # not a comment"` + "\n",
			want:  map[string]string{"A": "line1\nline2\t\"q\" \\ \r C:\\path # not a comment"},
		},
		{
			name:  "comments after quoted values",
			input: "A=\"x\" # comment\nB='y'   # comment\nC='z'#comment\n",
			want:  map[string]string{"A": "x", "B": "y", "C": "z"},
		},
		{
			name:  "variables are not expanded",
			input: "A=1\nB=$A\nC=${A}\nD=\"$A\"\nE=pa$word\n",
			want:  map[string]string{"A": "1", "B": "$A", "C": "${A}", "D": "$A", "E": "pa$word"},
		},
		{
			name:  "equals signs in value",
			input: "A=a=b==\n",
			want:  map[string]string{"A": "a=b=="},
		},
		{
			name:  "CRLF line endings",
			input: "A=1\r\nB=\"2\"\r\nC='3'\r\n",
			want:  map[string]string{"A": "1", "B": "2", "C": "3"},
		},
		{
			name:  "last value wins",
			input: "A=1\nA=2\n",
			want:  map[string]string{"A": "2"},
		},
		{
			name:  "no trailing newline",
			input: "A=1",
			want:  map[string]string{"A": "1"},
		},
		{
			name:    "missing equals sign",
			input:   "A=1\nB\n",
			wantErr: "invalid line 2: expected KEY=VALUE",
		},
		{
			name:    "empty key",
			input:   "=1\n",
			wantErr: "invalid line 1: expected KEY=VALUE",
		},
		{
			name:    "key with spaces",
			input:   "A B=1\n",
			wantErr: "invalid line 1: expected KEY=VALUE",
		},
		{
			name:    "unterminated double quote",
			input:   "A=1\nB=\"abc\nC=2\n",
			wantErr: "invalid line 2: missing closing quote",
		},
		{
			name:    "unterminated single quote",
			input:   "A='abc\n",
			wantErr: "invalid line 1: missing closing quote",
		},
		{
			name:    "escaped closing quote",
			input:   `A="abc\"` + "\n",
			wantErr: "invalid line 1: missing closing quote",
		},
		{
			name:    "text after closing quote",
			input:   `A="abc"def` + "\n",
			wantErr: "invalid line 1: unexpected characters after the closing quote",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDotenv([]byte(tc.input))
			if tc.wantErr != "" {
				require.Error(t, err)
				require.ErrorContains(t, err, tc.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
