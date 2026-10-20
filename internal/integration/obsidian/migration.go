// Explicit one-time migration support. Runtime collection does not call these readers.
package obsidian

import (
	"fmt"
	"go.yaml.in/yaml/v3"
	"os"
	"path/filepath"
	"radar/internal/integration/obsidian/settings"
	"radar/internal/taskdb"
	"sort"
	"strings"
)

type LegacyNote struct {
	Task          taskdb.Task
	Before, After []byte
	Mode          os.FileMode
}

func readLegacyNote(path string) (note, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return note{}, err
	}
	if !info.Mode().IsRegular() {
		return note{}, fmt.Errorf("legacy task note must be regular: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return note{}, err
	}
	current, err := parseNote(string(data))
	current.Path = path
	if _, old := current.fields["radar-ignored"]; old {
		return current, fmt.Errorf("old ignore frontmatter requires its schema-7 migration before SQLite import")
	}
	return current, err
}
func PlanLegacy(vault string) ([]LegacyNote, error) {
	entries, err := os.ReadDir(taskRoot(vault))
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("task root contains a symlink: %s", entry.Name())
		}
	}
	items, err := discoverLegacy(vault)
	if err != nil {
		return nil, err
	}
	validateNoteOwnership(items)
	var plan []LegacyNote
	for _, item := range items {
		if item.err != nil {
			return nil, fmt.Errorf("%s: %w", item.path, item.err)
		}
		current := item.note
		if err := validateRemovalAliases(current.content); err != nil {
			return nil, fmt.Errorf("%s: %w", item.path, err)
		}
		current.Vault = filepath.Clean(vault)
		_, current.Adopted = current.fields["radar-source-refs"]
		fields := make([]fieldRange, 0)
		for _, key := range legacyFields {
			if field, ok := current.fields[key]; ok {
				fields = append(fields, field)
			}
		}
		sort.Slice(fields, func(i, j int) bool { return fields[i].start > fields[j].start })
		after := current.content
		for _, field := range fields {
			after = after[:field.start] + after[field.end:]
		}
		// Empty YAML delimiters have no user content and can disappear too.
		lines := strings.SplitAfter(after, "\n")
		if len(lines) >= 2 && strings.TrimSpace(lines[0]) == "---" && strings.TrimSpace(lines[1]) == "---" {
			after = strings.Join(lines[2:], "")
		}
		info, err := os.Lstat(item.path)
		if err != nil {
			return nil, err
		}
		plan = append(plan, LegacyNote{Task: current.Task, Before: []byte(current.content), After: []byte(after), Mode: info.Mode().Perm()})
	}
	return plan, nil
}
func discoverLegacy(vault string) ([]discoveredNote, error) {
	root := taskRoot(vault)
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read Obsidian task root %s: %w", root, err)
	}
	items := make([]discoveredNote, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			if entry.Name() == settings.ArchiveDirectory {
				items = append(items, discoveredNote{err: fmt.Errorf("archive must be a real directory"), path: filepath.Join(root, entry.Name())})
			}
			continue
		}
		directory := filepath.Join(root, entry.Name())
		if entry.Name() == settings.ArchiveDirectory {
			children, readErr := os.ReadDir(directory)
			if readErr != nil {
				items = append(items, discoveredNote{err: readErr, path: directory})
				continue
			}
			for _, child := range children {
				path := filepath.Join(directory, child.Name())
				if child.IsDir() {
					items = append(items, discoveredNote{err: fmt.Errorf("archive must contain flat Markdown notes"), path: path})
				} else if strings.EqualFold(filepath.Ext(child.Name()), ".md") {
					current, noteErr := readLegacyNote(path)
					items = append(items, discoveredNote{note: current, err: noteErr, path: path})
				}
			}
			continue
		}
		children, readErr := os.ReadDir(directory)
		if readErr != nil {
			items = append(items, discoveredNote{err: readErr, path: directory})
			continue
		}
		notes := make([]string, 0, 1)
		for _, child := range children {
			if !child.IsDir() && strings.EqualFold(filepath.Ext(child.Name()), ".md") {
				notes = append(notes, filepath.Join(directory, child.Name()))
			}
		}
		if len(notes) != 1 {
			items = append(items, discoveredNote{err: fmt.Errorf("task directory must contain exactly one Markdown note"), path: directory})
			continue
		}
		path := notes[0]
		current, noteErr := readLegacyNote(path)
		directorySuffix := "--" + shortID(current.ID)
		if noteErr == nil && (!strings.HasSuffix(filepath.Base(directory), directorySuffix) || strings.TrimSuffix(filepath.Base(directory), directorySuffix) == "") {
			noteErr = fmt.Errorf("task directory must end with %s", directorySuffix)
		}
		items = append(items, discoveredNote{note: current, err: noteErr, path: path})
	}
	return items, nil
}

func validateRemovalAliases(content string) error {
	lines := strings.Split(content, "\n")
	end := 1
	for end < len(lines) && strings.TrimSpace(lines[end]) != "---" {
		end++
	}
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &document); err != nil {
		return err
	}
	root := document.Content[0]
	removed := map[*yaml.Node]bool{}
	managed := map[string]bool{}
	for _, field := range legacyFields {
		managed[field] = true
	}
	var mark func(*yaml.Node)
	mark = func(node *yaml.Node) {
		removed[node] = true
		for _, child := range node.Content {
			mark(child)
		}
	}
	for i := 0; i < len(root.Content); i += 2 {
		if managed[root.Content[i].Value] {
			mark(root.Content[i])
			mark(root.Content[i+1])
		}
	}
	var check func(*yaml.Node) error
	check = func(node *yaml.Node) error {
		if node.Kind == yaml.AliasNode && removed[node.Alias] {
			return fmt.Errorf("user frontmatter refers to an anchor in removed Radar state; resolve the alias before migration")
		}
		for _, child := range node.Content {
			if err := check(child); err != nil {
				return err
			}
		}
		return nil
	}
	for i := 0; i < len(root.Content); i += 2 {
		if !managed[root.Content[i].Value] {
			if err := check(root.Content[i+1]); err != nil {
				return err
			}
		}
	}
	return nil
}
