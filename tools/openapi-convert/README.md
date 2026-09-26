# openapi-convert

This utility converts a Swagger 2.0 JSON document into OpenAPI 3 output.

It writes both JSON and YAML outputs, validates the converted document, and can optionally filter paths by prefix.
When path filtering is enabled, it also prunes the components (schemas, responses, parameters, and so on) that the kept paths don't reference, directly or through other components, and the security schemes that neither the kept operations nor the document-level security requirements use.
The output is validated before it's written, so a failed run leaves existing output files untouched.

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
