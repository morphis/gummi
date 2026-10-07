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
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: top level is not a mapping", path)
	}
	idx := -1
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "name" {
			idx = i
			break
		}
	}
	switch {
	case name == "" && idx >= 0:
		root.Content = append(root.Content[:idx], root.Content[idx+2:]...)
	case name == "":
		return nil
	case idx >= 0:
		v := root.Content[idx+1]
		v.Kind, v.Tag, v.Value, v.Style = yaml.ScalarNode, "!!str", name, 0
	default:
		root.Content = append([]*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "name"},
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: name},
		}, root.Content...)
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return fmt.Errorf("encoding %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return atomicfile.Write(path, out, 0o644)
}
