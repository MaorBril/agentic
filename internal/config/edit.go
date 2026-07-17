package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Doc is the config file as a yaml.v3 node tree — editing at node level
// preserves comments and ordering.
type Doc struct {
	root *yaml.Node
	path string
}

func LoadDoc() (*Doc, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	if root.Kind == 0 || len(root.Content) == 0 {
		root = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	}
	return &Doc{root: &root, path: path}, nil
}

// Save validates the edited tree parses back into a valid Config, then
// writes it out.
func (d *Doc) Save() error {
	data, err := yaml.Marshal(d.root.Content[0])
	if err != nil {
		return err
	}
	if _, err := Parse(data); err != nil {
		return fmt.Errorf("refusing to save invalid config: %w", err)
	}
	return os.WriteFile(d.path, data, 0o600)
}

func (d *Doc) top() *yaml.Node { return d.root.Content[0] }

// mapChild finds key in a mapping node, optionally creating it.
func mapChild(m *yaml.Node, key string, create bool) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	if !create {
		return nil
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key},
		&yaml.Node{Kind: yaml.MappingNode})
	return m.Content[len(m.Content)-1]
}

func mapDelete(m *yaml.Node, key string) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return true
		}
	}
	return false
}

// Get returns the scalar (or re-marshaled subtree) at a dot path.
func (d *Doc) Get(dotPath string) (string, error) {
	node := d.top()
	for _, part := range strings.Split(dotPath, ".") {
		if node.Kind != yaml.MappingNode {
			return "", fmt.Errorf("%s: not a mapping", dotPath)
		}
		node = mapChild(node, part, false)
		if node == nil {
			return "", fmt.Errorf("%s: not found", dotPath)
		}
	}
	if node.Kind == yaml.ScalarNode {
		return node.Value, nil
	}
	out, err := yaml.Marshal(node)
	return strings.TrimSpace(string(out)), err
}

// Set writes a scalar at a dot path, creating intermediate mappings and
// inferring the YAML type (int/float/bool/string).
func (d *Doc) Set(dotPath, value string) error {
	parts := strings.Split(dotPath, ".")
	node := d.top()
	for _, part := range parts[:len(parts)-1] {
		if node.Kind != yaml.MappingNode {
			return fmt.Errorf("%s: not a mapping", part)
		}
		node = mapChild(node, part, true)
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: parent is not a mapping", dotPath)
	}
	key := parts[len(parts)-1]
	target := mapChild(node, key, false)
	if target == nil {
		node.Content = append(node.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: key},
			&yaml.Node{Kind: yaml.ScalarNode})
		target = node.Content[len(node.Content)-1]
	}
	*target = yaml.Node{Kind: yaml.ScalarNode, Value: value, Tag: inferTag(value)}
	return nil
}

// SetSubtree replaces (or creates) section[name] with the parsed YAML of
// `snippet` — used by `providers add` / `models add`.
func (d *Doc) SetSubtree(section, name, snippet string) error {
	var parsed yaml.Node
	if err := yaml.Unmarshal([]byte(snippet), &parsed); err != nil {
		return err
	}
	sec := mapChild(d.top(), section, true)
	if sec.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: not a mapping", section)
	}
	value := parsed.Content[0]
	if existing := mapChild(sec, name, false); existing != nil {
		*existing = *value
		return nil
	}
	sec.Content = append(sec.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: name}, value)
	return nil
}

// Delete removes section[name].
func (d *Doc) Delete(section, name string) error {
	sec := mapChild(d.top(), section, false)
	if sec == nil || !mapDelete(sec, name) {
		return fmt.Errorf("%s.%s: not found", section, name)
	}
	return nil
}

func inferTag(v string) string {
	if _, err := strconv.ParseInt(v, 10, 64); err == nil {
		return "!!int"
	}
	if _, err := strconv.ParseFloat(v, 64); err == nil {
		return "!!float"
	}
	if v == "true" || v == "false" {
		return "!!bool"
	}
	return "!!str"
}
