// Package configfile reads and edits human-authored YAML configuration.
package configfile

import (
	"bytes"
	"fmt"
	"io"

	"go.yaml.in/yaml/v3"
)

// Parse accepts one mapping document. Decode once to validate the whole tree,
// including duplicate keys in settings unknown to a particular consumer.
func Parse(data []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := decoder.Decode(&doc); err != nil {
		return nil, err
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("configuration must contain a YAML mapping")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("configuration must contain exactly one YAML document")
	}
	var checked any
	if err := doc.Decode(&checked); err != nil {
		return nil, err
	}
	return &doc, nil
}

func Decode(data []byte, target any) error {
	doc, err := Parse(data)
	if err != nil {
		return err
	}
	return doc.Decode(target)
}

func Node(value any) (*yaml.Node, error) {
	var node yaml.Node
	if err := node.Encode(value); err != nil {
		return nil, err
	}
	return &node, nil
}

func Encode(node *yaml.Node) ([]byte, error) {
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(node); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func Root(node *yaml.Node) *yaml.Node {
	if node.Kind == yaml.DocumentNode {
		return node.Content[0]
	}
	return node
}

func index(node *yaml.Node, key string) int {
	if node.Kind == yaml.MappingNode {
		for i := 0; i < len(node.Content); i += 2 {
			if node.Content[i].Value == key {
				return i
			}
		}
	}
	return -1
}

// Field also resolves inherited fields; edits insert a local override instead
// of mutating the anchor supplying that field.
func Field(node *yaml.Node, key string) *yaml.Node {
	node = Root(node)
	if node.Kind == yaml.AliasNode {
		return Field(node.Alias, key)
	}
	if i := index(node, key); i >= 0 {
		return node.Content[i+1]
	}
	return inheritedField(node, key)
}

func inheritedField(node *yaml.Node, key string) *yaml.Node {
	if i := index(node, "<<"); i >= 0 {
		merge := node.Content[i+1]
		if merge.Kind != yaml.SequenceNode {
			return Field(merge, key)
		}
		for _, item := range merge.Content {
			if value := Field(item, key); value != nil {
				return value
			}
		}
	}
	return nil
}

// Equal ignores presentation metadata. The compared trees are encoded typed
// values, not user documents, so mapping order is deterministic.
func Equal(a, b *yaml.Node) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Kind != b.Kind || a.Tag != b.Tag || a.Value != b.Value || len(a.Content) != len(b.Content) {
		return false
	}
	for i := range a.Content {
		if !Equal(a.Content[i], b.Content[i]) {
			return false
		}
	}
	return true
}

// expanded copies an inherited value without creating duplicate anchors.
func expanded(node *yaml.Node) *yaml.Node {
	if node.Kind == yaml.AliasNode {
		result := expanded(node.Alias)
		comments(result, node)
		return result
	}
	result := *node
	result.Anchor = ""
	result.Content = make([]*yaml.Node, len(node.Content))
	for i, child := range node.Content {
		result.Content[i] = expanded(child)
	}
	return &result
}

func comments(next, previous *yaml.Node) {
	if previous.HeadComment != "" {
		next.HeadComment = previous.HeadComment
	}
	if previous.LineComment != "" {
		next.LineComment = previous.LineComment
	}
	if previous.FootComment != "" {
		next.FootComment = previous.FootComment
	}
}

// Freeze references before changing an anchored value, so editing one setting
// cannot also change an unrelated setting that happened to share its anchor.
func detachAliases(doc, target *yaml.Node) {
	for i, child := range doc.Content {
		if child.Kind == yaml.AliasNode && child.Alias == target {
			doc.Content[i] = expanded(child)
		} else {
			detachAliases(child, target)
		}
	}
}

// Patch applies only the typed delta, retaining unknown settings and comments.
// before/after must be independently encoded snapshots (not shared Go maps).
func Patch(document, before, after *yaml.Node) bool {
	if Equal(before, after) {
		return false
	}
	patch(document, Root(document), before, after)
	return true
}

func patch(doc, current, before, after *yaml.Node) {
	if Equal(before, after) {
		return
	}
	if current.Anchor != "" {
		detachAliases(doc, current)
	}
	if current.Kind == yaml.AliasNode {
		*current = *expanded(current)
	}
	if current.Kind == yaml.MappingNode && after.Kind == yaml.MappingNode && before != nil && before.Kind == yaml.MappingNode {
		for i := 0; i < len(before.Content); i += 2 {
			key := before.Content[i].Value
			if index(after, key) < 0 {
				j := index(current, key)
				// Removing a local override must not reveal an inherited value.
				if inheritedField(current, key) != nil {
					unset := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
					if j < 0 {
						current.Content = append(current.Content, expanded(before.Content[i]), unset)
					} else {
						detachAliases(doc, current.Content[j+1])
						comments(unset, current.Content[j+1])
						current.Content[j+1] = unset
					}
				} else if j >= 0 {
					detachAliases(doc, current.Content[j+1])
					current.Content = append(current.Content[:j], current.Content[j+2:]...)
				}
			}
		}
		for i := 0; i < len(after.Content); i += 2 {
			key, value := after.Content[i], after.Content[i+1]
			old := Field(before, key.Value)
			if Equal(old, value) {
				continue
			}
			j := index(current, key.Value)
			if j < 0 {
				if inherited := Field(current, key.Value); inherited != nil {
					copy := expanded(inherited)
					patch(doc, copy, old, value)
					current.Content = append(current.Content, expanded(key), copy)
				} else {
					current.Content = append(current.Content, expanded(key), expanded(value))
				}
			} else {
				patch(doc, current.Content[j+1], old, value)
			}
		}
		return
	}
	if current.Kind == yaml.SequenceNode && after.Kind == yaml.SequenceNode {
		for i, value := range after.Content {
			if i < len(current.Content) {
				var old *yaml.Node
				if before != nil && i < len(before.Content) {
					old = before.Content[i]
				}
				patch(doc, current.Content[i], old, value)
			} else {
				current.Content = append(current.Content, expanded(value))
			}
		}
		for _, removed := range current.Content[len(after.Content):] {
			detachAliases(doc, removed)
		}
		current.Content = current.Content[:len(after.Content)]
		return
	}
	next := expanded(after)
	comments(next, current)
	next.Anchor = current.Anchor
	if next.Kind == current.Kind {
		next.Style = current.Style
	}
	*current = *next
}

// Annotate adds guidance only where no user comment is already present.
func Annotate(node *yaml.Node, guidance map[string]string) {
	var visit func(*yaml.Node, string)
	visit = func(node *yaml.Node, prefix string) {
		node = Root(node)
		if node.Kind != yaml.MappingNode {
			return
		}
		for i := 0; i < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			path := prefix + key.Value
			if key.HeadComment == "" && key.LineComment == "" && value.HeadComment == "" && value.LineComment == "" {
				key.HeadComment = guidance[path]
			}
			visit(value, path+".")
		}
	}
	visit(node, "")
}
