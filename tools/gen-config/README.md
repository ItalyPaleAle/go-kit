# gen-config

This utility updates the [`config.sample.yaml`](../../config.sample.yaml) file based on the `Config` struct defined in [`pkg/config/config.go`](../../pkg/config/config.go).

Additionally, it writes all configuration options in the (Git-ignored) file `config.md` and updates the section in the [`README.md`](../../README.md) between `<!-- BEGIN CONFIG TABLE -->` and `<!-- END CONFIG TABLE -->`.

Usage

```sh
Usage: gen-config [options]

Generate sample YAML and markdown docs from a config struct.

Options:
  -docs string
        Optional docs file path to update between config table markers
  -md string
        Destination path for generated markdown documentation (default "config.md")
  -struct string
        Path to config struct file or directory containing config.go (default "pkg/config/config.go")
  -yaml string
        Destination path for generated sample YAML (default "config.sample.yaml")
```
