package config

import (
	"errors"
	"fmt"
	"strings"
)

// parseDotenv parses the contents of a dotenv file
// Each line is in the format `KEY=value`, optionally prefixed with `export`, and lines starting with `#` are comments
// Values can be unquoted, single-quoted (taken literally), or double-quoted (supporting the escape sequences \n, \r, \t, \" and \\), and must be on a single line
// Unlike other dotenv parsers, references such as `$NAME` are never expanded, so secrets that contain `$` are read as they are
func parseDotenv(data []byte) (map[string]string, error) {
	vars := map[string]string{}
	lineNum := 0
	for line := range strings.Lines(string(data)) {
		lineNum++

		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}

		// Allow lines copied from shell scripts
		rest, found := strings.CutPrefix(line, "export")
		if found && rest != "" && (rest[0] == ' ' || rest[0] == '\t') {
			line = rest
		}

		key, raw, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" || strings.ContainsAny(key, " \t") {
			return nil, fmt.Errorf("invalid line %d: expected KEY=VALUE", lineNum)
		}

		value, err := parseDotenvValue(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid line %d: %w", lineNum, err)
		}

		vars[key] = value
	}

	return vars, nil
}

// parseDotenvValue parses the part of a dotenv line after the "=" sign
func parseDotenvValue(raw string) (string, error) {
	trimmed := strings.TrimLeft(raw, " \t")
	if trimmed == "" {
		return "", nil
	}

	switch trimmed[0] {
	case '\'':
		end := strings.IndexByte(trimmed[1:], '\'')
		if end < 0 {
			return "", errors.New("missing closing quote")
		}
		err := checkAfterClosingQuote(trimmed[end+2:])
		if err != nil {
			return "", err
		}
		return trimmed[1 : end+1], nil

	case '"':
		var sb strings.Builder
		for i := 1; i < len(trimmed); i++ {
			c := trimmed[i]
			switch {
			case c == '\\' && i+1 < len(trimmed):
				i++
				switch trimmed[i] {
				case 'n':
					sb.WriteByte('\n')
				case 'r':
					sb.WriteByte('\r')
				case 't':
					sb.WriteByte('\t')
				case '"', '\\':
					sb.WriteByte(trimmed[i])
				default:
					// Keep unknown escape sequences as they are, so values such as Windows paths are not mangled
					sb.WriteByte('\\')
					sb.WriteByte(trimmed[i])
				}
			case c == '"':
				err := checkAfterClosingQuote(trimmed[i+1:])
				if err != nil {
					return "", err
				}
				return sb.String(), nil
			default:
				sb.WriteByte(c)
			}
		}
		return "", errors.New("missing closing quote")

	default:
		// In unquoted values, a "#" starts a comment only when it follows whitespace, so values such as "abc#123" are kept whole
		// This uses the untrimmed value so "KEY= # comment" is an empty value
		for i := 1; i < len(raw); i++ {
			if raw[i] == '#' && (raw[i-1] == ' ' || raw[i-1] == '\t') {
				raw = raw[:i]
				break
			}
		}
		return strings.TrimSpace(raw), nil
	}
}

// checkAfterClosingQuote returns an error if there's anything other than whitespace or a comment after the closing quote of a value
func checkAfterClosingQuote(rest string) error {
	rest = strings.TrimSpace(rest)
	if rest != "" && rest[0] != '#' {
		return errors.New("unexpected characters after the closing quote")
	}
	return nil
}
