# openapi-convert

This utility converts a Swagger 2.0 JSON document into OpenAPI 3 output.

It writes both JSON and YAML outputs, validates the converted document, and can optionally filter paths by prefix.
When path filtering is enabled, it also prunes unreferenced component schemas and unreferenced security schemes from the result.

Usage

```sh
Usage: openapi-convert [options]

Convert Swagger 2.0 JSON to OpenAPI 3 JSON and YAML.

Options:
  -description string
        override info.description in the output doc
  -in string
        path to Swagger 2.0 JSON input
  -json string
        path to OpenAPI 3 JSON output
  -path-prefix string
        if set, only include paths with this prefix
  -title string
        override info.title in the output doc
  -yaml string
        path to OpenAPI 3 YAML output
```
