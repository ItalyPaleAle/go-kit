// Package openapidoc serves the OpenAPI documents that each service embeds into its binary at build time
package openapidoc

import (
	"io/fs"
	"net/http"
	"strings"

	"sigs.k8s.io/yaml"
)

// Serve responds with one document from specs, selected by the "document" path value
//
// Only the minified JSON form of each document is embedded, so a YAML request reads the JSON source and converts it in place
// That keeps a single copy of every document in the binary instead of shipping a second, larger representation that most callers never fetch
// defaultDocument is used when the path value is empty, which lets a service expose an extension-free discovery URL; pass an empty string to 404 instead
func Serve(w http.ResponseWriter, r *http.Request, specs fs.FS, defaultDocument string) {
	document := r.PathValue("document")
	if document == "" {
		document = defaultDocument
	}

	// Map the requested representation back to the embedded JSON file and reject names outside the JSON and YAML document set
	var (
		source     string
		renderYAML bool
	)
	switch {
	case strings.HasSuffix(document, ".json"):
		source = document
	case strings.HasSuffix(document, ".yaml"):
		source = strings.TrimSuffix(document, ".yaml") + ".json"
		renderYAML = true
	default:
		http.NotFound(w, r)
		return
	}

	// Read from the build-time snapshot so deployments never depend on files beside the binary
	// fs.ReadFile rejects traversal and absolute paths, so an unexpected name can only miss
	data, err := fs.ReadFile(specs, source)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Rendering per request trades one conversion for not carrying the YAML copy in memory for the process lifetime
	contentType := "application/json"
	if renderYAML {
		contentType = "application/yaml"
		data, err = yaml.JSONToYAML(data)
		if err != nil {
			// The embedded document is valid JSON by construction, so this only fires if the build snapshot is corrupt
			http.Error(w, "Failed to render YAML document", http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(data) //nolint:gosec // The response contains a trusted OpenAPI document embedded at build time
}
