package config

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	yaml "sigs.k8s.io/yaml/goyaml.v3"
)

const (
	// Tag for values read from a file, as in `!file <path>`
	tagFile = "!file"
	// Tag for values read from a variable in a dotenv file, as in `!envfile <path> <name>`
	tagEnvFile = "!envfile"

	// UTF-8 byte order mark, which can appear at the start of a document
	utf8BOM = "\ufeff"
)

// fileRefResolver replaces values tagged with !file or !envfile in a YAML document with the contents they reference
type fileRefResolver struct {
	// Source YAML document
	data []byte
	// Byte offset where each line of data starts
	lineStarts []int
	// Directory that relative paths are resolved against
	baseDir string
	// Parsed dotenv files, keyed by path, so each file is read once
	envFiles map[string]map[string]string
}

// fileRefReplacement is a span of the source document that is replaced with a resolved value
type fileRefReplacement struct {
	start int
	end   int
	text  string
}

// resolveFileRefs replaces values tagged with !file or !envfile in a YAML document with the contents they reference
// Relative paths are resolved against baseDir
//
// The replacement happens on the source bytes rather than on a decoded yaml.Node tree, because decoding a yaml.Node ignores the decoder's KnownFields option, and re-encoding the tree changes the line numbers reported in decoding errors
// Each reference is replaced with a double-quoted string on the same line, so the line numbers of the document do not change
func resolveFileRefs(data []byte, baseDir string) ([]byte, error) {
	// Most config files do not use these tags, so avoid parsing the document twice
	if !bytes.Contains(data, []byte(tagFile)) && !bytes.Contains(data, []byte(tagEnvFile)) {
		return data, nil
	}

	// If the document can't be parsed, leave it unchanged so the decoder reports the syntax error as it does for any other config file
	var doc yaml.Node
	err := yaml.Unmarshal(data, &doc)
	if err != nil {
		return data, nil //nolint:nilerr
	}

	r := &fileRefResolver{
		data:       data,
		lineStarts: lineOffsets(data),
		baseDir:    baseDir,
		envFiles:   map[string]map[string]string{},
	}
	replacements, err := r.collect(&doc, nil)
	if err != nil {
		return nil, err
	}
	if len(replacements) == 0 {
		return data, nil
	}

	slices.SortFunc(replacements, func(a, b fileRefReplacement) int {
		return cmp.Compare(a.start, b.start)
	})

	var out bytes.Buffer
	out.Grow(len(data))
	prev := 0
	for _, repl := range replacements {
		out.Write(data[prev:repl.start])
		out.WriteString(repl.text)
		prev = repl.end
	}
	out.Write(data[prev:])

	return out.Bytes(), nil
}

// collect walks the document tree and appends a replacement for every tagged node to dst
// Alias nodes are not followed: the anchored node is replaced where it's defined, and aliases then refer to the resolved value
func (r *fileRefResolver) collect(node *yaml.Node, dst []fileRefReplacement) ([]fileRefReplacement, error) {
	if node.Tag == tagFile || node.Tag == tagEnvFile {
		repl, err := r.replacement(node)
		if err != nil {
			return nil, err
		}
		return append(dst, repl), nil
	}

	var err error
	for _, child := range node.Content {
		dst, err = r.collect(child, dst)
		if err != nil {
			return nil, err
		}
	}

	return dst, nil
}

// replacement returns the replacement for a tagged node
func (r *fileRefResolver) replacement(node *yaml.Node) (fileRefReplacement, error) {
	if node.Kind != yaml.ScalarNode {
		return fileRefReplacement{}, fmt.Errorf("line %d: %s can only be used on a string value", node.Line, node.Tag)
	}

	start, end, err := r.span(node)
	if err != nil {
		return fileRefReplacement{}, err
	}

	value, err := r.resolve(node)
	if err != nil {
		return fileRefReplacement{}, err
	}

	// Go's escape sequences in quoted strings are a subset of the ones YAML supports in double-quoted scalars
	// Quoting also escapes newlines, so the replacement doesn't add lines to the document
	text := strconv.Quote(value)

	// Keep the anchor so aliases to this node resolve to the same value
	if node.Anchor != "" {
		text = "&" + node.Anchor + " " + text
	}

	return fileRefReplacement{
		start: start,
		end:   end,
		text:  text,
	}, nil
}

// span returns the byte offsets of a tagged node in the source document, from its first property (the tag or an anchor) to the end of its value
func (r *fileRefResolver) span(node *yaml.Node) (int, int, error) {
	errLocate := fmt.Errorf("line %d: could not locate the value tagged with %s", node.Line, node.Tag)
	errSameLine := fmt.Errorf("line %d: the value tagged with %s must be on the same line as the tag", node.Line, node.Tag)

	// The parser reports columns in characters, so walk the line one rune at a time to find the byte offset
	if node.Line < 1 || node.Line > len(r.lineStarts) {
		return 0, 0, errLocate
	}
	start := r.lineStarts[node.Line-1]
	for range node.Column - 1 {
		if start >= len(r.data) {
			return 0, 0, errLocate
		}
		_, size := utf8.DecodeRune(r.data[start:])
		start += size
	}

	// Skip the node's properties, which are the tag and optionally an anchor, in any order
	// Values can't start with "!" or "&" unless they're quoted, so these characters can only begin a property here
	pos := start
	sawTag := false
	for pos < len(r.data) && (r.data[pos] == '!' || r.data[pos] == '&') {
		if r.data[pos] == '!' {
			sawTag = true
		}
		for pos < len(r.data) && !isYAMLSpace(r.data[pos]) {
			pos++
		}
		for pos < len(r.data) && (r.data[pos] == ' ' || r.data[pos] == '\t') {
			pos++
		}
	}
	if !sawTag {
		return 0, 0, errLocate
	}

	var end int
	switch node.Style &^ yaml.TaggedStyle {
	case yaml.DoubleQuotedStyle:
		end = quotedScalarEnd(r.data, pos, '"')
	case yaml.SingleQuotedStyle:
		end = quotedScalarEnd(r.data, pos, '\'')
	case 0:
		// The source text of a plain scalar on a single line is the same as its value
		// If the value continues on another line, the parser joins the lines, so it no longer matches the source
		if !bytes.HasPrefix(r.data[pos:], []byte(node.Value)) {
			return 0, 0, errSameLine
		}
		end = pos + len(node.Value)
	default:
		// Literal and folded block scalars always span multiple lines
		return 0, 0, errSameLine
	}
	if end < 0 {
		return 0, 0, errSameLine
	}

	return start, end, nil
}

// resolve returns the value that a tagged node references
func (r *fileRefResolver) resolve(node *yaml.Node) (string, error) {
	switch node.Tag {
	case tagFile:
		if node.Value == "" {
			return "", fmt.Errorf("line %d: %s requires a file path", node.Line, tagFile)
		}

		path := r.resolvePath(node.Value)
		data, err := os.ReadFile(path) //nolint:gosec
		if err != nil {
			return "", fmt.Errorf("line %d: failed to read file referenced by %s: %w", node.Line, tagFile, err)
		}

		// Files usually end with a newline that's not part of the value, for example when written with echo
		return strings.TrimRight(string(data), "\r\n"), nil

	case tagEnvFile:
		// Variable names can't contain whitespace, so the name is what follows the last space, and the path can contain spaces
		ref := strings.TrimSpace(node.Value)
		idx := strings.LastIndexAny(ref, " \t")
		if idx < 0 {
			return "", fmt.Errorf("line %d: %s requires a file path and a variable name, separated by a space", node.Line, tagEnvFile)
		}
		path := r.resolvePath(strings.TrimRight(ref[:idx], " \t"))
		name := ref[idx+1:]

		vars, err := r.envFile(path)
		if err != nil {
			return "", fmt.Errorf("line %d: %w", node.Line, err)
		}

		value, ok := vars[name]
		if !ok {
			return "", fmt.Errorf("line %d: variable '%s' not found in env file '%s'", node.Line, name, path)
		}

		return value, nil

	default:
		// Indicates a development-time error
		return "", errors.New("unsupported tag: " + node.Tag)
	}
}

// envFile returns the variables in a dotenv file, reading the file only the first time it's requested
func (r *fileRefResolver) envFile(path string) (map[string]string, error) {
	vars, ok := r.envFiles[path]
	if ok {
		return vars, nil
	}

	data, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		return nil, fmt.Errorf("failed to read env file referenced by %s: %w", tagEnvFile, err)
	}

	vars, err = parseDotenv(data)
	if err != nil {
		return nil, fmt.Errorf("failed to parse env file '%s': %w", path, err)
	}
	r.envFiles[path] = vars

	return vars, nil
}

// resolvePath resolves relative paths against the directory of the config file, so references keep working regardless of the working directory
func (r *fileRefResolver) resolvePath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(r.baseDir, path)
}

// quotedScalarEnd returns the offset right after the closing quote of the quoted scalar that starts at pos
// It returns -1 if the scalar doesn't start at pos or doesn't end on the same line
func quotedScalarEnd(data []byte, pos int, quote byte) int {
	if pos >= len(data) || data[pos] != quote {
		return -1
	}

	for i := pos + 1; i < len(data); i++ {
		switch {
		case data[i] == '\n' || data[i] == '\r':
			return -1
		case quote == '"' && data[i] == '\\':
			// Skip the escaped character, unless it's an escaped line break which continues the scalar on the next line
			if i+1 >= len(data) || data[i+1] == '\n' || data[i+1] == '\r' {
				return -1
			}
			i++
		case data[i] == quote:
			// In single-quoted scalars, a quote is escaped by repeating it
			if quote == '\'' && i+1 < len(data) && data[i+1] == '\'' {
				i++
				continue
			}
			return i + 1
		}
	}

	return -1
}

// lineOffsets returns the byte offset where each line of data starts
func lineOffsets(data []byte) []int {
	// The parser doesn't count a byte order mark as a column, so the first line starts after it
	first := 0
	if bytes.HasPrefix(data, []byte(utf8BOM)) {
		first = len(utf8BOM)
	}

	offsets := []int{first}
	for i, c := range data {
		if c == '\n' {
			offsets = append(offsets, i+1)
		}
	}
	return offsets
}

func isYAMLSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
