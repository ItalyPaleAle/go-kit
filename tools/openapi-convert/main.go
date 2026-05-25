package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/getkin/kin-openapi/openapi2"
	"github.com/getkin/kin-openapi/openapi2conv"
	"github.com/getkin/kin-openapi/openapi3"
	"sigs.k8s.io/yaml"
)

func main() {
	inPath := flag.String("in", "", "path to Swagger 2.0 JSON input")
	jsonPath := flag.String("json", "", "path to OpenAPI 3 JSON output")
	yamlPath := flag.String("yaml", "", "path to OpenAPI 3 YAML output")
	pathPrefix := flag.String("path-prefix", "", "if set, only include paths with this prefix")
	title := flag.String("title", "", "override info.title in the output doc")
	description := flag.String("description", "", "override info.description in the output doc")
	flag.Parse()

	err := run(*inPath, *jsonPath, *yamlPath, *pathPrefix, *title, *description)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "openapi-convert: %v\n", err)
		os.Exit(1)
	}
}

func run(inPath, jsonPath, yamlPath, pathPrefix, title, description string) error {
	if inPath == "" {
		return errors.New("missing -in")
	}
	if jsonPath == "" {
		return errors.New("missing -json")
	}
	if yamlPath == "" {
		return errors.New("missing -yaml")
	}

	// #nosec G304 - This generator is invoked by Make with repo-controlled paths
	in, err := os.ReadFile(inPath)
	if err != nil {
		return fmt.Errorf("read swagger input: %w", err)
	}

	var doc2 openapi2.T
	err = json.Unmarshal(in, &doc2)
	if err != nil {
		return fmt.Errorf("decode swagger input: %w", err)
	}

	doc3, err := openapi2conv.ToV3(&doc2)
	if err != nil {
		return fmt.Errorf("convert to openapi v3: %w", err)
	}
	if doc3.OpenAPI == "" {
		doc3.OpenAPI = "3.0.3"
	}

	err = doc3.Validate(context.Background())
	if err != nil {
		return fmt.Errorf("validate openapi v3 document: %w", err)
	}

	outJSON, err := json.MarshalIndent(doc3, "", "  ")
	if err != nil {
		return fmt.Errorf("encode openapi json: %w", err)
	}
	outJSON = append(outJSON, '\n')

	// Filter to a subset of paths when a prefix is given
	if pathPrefix != "" {
		outJSON, err = filterDoc(outJSON, pathPrefix, title, description)
		if err != nil {
			return fmt.Errorf("filter doc: %w", err)
		}
	}

	outYAML, err := yaml.JSONToYAML(outJSON)
	if err != nil {
		return fmt.Errorf("encode openapi yaml: %w", err)
	}

	err = writeFile(jsonPath, outJSON)
	if err != nil {
		return err
	}
	err = writeFile(yamlPath, outYAML)
	if err != nil {
		return err
	}

	loader := openapi3.NewLoader()
	loaded, err := loader.LoadFromData(outJSON)
	if err != nil {
		return fmt.Errorf("reload openapi json: %w", err)
	}
	err = loaded.Validate(context.Background())
	if err != nil {
		return fmt.Errorf("validate written openapi json: %w", err)
	}

	return nil
}

// schemaRefRe matches $ref values pointing into #/components/schemas/
var schemaRefRe = regexp.MustCompile(`"\$ref":\s*"#/components/schemas/([^"]+)"`)

// filterDoc returns a copy of docJSON with only paths that start with prefix, unreferenced component schemas pruned, unreferenced security schemes pruned, and info.title / info.description overridden when non-empty
func filterDoc(docJSON []byte, prefix, title, description string) ([]byte, error) {
	var doc map[string]any
	err := json.Unmarshal(docJSON, &doc)
	if err != nil {
		return nil, fmt.Errorf("unmarshal doc: %w", err)
	}

	// Override info fields
	if info, ok := doc["info"].(map[string]any); ok {
		if title != "" {
			info["title"] = title
		}
		if description != "" {
			info["description"] = description
		}
	}

	// Filter paths to those matching the prefix
	paths, _ := doc["paths"].(map[string]any)
	filteredPaths := make(map[string]any)
	for path, item := range paths {
		if strings.HasPrefix(path, prefix) {
			filteredPaths[path] = item
		}
	}
	doc["paths"] = filteredPaths

	// Re-serialize so the ref scanners operate on the final path set
	filteredJSON, err := json.Marshal(filteredPaths)
	if err != nil {
		return nil, fmt.Errorf("marshal filtered paths: %w", err)
	}

	// Collect all component schema names referenced by the kept paths, then expand transitively through the schemas themselves
	components, _ := doc["components"].(map[string]any)
	if components != nil {
		err = pruneSchemas(components, filteredJSON)
		if err != nil {
			return nil, err
		}

		pruneSecuritySchemes(components, filteredPaths)
	}

	result, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal result: %w", err)
	}
	result = append(result, '\n')

	return result, nil
}

// collectSchemaRefs returns the set of schema names referenced via $ref in src.
func collectSchemaRefs(src []byte) map[string]bool {
	refs := make(map[string]bool)
	for _, match := range schemaRefRe.FindAllSubmatch(src, -1) {
		refs[string(match[1])] = true
	}
	return refs
}

// pruneSchemas removes schemas from components that are not reachable from the paths JSON, resolving transitive references inside schemas themselves
func pruneSchemas(components map[string]any, pathsJSON []byte) error {
	schemas, _ := components["schemas"].(map[string]any)
	if schemas == nil {
		return nil
	}

	// Seed the reachable set from the paths
	reachable := collectSchemaRefs(pathsJSON)

	// Expand transitively: any schema referenced by a reachable schema is also reachable
	changed := true
	for changed {
		changed = false
		for name := range reachable {
			schema, ok := schemas[name]
			if !ok {
				continue
			}
			schemaJSON, err := json.Marshal(schema)
			if err != nil {
				return fmt.Errorf("marshal schema %s: %w", name, err)
			}
			for ref := range collectSchemaRefs(schemaJSON) {
				if !reachable[ref] {
					reachable[ref] = true
					changed = true
				}
			}
		}
	}

	for name := range schemas {
		if !reachable[name] {
			delete(schemas, name)
		}
	}

	return nil
}

// pruneSecuritySchemes removes security scheme entries that are not used by any operation in filteredPaths
func pruneSecuritySchemes(components map[string]any, filteredPaths map[string]any) {
	secSchemes, _ := components["securitySchemes"].(map[string]any)
	if secSchemes == nil {
		return
	}

	used := make(map[string]bool)
	collectUsedSecuritySchemes(filteredPaths, used)

	for name := range secSchemes {
		if !used[name] {
			delete(secSchemes, name)
		}
	}
}

// collectUsedSecuritySchemes walks v recursively and collects all security scheme names that appear as keys in "security" array entries
func collectUsedSecuritySchemes(v any, used map[string]bool) {
	switch t := v.(type) {
	case map[string]any:
		if sec, ok := t["security"]; ok {
			if secArr, ok := sec.([]any); ok {
				for _, entry := range secArr {
					if entryMap, ok := entry.(map[string]any); ok {
						for k := range entryMap {
							used[k] = true
						}
					}
				}
			}
		}
		for _, val := range t {
			collectUsedSecuritySchemes(val, used)
		}
	case []any:
		for _, item := range t {
			collectUsedSecuritySchemes(item, used)
		}
	}
}

func writeFile(path string, data []byte) error {
	err := os.MkdirAll(filepath.Dir(path), 0o750)
	if err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	err = os.WriteFile(path, data, 0o600)
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	return nil
}
