package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunKeepsInheritedSecuritySchemes(t *testing.T) {
	// The kept operation has no security of its own, so it inherits the document-level requirement
	const swagger = `{
		"swagger": "2.0",
		"info": {"title": "Test", "version": "1.0"},
		"securityDefinitions": {
			"apiKey": {"type": "apiKey", "name": "Authorization", "in": "header"},
			"internalKey": {"type": "apiKey", "name": "X-Internal", "in": "header"}
		},
		"security": [{"apiKey": []}],
		"paths": {
			"/api/items": {"get": {"responses": {"200": {"description": "OK"}}}},
			"/internal/debug": {"get": {"security": [{"internalKey": []}], "responses": {"200": {"description": "OK"}}}}
		}
	}`

	dir := t.TempDir()
	inPath := filepath.Join(dir, "swagger.json")
	jsonPath := filepath.Join(dir, "openapi.json")
	yamlPath := filepath.Join(dir, "openapi.yaml")
	require.NoError(t, os.WriteFile(inPath, []byte(swagger), 0o600))

	err := run(inPath, jsonPath, yamlPath, "/api", "", "")
	require.NoError(t, err)

	out, err := os.ReadFile(jsonPath)
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(out, &doc))
	assert.Equal(t, []string{"apiKey"}, securitySchemeNames(t, doc))
}

func TestRunKeepsComponentsReachedThroughOtherComponents(t *testing.T) {
	// Kept operations reach schemas only through reusable responses, parameters, and request bodies, and Result references Nested
	// The dropped path is the only user of DebugResponse and Debug
	const swagger = `{
		"swagger": "2.0",
		"info": {"title": "Test", "version": "1.0"},
		"consumes": ["application/json"],
		"produces": ["application/json"],
		"definitions": {
			"Result": {"type": "object", "properties": {"nested": {"$ref": "#/definitions/Nested"}}},
			"Nested": {"type": "object", "properties": {"id": {"type": "string"}}},
			"Item": {"type": "object", "properties": {"name": {"type": "string"}}},
			"Debug": {"type": "object", "properties": {"info": {"type": "string"}}}
		},
		"parameters": {
			"Limit": {"name": "limit", "in": "query", "type": "integer"},
			"ItemBody": {"name": "body", "in": "body", "required": true, "schema": {"$ref": "#/definitions/Item"}}
		},
		"responses": {
			"ResultResponse": {"description": "OK", "schema": {"$ref": "#/definitions/Result"}},
			"DebugResponse": {"description": "OK", "schema": {"$ref": "#/definitions/Debug"}}
		},
		"paths": {
			"/api/items": {
				"get": {"parameters": [{"$ref": "#/parameters/Limit"}], "responses": {"200": {"$ref": "#/responses/ResultResponse"}}},
				"post": {"parameters": [{"$ref": "#/parameters/ItemBody"}], "responses": {"204": {"description": "Created"}}}
			},
			"/internal/debug": {
				"get": {"responses": {"200": {"$ref": "#/responses/DebugResponse"}}}
			}
		}
	}`

	dir := t.TempDir()
	inPath := filepath.Join(dir, "swagger.json")
	jsonPath := filepath.Join(dir, "openapi.json")
	yamlPath := filepath.Join(dir, "openapi.yaml")
	require.NoError(t, os.WriteFile(inPath, []byte(swagger), 0o600))

	err := run(inPath, jsonPath, yamlPath, "/api", "", "")
	require.NoError(t, err)

	out, err := os.ReadFile(jsonPath)
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(out, &doc))
	assert.Equal(t, []string{"Item", "Nested", "Result"}, componentNames(t, doc, "schemas"))
	assert.Equal(t, []string{"ResultResponse"}, componentNames(t, doc, "responses"))
	assert.Equal(t, []string{"Limit"}, componentNames(t, doc, "parameters"))
	assert.Equal(t, []string{"ItemBody"}, componentNames(t, doc, "requestBodies"))
}

func TestFilterDocSecuritySchemes(t *testing.T) {
	tests := []struct {
		name        string
		docSecurity []any
		opSecurity  []any
		expected    []string
	}{
		{
			name:        "inherited document-level requirement",
			docSecurity: []any{map[string]any{"apiKey": []any{}}},
			expected:    []string{"apiKey"},
		},
		{
			name:       "operation-level requirement",
			opSecurity: []any{map[string]any{"bearer": []any{}}},
			expected:   []string{"bearer"},
		},
		{
			// The document-level requirement still references the scheme, even if this operation opts out
			name:        "operation opts out of the document-level requirement",
			docSecurity: []any{map[string]any{"apiKey": []any{}}},
			opSecurity:  []any{},
			expected:    []string{"apiKey"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			op := map[string]any{
				"responses": map[string]any{"200": map[string]any{"description": "OK"}},
			}
			if tc.opSecurity != nil {
				op["security"] = tc.opSecurity
			}

			doc := map[string]any{
				"openapi": "3.0.3",
				"info":    map[string]any{"title": "Test", "version": "1.0"},
				"paths": map[string]any{
					"/api/items": map[string]any{"get": op},
				},
				"components": map[string]any{
					"securitySchemes": map[string]any{
						"apiKey": map[string]any{"type": "apiKey", "name": "Authorization", "in": "header"},
						"bearer": map[string]any{"type": "http", "scheme": "bearer"},
					},
				},
			}
			if tc.docSecurity != nil {
				doc["security"] = tc.docSecurity
			}

			in, err := json.Marshal(doc)
			require.NoError(t, err)

			out, err := filterDoc(in, "/api", "", "")
			require.NoError(t, err)

			var filtered map[string]any
			require.NoError(t, json.Unmarshal(out, &filtered))
			assert.Equal(t, tc.expected, securitySchemeNames(t, filtered))
		})
	}
}

// securitySchemeNames returns the sorted names of the security schemes in an OpenAPI 3 document
func securitySchemeNames(t *testing.T, doc map[string]any) []string {
	t.Helper()

	return componentNames(t, doc, "securitySchemes")
}

// componentNames returns the sorted names of the components of the given type in an OpenAPI 3 document
func componentNames(t *testing.T, doc map[string]any, kind string) []string {
	t.Helper()

	components, ok := doc["components"].(map[string]any)
	require.True(t, ok, "document has no components")
	entries, ok := components[kind].(map[string]any)
	require.True(t, ok, "document has no %s", kind)

	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	slices.Sort(names)

	return names
}

func TestParseComponentRef(t *testing.T) {
	tests := []struct {
		ref      string
		kind     string
		name     string
		expectOK bool
	}{
		{ref: "#/components/schemas/Item", kind: "schemas", name: "Item", expectOK: true},
		{ref: "#/components/responses/ResultResponse", kind: "responses", name: "ResultResponse", expectOK: true},
		{ref: "#/components/schemas/Item/properties/name", kind: "schemas", name: "Item", expectOK: true},
		{ref: "#/components/schemas/a~1b~0c", kind: "schemas", name: "a/b~c", expectOK: true},
		{ref: "#/components/schemas/", expectOK: false},
		{ref: "#/definitions/Item", expectOK: false},
		{ref: "other.json#/components/schemas/Item", expectOK: false},
		{ref: "", expectOK: false},
	}

	for _, tc := range tests {
		t.Run(tc.ref, func(t *testing.T) {
			kind, name, ok := parseComponentRef(tc.ref)
			require.Equal(t, tc.expectOK, ok)
			assert.Equal(t, tc.kind, kind)
			assert.Equal(t, tc.name, name)
		})
	}
}
