package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/morphis/gummi/internal/atomicfile"
)

// MaxNameLen is the longest instance name, in characters: it has to fit a
// status bar pill and a browser tab beside the rest of their words.
const MaxNameLen = 40

// ValidateName trims an instance name and refuses one that could not be
// drawn on a single line. The empty name is valid: it clears the name.
func ValidateName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > MaxNameLen {
		return "", fmt.Errorf("at most %d characters", MaxNameLen)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", errors.New("one line, no control characters")
		}
	}
	return name, nil
}

// SetName writes the instance name into the workspace config at path,
// leaving every other key, and its comments, where they were. An empty
// name removes the key. A missing file is created.
func SetName(path, name string) error {
	name, err := ValidateName(name)
	if err != nil {
		return err
	}
	var value *yaml.Node
	if name != "" {
		value = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}
	}
	return setKey(path, "name", value, true)
}

// SetWorkflows writes workflows: on or off into the workspace config at
// path, leaving the rest of the file as it was. Off is written as "off",
// not removed, so a workspace off still beats a user-level on.
func SetWorkflows(path string, on bool) error {
	v := "off"
	if on {
		v = "on"
	}
	return setKey(path, "workflows", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}, false)
}

// setKey writes one top-level key of the workspace config at path, leaving
// every other key, and its comments, where they were. A nil value removes
// the key; a new key goes first or last. A missing file is created.
//
// It edits the file's text, not a re-encoding of it: only the key's own
// lines are replaced, so the rest keeps its comments, its indentation and
// its blank lines byte for byte. The key's lines run from its own line to
// the line before the next top-level key (or the end), less the blank
// lines and unindented comments directly above it: those belong to the
// next key, or to the file.
func setKey(path, key string, value *yaml.Node, first bool) error {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	var doc yaml.Node
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return fmt.Errorf("parsing %s: %w", path, err)
		}
	}
	var root *yaml.Node
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		root = doc.Content[0]
		if root.Kind != yaml.MappingNode {
			return fmt.Errorf("%s: top level is not a mapping", path)
		}
	}
	lines := strings.SplitAfter(string(raw), "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	if n := len(lines); n > 0 && !strings.HasSuffix(lines[n-1], "\n") {
		lines[n-1] += "\n"
	}

	var rendered []string
	if value != nil {
		block, err := renderKey(key, value, indentOf(lines))
		if err != nil {
			return fmt.Errorf("encoding %s: %w", path, err)
		}
		rendered = strings.SplitAfter(strings.TrimSuffix(block, "\n")+"\n", "\n")
		rendered = rendered[:len(rendered)-1]
	}

	// Where the key's lines are (0-based, [from, to)), and where the
	// first top-level key starts.
	from, to, firstKey := -1, -1, len(lines)
	if root != nil {
		for i := 0; i+1 < len(root.Content); i += 2 {
			k := root.Content[i]
			if i == 0 {
				firstKey = k.Line - 1
			}
			if k.Value != key {
				continue
			}
			from, to = k.Line-1, len(lines)
			if i+2 < len(root.Content) {
				to = root.Content[i+2].Line - 1
			}
			for to > from+1 && isTopCommentOrBlank(lines[to-1]) {
				to--
			}
		}
		for firstKey > 0 && strings.HasPrefix(lines[firstKey-1], "#") {
			firstKey--
		}
	}
	var out []string
	switch {
	case from >= 0:
		out = append(append(append(out, lines[:from]...), rendered...), lines[to:]...)
	case value == nil:
		return nil
	case first:
		out = append(append(append(out, lines[:firstKey]...), rendered...), lines[firstKey:]...)
	default:
		out = append(append(out, lines...), rendered...)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return atomicfile.Write(path, []byte(strings.Join(out, "")), 0o644)
}

// renderKey is `key: value` as YAML, indented by indent spaces per level.
func renderKey(key string, value *yaml.Node, indent int) (string, error) {
	m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value,
	}}
	var b strings.Builder
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(indent)
	if err := enc.Encode(m); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return b.String(), nil
}

// indentOf is the indentation the file already uses (the first indented
// line that is not a comment), 2 when it has none.
func indentOf(lines []string) int {
	for _, l := range lines {
		if isCommentOrBlank(l) {
			continue
		}
		if n := len(l) - len(strings.TrimLeft(l, " ")); n > 0 {
			return n
		}
	}
	return 2
}

func isTopCommentOrBlank(l string) bool {
	return strings.TrimSpace(l) == "" || strings.HasPrefix(l, "#")
}

func isCommentOrBlank(l string) bool {
	t := strings.TrimSpace(l)
	return t == "" || strings.HasPrefix(t, "#")
}
