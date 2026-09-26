package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
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

	// Validate the final document before writing anything, so a failure leaves existing outputs untouched
	loaded, err := openapi3.NewLoader().LoadFromData(outJSON)
	if err != nil {
		return fmt.Errorf("reload openapi json: %w", err)
	}
	err = loaded.Validate(context.Background())
	if err != nil {
		return fmt.Errorf("validate output openapi json: %w", err)
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

	return nil
}

// componentRef identifies an entry in the document's components, such as a schema or a response
type componentRef struct {
	kind string
	name string
}

// filterDoc returns a copy of docJSON with only paths that start with prefix, unreferenced components and security schemes pruned, and info.title / info.description overridden when non-empty
func filterDoc(docJSON []byte, prefix, title, description string) ([]byte, error) {
	var doc map[string]any
	err := json.Unmarshal(docJSON, &doc)
	if err != nil {
		return nil, fmt.Errorf("unmarshal doc: %w", err)
	}

	// Override info fields
	info, ok := doc["info"].(map[string]any)
	if ok {
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

	// Prune the components that the kept paths don't use
	components, _ := doc["components"].(map[string]any)
	if components != nil {
		pruneComponents(components, filteredPaths)
		pruneSecuritySchemes(components, filteredPaths, doc["security"])
	}

	result, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal result: %w", err)
	}
	result = append(result, '\n')

	return result, nil
}

// pruneComponents removes the components that the kept paths don't reference, directly or through other components
// References are followed across every component type: for example, a kept operation can reference a response, whose content references a schema
// Security schemes are not referenced via $ref, so pruneSecuritySchemes handles them instead
func pruneComponents(components map[string]any, filteredPaths map[string]any) {
	// Seed the reachable set from the paths, then expand it transitively
	reachable := make(map[componentRef]bool)
	collectComponentRefs(filteredPaths, reachable)

	queue := slices.Collect(maps.Keys(reachable))
	for len(queue) > 0 {
		ref := queue[len(queue)-1]
		queue = queue[:len(queue)-1]

		entries, _ := components[ref.kind].(map[string]any)
		found := make(map[componentRef]bool)
		collectComponentRefs(entries[ref.name], found)
		for r := range found {
			if !reachable[r] {
				reachable[r] = true
				queue = append(queue, r)
			}
		}
	}

	for kind, v := range components {
		// Extensions are not component maps
		if kind == "securitySchemes" || strings.HasPrefix(kind, "x-") {
			continue
		}

		entries, _ := v.(map[string]any)
		for name := range entries {
			if !reachable[componentRef{kind: kind, name: name}] {
				delete(entries, name)
			}
		}
	}
}

// collectComponentRefs walks v recursively and adds every component referenced via $ref to refs
func collectComponentRefs(v any, refs map[componentRef]bool) {
	switch t := v.(type) {
	case map[string]any:
		ref, _ := t["$ref"].(string)
		kind, name, ok := parseComponentRef(ref)
		if ok {
			refs[componentRef{kind: kind, name: name}] = true
		}

		for _, val := range t {
			collectComponentRefs(val, refs)
		}
	case []any:
		for _, item := range t {
			collectComponentRefs(item, refs)
		}
	}
}

// parseComponentRef returns the component type and name from a local reference such as "#/components/schemas/Name"
// For a reference into a component, such as "#/components/schemas/Name/properties/id", it returns the component that contains it
func parseComponentRef(ref string) (kind string, name string, ok bool) {
	rest, ok := strings.CutPrefix(ref, "#/components/")
	if !ok {
		return "", "", false
	}

	kind, rest, _ = strings.Cut(rest, "/")
	name, _, _ = strings.Cut(rest, "/")
	if kind == "" || name == "" {
		return "", "", false
	}

	// Reference tokens are JSON pointers, where "~1" stands for "/" and "~0" for "~"
	name = strings.ReplaceAll(name, "~1", "/")
	name = strings.ReplaceAll(name, "~0", "~")

	return kind, name, true
}

// pruneSecuritySchemes removes security scheme entries that are not used by the document-level security requirements or by any operation in filteredPaths
func pruneSecuritySchemes(components map[string]any, filteredPaths map[string]any, docSecurity any) {
	secSchemes, _ := components["securitySchemes"].(map[string]any)
	if secSchemes == nil {
		return
	}

	// Operations without their own security requirements inherit the document-level ones, which stay in the output
	used := make(map[string]bool)
	collectUsedSecuritySchemes(map[string]any{"security": docSecurity}, used)
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
