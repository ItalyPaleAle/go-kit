package openapidoc

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Only the JSON form is embedded, so the fixture mirrors what a service binary actually carries
var testSpecs = fstest.MapFS{
	"example.json": &fstest.MapFile{Data: []byte(`{"openapi":"3.0.3","info":{"title":"Example","version":"1.0"}}`)},
	"broken.json":  &fstest.MapFile{Data: []byte(`{"openapi":`)},
}

// serve issues a request through Serve with document supplied as the path value, matching how the services route it
func serve(t *testing.T, document string, defaultDocument string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/.openapi/"+document, nil)
	req.SetPathValue("document", document)
	rec := httptest.NewRecorder()
	Serve(rec, req, testSpecs, defaultDocument)
	return rec
}

func TestServeJSONReturnsEmbeddedDocumentVerbatim(t *testing.T) {
	rec := serve(t, "example.json", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.JSONEq(t, `{"openapi":"3.0.3","info":{"title":"Example","version":"1.0"}}`, rec.Body.String())
}

// The YAML representation is not embedded, so this asserts it is produced from the JSON source at request time
func TestServeYAMLRendersFromJSONSource(t *testing.T) {
	rec := serve(t, "example.yaml", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/yaml", rec.Header().Get("Content-Type"))
	assert.Equal(t, "info:\n  title: Example\n  version: \"1.0\"\nopenapi: 3.0.3\n", rec.Body.String())
}

func TestServeDefaultDocument(t *testing.T) {
	t.Run("empty path value falls back to the default", func(t *testing.T) {
		rec := serve(t, "", "example.json")

		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	})

	// Services without a canonical document pass an empty default and must not expose a discovery URL
	t.Run("empty path value without a default is a 404", func(t *testing.T) {
		rec := serve(t, "", "")

		assert.Equal(t, http.StatusNotFound, rec.Code)
	})
}

func TestServeRejectsUnknownDocuments(t *testing.T) {
	tests := map[string]string{
		"unknown name":              "missing.json",
		"unknown name as yaml":      "missing.yaml",
		"unsupported extension":     "example.txt",
		"no extension":              "example",
		"bare yaml extension":       ".yaml",
		"traversal outside the FS":  "../example.json",
		"absolute path":             "/example.json",
		"embed.go from the package": "embed.go",
	}

	for name, document := range tests {
		t.Run(name, func(t *testing.T) {
			rec := serve(t, document, "")

			assert.Equal(t, http.StatusNotFound, rec.Code)
		})
	}
}

// A document that is not valid JSON can only come from a corrupt build snapshot, and must surface as a server error rather than a partial body
func TestServeYAMLFailsOnInvalidJSON(t *testing.T) {
	rec := serve(t, "broken.yaml", "")

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}
