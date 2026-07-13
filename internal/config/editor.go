// Package config edits a service's own configuration file in place.
//
// The services wharf runs mostly read a hard-coded path (viper's
// AddConfigPath("config")) with no flag or environment override, so there is no
// way to hand them a config from elsewhere. Their config files are gitignored
// local files, so editing them is legitimate — but only if the edit is
// *surgical*.
//
// A decode/re-encode round trip would silently strip every comment and reflow
// the document. Instead this package locates the exact line and column of the
// value it must change and rewrites that token alone, leaving every other byte
// of the file identical.
package config

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Format identifies how a config file is encoded.
type Format string

const (
	FormatYAML   Format = "yaml"
	FormatJSON   Format = "json"
	FormatDotenv Format = "dotenv"
)

// SetValue returns raw with the value at key replaced by value.
//
// key is a dotted path for YAML and JSON ("app.port") and a plain variable name
// for dotenv ("PORT"). It reports changed=false when the file already holds the
// desired value, so callers can avoid pointless writes.
func SetValue(raw []byte, format Format, key, value string) (out []byte, changed bool, err error) {
	switch format {
	case FormatDotenv:
		return setDotenv(raw, key, value)
	case FormatYAML, FormatJSON:
		// JSON is a subset of YAML, so the node-based editor handles both and
		// preserves JSON's formatting for free.
		return setYAML(raw, key, value)
	default:
		return nil, false, fmt.Errorf("unsupported config format %q", format)
	}
}

// setYAML rewrites a scalar in place using its source position.
func setYAML(raw []byte, key, value string) ([]byte, bool, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, false, fmt.Errorf("parse yaml: %w", err)
	}
	if len(doc.Content) == 0 {
		return nil, false, fmt.Errorf("empty document")
	}

	target := findNode(doc.Content[0], strings.Split(key, "."))
	if target == nil {
		return nil, false, fmt.Errorf("key %q not found", key)
	}
	if target.Kind != yaml.ScalarNode {
		return nil, false, fmt.Errorf("key %q is not a scalar", key)
	}
	if target.Value == value {
		return raw, false, nil
	}

	edited, err := replaceToken(raw, target, value)
	if err != nil {
		return nil, false, err
	}
	return edited, true, nil
}

// findNode walks a dotted path through a YAML mapping.
func findNode(node *yaml.Node, path []string) *yaml.Node {
	cur := node
	for _, segment := range path {
		if cur.Kind == yaml.DocumentNode && len(cur.Content) > 0 {
			cur = cur.Content[0]
		}
		if cur.Kind != yaml.MappingNode {
			return nil
		}
		// A mapping's Content alternates key, value, key, value.
		found := false
		for i := 0; i+1 < len(cur.Content); i += 2 {
			if strings.EqualFold(cur.Content[i].Value, segment) {
				cur = cur.Content[i+1]
				found = true
				break
			}
		}
		if !found {
			return nil
		}
	}
	return cur
}

// replaceToken swaps the source text of node's scalar for value, preserving the
// original quoting style so a quoted port stays quoted and an int stays an int.
func replaceToken(raw []byte, node *yaml.Node, value string) ([]byte, error) {
	lines := bytes.Split(raw, []byte("\n"))
	idx := node.Line - 1 // yaml positions are 1-based
	if idx < 0 || idx >= len(lines) {
		return nil, fmt.Errorf("value for %q is at line %d, outside the file", node.Value, node.Line)
	}

	line := lines[idx]
	start := node.Column - 1
	if start < 0 || start > len(line) {
		return nil, fmt.Errorf("value for %q is at column %d, outside the line", node.Value, node.Column)
	}

	oldToken := sourceToken(node)
	newToken := quoteLike(node, value)

	if !bytes.HasPrefix(line[start:], []byte(oldToken)) {
		// The node's position did not land where we expected. Refusing is the
		// only safe move: a blind write here could corrupt the file.
		return nil, fmt.Errorf("cannot locate %q at line %d col %d", node.Value, node.Line, node.Column)
	}

	var edited bytes.Buffer
	edited.Write(line[:start])
	edited.WriteString(newToken)
	edited.Write(line[start+len(oldToken):])
	lines[idx] = edited.Bytes()

	return bytes.Join(lines, []byte("\n")), nil
}

// sourceToken reconstructs the exact text the scalar occupies in the file,
// including quotes.
func sourceToken(node *yaml.Node) string {
	switch node.Style {
	case yaml.DoubleQuotedStyle:
		return `"` + node.Value + `"`
	case yaml.SingleQuotedStyle:
		return `'` + node.Value + `'`
	default:
		return node.Value
	}
}

// quoteLike renders value in the same style the original scalar used, so the
// edit is invisible to any linter or diff beyond the value itself.
func quoteLike(node *yaml.Node, value string) string {
	switch node.Style {
	case yaml.DoubleQuotedStyle:
		return `"` + value + `"`
	case yaml.SingleQuotedStyle:
		return `'` + value + `'`
	default:
		return value
	}
}

// setDotenv rewrites KEY=value, preserving comments, blank lines and ordering.
// A key that is absent is appended rather than treated as an error: a .env is
// routinely incomplete, and adding PORT is the intended outcome.
func setDotenv(raw []byte, key, value string) ([]byte, bool, error) {
	lines := strings.Split(string(raw), "\n")

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		name, existing, ok := strings.Cut(trimmed, "=")
		if !ok {
			continue
		}
		name = strings.TrimSpace(strings.TrimPrefix(name, "export "))
		if name != key {
			continue
		}

		if unquote(strings.TrimSpace(existing)) == value {
			return raw, false, nil
		}
		// Rewrite the whole line: dotenv has no structure worth preserving
		// within a line, and this keeps quoting predictable.
		lines[i] = key + "=" + value
		return []byte(strings.Join(lines, "\n")), true, nil
	}

	// Not present — append, keeping the file newline-terminated.
	body := strings.TrimRight(string(raw), "\n")
	if body != "" {
		body += "\n"
	}
	body += key + "=" + value + "\n"
	return []byte(body), true, nil
}

func unquote(s string) string {
	return strings.Trim(s, `"'`)
}

// yamlValue reads the scalar at a dotted key, if present.
func yamlValue(raw []byte, key string) (string, bool) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil || len(doc.Content) == 0 {
		return "", false
	}
	node := findNode(doc.Content[0], strings.Split(key, "."))
	if node == nil || node.Kind != yaml.ScalarNode {
		return "", false
	}
	return node.Value, true
}

// dotenvValue reads a variable's value, if present.
func dotenvValue(raw []byte, key string) (string, bool) {
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		name, value, ok := strings.Cut(trimmed, "=")
		if !ok {
			continue
		}
		if strings.TrimSpace(strings.TrimPrefix(name, "export ")) == key {
			return unquote(strings.TrimSpace(value)), true
		}
	}
	return "", false
}

// Itoa is a convenience for the common case of writing a port.
func Itoa(n int) string { return strconv.Itoa(n) }
