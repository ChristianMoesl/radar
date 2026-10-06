package obsidian

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"

	"radar/internal/protocol"
)

// Ranges refer to the original file, not a mutable slice of lines. Applying all
// replacements from right to left keeps optional insertions and multiline values
// from shifting the indexes of other managed fields.
type fieldRange struct {
	start, end int
	comment    string
}

func parseNote(content string) (note, error) {
	current := note{content: content, fields: map[string]fieldRange{}, newline: "\n"}
	lines := strings.SplitAfter(content, "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return current, fmt.Errorf("Markdown frontmatter is required")
	}
	if strings.HasSuffix(lines[0], "\r\n") {
		current.newline = "\r\n"
	}
	offsets := make([]int, len(lines))
	end := -1
	for i := range lines {
		if i > 0 {
			offsets[i] = offsets[i-1] + len(lines[i-1])
		}
		if i > 0 && strings.TrimRight(lines[i], " \t\r\n") == "---" && end < 0 {
			end = i
			current.frontmatterEnd = offsets[i]
		}
	}
	if end < 0 {
		return current, fmt.Errorf("Markdown frontmatter is not closed")
	}
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(content[len(lines[0]):offsets[end]]), &document); err != nil {
		return current, fmt.Errorf("invalid YAML frontmatter")
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode || document.Content[0].Style&yaml.FlowStyle != 0 {
		return current, fmt.Errorf("frontmatter must be a block YAML mapping")
	}
	root := document.Content[0]
	values := map[string]*yaml.Node{}
	for i := 0; i < len(root.Content); i += 2 {
		key, value := root.Content[i], root.Content[i+1]
		if key.Kind != yaml.ScalarNode {
			return current, fmt.Errorf("frontmatter keys must be YAML scalars")
		}
		if _, exists := values[key.Value]; exists {
			return current, fmt.Errorf("duplicate frontmatter field %q", key.Value)
		}
		values[key.Value] = value
		startLine := key.Line // YAML line 1 is file line 2.
		endLine := end
		if i+2 < len(root.Content) {
			endLine = root.Content[i+2].Line
		}
		// Comments/blank lines between top-level fields belong to the user, not
		// to the preceding managed value. Nested binding comments are managed.
		for endLine > startLine+1 {
			line := strings.TrimRight(lines[endLine-1], "\r\n")
			if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "#") {
				break
			}
			endLine--
		}
		comment := inlineComment(lines[startLine])
		// YAML distinguishes a real quote from an apostrophe inside a plain
		// scalar (e.g. a flow binding's ID). Use its comment metadata too.
		for _, node := range []*yaml.Node{key, value} {
			if node.Line == key.Line && node.LineComment != "" {
				line := strings.TrimRight(lines[startLine], "\r\n")
				if index := strings.LastIndex(line, node.LineComment); index >= 0 {
					for index > 0 && (line[index-1] == ' ' || line[index-1] == '\t') {
						index--
					}
					comment = line[index:]
				}
			}
		}
		current.fields[key.Value] = fieldRange{start: offsets[startLine], end: offsets[endLine], comment: comment}
	}
	// Retain the ID even when another field is invalid, so collection can retain
	// the previous observation for a malformed or conflicted note.
	if id := values["radar-id"]; id != nil && id.Kind == yaml.ScalarNode {
		current.ID = id.Value
	}
	for _, field := range []string{"radar-id", "radar-title", "radar-state", "radar-priority", "radar-created-at", "radar-completed-at"} {
		if values[field] == nil {
			return current, fmt.Errorf("missing required field %s", field)
		}
	}
	for _, field := range []struct {
		name  string
		value *string
	}{
		{"radar-id", &current.ID}, {"radar-title", &current.Title},
		{"radar-state", &current.State}, {"radar-priority", &current.Priority},
		{"radar-created-at", &current.CreatedAt}, {"radar-completed-at", &current.CompletedAt},
		{"radar-completion-baseline", &current.CompletionBaseline},
	} {
		value := values[field.name]
		if value == nil {
			continue
		}
		nullable := field.name == "radar-completed-at" || field.name == "radar-completion-baseline"
		timestamp := field.name == "radar-created-at" || field.name == "radar-completed-at"
		if value.Kind != yaml.ScalarNode || (value.Tag != "!!str" && !(nullable && value.Tag == "!!null") && !(timestamp && value.Tag == "!!timestamp")) {
			return current, fmt.Errorf("%s must be a YAML string", field.name)
		}
		*field.value = value.Value
		if value.Tag == "!!null" {
			*field.value = ""
		}
	}
	current.Title = strings.TrimSpace(current.Title)
	if current.Title == "" {
		return current, fmt.Errorf("radar-title must be a non-empty YAML string")
	}
	if !validID.MatchString(current.ID) {
		return current, fmt.Errorf("invalid radar-id %q", current.ID)
	}
	if current.State != "open" && current.State != "done" {
		return current, fmt.Errorf("unsupported radar-state %q", current.State)
	}
	if current.Priority != "normal" && current.Priority != "urgent" {
		return current, fmt.Errorf("unsupported radar-priority %q", current.Priority)
	}
	if err := validTimestamp("radar-created-at", current.CreatedAt, false); err != nil {
		return current, err
	}
	if err := validTimestamp("radar-completed-at", current.CompletedAt, current.State == "open"); err != nil {
		return current, err
	}
	if current.State == "done" && current.CompletedAt == "" {
		return current, fmt.Errorf("radar-completed-at is required when radar-state is done")
	}
	if current.State == "open" && current.CompletedAt != "" {
		return current, fmt.Errorf("radar-completed-at must be empty when radar-state is open")
	}
	if value := current.CompletionBaseline; value != "" && value != "pending" && !validCompletionBaseline.MatchString(value) {
		return current, fmt.Errorf("invalid radar-completion-baseline %q", value)
	}
	if value := values["radar-muted"]; value != nil {
		if value.Kind != yaml.ScalarNode || value.Tag != "!!bool" || value.Decode(&current.Muted) != nil {
			return current, fmt.Errorf("radar-muted must be a YAML boolean")
		}
	}
	if value := values["radar-source-refs"]; value != nil {
		bindings, err := parseBindings(value)
		if err != nil {
			return current, err
		}
		current.Bindings = bindings
	}
	return current, nil
}

func parseBindings(value *yaml.Node) ([]protocol.SourceBinding, error) {
	if value.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("radar-source-refs must be a YAML sequence")
	}
	bindings := make([]protocol.SourceBinding, 0, len(value.Content))
	identities := map[string]bool{}
	for i, item := range value.Content {
		fail := func(reason string) ([]protocol.SourceBinding, error) {
			return nil, fmt.Errorf("radar-source-refs entry %d: %s", i+1, reason)
		}
		if item.Kind != yaml.MappingNode {
			return fail("must be an explicit YAML mapping")
		}
		seen := map[string]bool{}
		for j := 0; j < len(item.Content); j += 2 {
			key, field := item.Content[j], item.Content[j+1]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || seen[key.Value] {
				return fail("invalid or duplicate binding field")
			}
			seen[key.Value] = true
			switch key.Value {
			case "source", "kind", "id", "key":
				if field.Kind != yaml.ScalarNode || field.Tag != "!!str" || !validBindingString(field.Value) {
					return fail(key.Value + " must be a non-empty exact YAML string without control characters")
				}
				if (key.Value == "source" || key.Value == "kind") && strings.Contains(field.Value, ":") {
					return fail(key.Value + " must not contain ':'")
				}
			case "work_item":
				if field.Kind != yaml.ScalarNode || field.Tag != "!!bool" {
					return fail("work_item must be a YAML boolean")
				}
			default:
				return fail("unsupported binding field " + key.Value)
			}
		}
		for _, field := range []string{"source", "kind", "id"} {
			if !seen[field] {
				return fail("missing " + field)
			}
		}
		var binding protocol.SourceBinding
		if err := item.Decode(&binding); err != nil {
			return fail("invalid binding")
		}
		if identities[binding.LinkingKey()] {
			return fail("duplicate source identity")
		}
		identities[binding.LinkingKey()] = true
		bindings = append(bindings, binding)
	}
	return bindings, nil
}

func validBindingString(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && utf8.ValidString(value) && !strings.ContainsFunc(value, unicode.IsControl)
}

func validTimestamp(field, value string, allowEmpty bool) error {
	if value == "" && allowEmpty {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return fmt.Errorf("%s must be an RFC 3339 timestamp", field)
	}
	_, offset := parsed.Zone()
	if offset != 0 {
		return fmt.Errorf("%s must be in UTC", field)
	}
	return nil
}

// inlineComment returns only an unquoted YAML comment. Its whitespace is kept
// along with it; hashes in quoted IDs, URLs and flow mappings are not comments.
func inlineComment(line string) string {
	line = strings.TrimRight(line, "\r\n")
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		if quote != 0 {
			if c == '\\' && quote == '"' {
				i++
			} else if c == quote {
				if quote == '\'' && i+1 < len(line) && line[i+1] == '\'' {
					i++
				} else {
					quote = 0
				}
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
		} else if c == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') {
			start := i
			for start > 0 && (line[start-1] == ' ' || line[start-1] == '\t') {
				start--
			}
			return line[start:]
		}
	}
	return ""
}

// Values are YAML fragments (possibly a multiline sequence), not quoted Go
// strings. Only managed fields may be changed. The complete result is validated
// before any bytes are written.
func updateNoteContent(current note, updates map[string]string) (string, note, error) {
	type replacement struct {
		start, end int
		text       string
	}
	replacements := make([]replacement, 0, len(updates))
	insertions := make([]string, 0)
	fields := make([]string, 0, len(updates))
	for field := range updates {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	for _, field := range fields {
		switch field {
		case "radar-state", "radar-priority", "radar-completed-at", "radar-completion-baseline", "radar-muted", "radar-source-refs":
		default:
			return "", note{}, fmt.Errorf("unsupported managed field %s", field)
		}
		value := updates[field]
		if _, exists := current.fields[field]; exists && unchangedManagedField(current, field, value) {
			continue
		}
		text := field + ":"
		if value != "" {
			if !strings.HasPrefix(value, "\n") {
				text += " "
			}
			text += strings.TrimSuffix(value, "\n")
		}
		span, exists := current.fields[field]
		if !exists {
			if field != "radar-completion-baseline" && field != "radar-muted" && field != "radar-source-refs" {
				return "", note{}, fmt.Errorf("managed field %s is missing from %s", field, current.Path)
			}
			insertions = append(insertions, strings.ReplaceAll(text, "\n", current.newline)+current.newline)
			continue
		}
		// Keep a field's header comment even when replacing a multiline value.
		if span.comment != "" {
			if index := strings.IndexByte(text, '\n'); index >= 0 {
				text = text[:index] + span.comment + text[index:]
			} else {
				text += span.comment
			}
		}
		text = strings.ReplaceAll(text, "\n", current.newline) + current.newline
		replacements = append(replacements, replacement{span.start, span.end, text})
	}
	if len(insertions) > 0 {
		replacements = append(replacements, replacement{current.frontmatterEnd, current.frontmatterEnd, strings.Join(insertions, "")})
	}
	if len(replacements) == 0 {
		return current.content, current, nil
	}
	sort.Slice(replacements, func(i, j int) bool { return replacements[i].start > replacements[j].start })
	content := current.content
	for _, edit := range replacements {
		content = content[:edit.start] + edit.text + content[edit.end:]
	}
	updated, err := parseNote(content)
	if err != nil {
		return "", note{}, fmt.Errorf("updated Obsidian task note is invalid: %w", err)
	}
	updated.Path = current.Path
	return content, updated, nil
}

func unchangedManagedField(current note, field, value string) bool {
	switch field {
	case "radar-state":
		return current.State == value
	case "radar-priority":
		return current.Priority == value
	case "radar-completed-at":
		return current.CompletedAt == value
	case "radar-completion-baseline":
		return current.CompletionBaseline == value
	case "radar-muted", "radar-source-refs":
		var document yaml.Node
		if yaml.Unmarshal([]byte(value), &document) != nil || len(document.Content) != 1 {
			return false
		}
		node := document.Content[0]
		if field == "radar-muted" {
			var muted bool
			return node.Kind == yaml.ScalarNode && node.Tag == "!!bool" && node.Decode(&muted) == nil && muted == current.Muted
		}
		bindings, err := parseBindings(node)
		return err == nil && reflect.DeepEqual(bindings, current.Bindings)
	}
	return false
}
