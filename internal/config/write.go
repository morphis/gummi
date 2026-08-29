package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/morphis/gummi/internal/atomicfile"
)

// SetAgent persists the `agent:` key (Config.Agent — the hosted agent-tab
// CLI selection the TUI's picker, or a hand-edit, chooses) into the
// workspace config.yaml at path, without disturbing anything else already
// in the file: every other key, its order, and every comment survive
// untouched.
//
// The naive approach — Load the file into a Config, set the field,
// yaml.Marshal the struct back out — cannot do that. A comment has no
// home on a Go struct field, so re-marshaling one always produces a bare
// `key: value` dump and silently deletes whatever a human wrote in the
// file (Template itself is mostly prose above its first real key). That
// would be a hostile trade for a feature whose entire job is to correct
// one line.
//
// Instead this decodes the file into a yaml.Node — v3's concrete syntax
// tree, where comments are attached to the nodes they sit beside rather
// than discarded on decode — walks the top-level mapping for an existing
// "agent" entry and rewrites just its value node in place, or appends a
// fresh "agent: <name>" pair at the end of the mapping when there is
// none. Re-encoding that same tree (never a struct) is what turns the
// write into a point edit instead of a re-render.
func SetAgent(path, name string) error {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", path, err)
	}

	var doc yaml.Node
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return fmt.Errorf("parsing %s: %w", path, err)
		}
	}
	// A missing file, an empty one, or one that is only comments all leave
	// doc without a document node (Kind == 0, the zero value) — start a
	// fresh document/mapping pair so there is somewhere to put the key.
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode}
	}
	if len(doc.Content) == 0 {
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: cannot set agent: top-level YAML is not a mapping", path)
	}

	// A mapping's Content alternates key, value, key, value, ... — walk it
	// two at a time looking for an existing "agent" key.
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "agent" {
			v := root.Content[i+1]
			// Reset every field a previous value might have set (a block
			// scalar's Style, a mapping's Content, …) so the node becomes
			// a plain string scalar regardless of what it held before.
			v.Kind, v.Tag, v.Value, v.Style, v.Content = yaml.ScalarNode, "!!str", name, 0, nil
			return writeYAMLDoc(path, &doc)
		}
	}
	root.Content = append(root.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "agent"},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name},
	)
	return writeYAMLDoc(path, &doc)
}

// writeYAMLDoc marshals a yaml.Node document and writes it atomically —
// the same crash-safety atomicfile already gives every other file gummi
// writes under .gummi. A config file a human hand-tuned deserves no less
// protection against a torn write than a spec draft does.
func writeYAMLDoc(path string, doc *yaml.Node) error {
	out, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("encoding %s: %w", path, err)
	}
	if err := atomicfile.Write(path, out, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
