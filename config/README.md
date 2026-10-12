# config

Utilities for loading YAML configuration files.

`LoadConfig` reads the config file from the path in an environment variable, or else looks for `config.yaml` (or `config.yml`) in the current folder, `~/.<app>`, and `/etc/<app>`. Unknown keys are rejected.

## Reading values from files

String values in the config file can be read from other files, so secrets can stay out of the config file itself. This works with secrets mounted as files, such as Kubernetes Secret volumes, Docker secrets, and systemd credentials.

```yaml
providers:
  main:
    cloudflare:
      # Reads the whole file
      apiToken: !file /run/secrets/cloudflare_token
      # Reads the variable CF_ZONE_ID from a dotenv file
      zoneId: !envfile secrets.env CF_ZONE_ID
```

- `!file <path>` reads the whole file. Trailing newlines are removed, but other whitespace is kept.
- `!envfile <path> <name>` reads the variable `<name>` from a dotenv file. The variable name is what follows the last space.
- Relative paths are resolved against the **folder that contains the config file**, not the working directory.
- Quoting the whole value is optional, for example `!file "path to file.txt"` or `!envfile "path to file.env NAME"`. As with any YAML string, quotes are needed when a path contains characters such as ` #` or `: `.
- The tag and its value must be on the same line.
- Values are always strings. They can be used for string fields and for types that are decoded from strings, such as `time.Duration`, but not for numbers or booleans.
- Loading the config fails if a file or a variable doesn't exist. Error messages include the path and the variable name, but never the contents of the files.

Dotenv files contain one `KEY=value` per line, optionally prefixed with `export`. Lines starting with `#` are comments, and so is anything after a ` #` in an unquoted value. Values can be:

- Unquoted: surrounding whitespace is removed
- Single-quoted: taken literally
- Double-quoted: supporting the escape sequences `\n`, `\r`, `\t`, `\"`, and `\\`

Unlike other dotenv parsers, references to other variables such as `$NAME` are never expanded, so values containing `$` are read as they are. Values must be on a single line: for values with multiple lines, such as certificates, use `!file` or `\n` in a double-quoted value.

```sh
# Comment
CF_ZONE_ID=abc123
export OVH_API_KEY='pa$$word'
OVH_API_SECRET="line1\nline2"
```
