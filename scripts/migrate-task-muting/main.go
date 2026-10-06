// One-time, explicit rollout tool. This is not part of Radar's runtime reader.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"

	"radar/internal/integration"
	"radar/internal/integration/obsidian"
	"radar/internal/process"
	"radar/internal/state"
)

type fileChange struct {
	path, relative string
	before, after  []byte
	mode           os.FileMode
}

type migration struct {
	files                           []fileChange
	notes, noteChanges, preferences int
	cacheVersion                    int
	inventory                       taskInventory
}

type byteEdit struct {
	start, end int
	value      []byte
}

func edited(data []byte, edits []byteEdit) []byte {
	out := bytes.Clone(data)
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	for _, edit := range edits {
		out = append(append(append([]byte{}, out[:edit.start]...), edit.value...), out[edit.end:]...)
	}
	return out
}

// Only the top-level key token changes. YAML reserialization would also change
// comments, scalar spellings, newline styles, and user-owned frontmatter.
func migrateNote(data []byte) ([]byte, error) {
	lines := bytes.SplitAfter(data, []byte("\n"))
	if len(lines) < 3 || strings.TrimSpace(string(lines[0])) != "---" {
		return nil, fmt.Errorf("Markdown frontmatter is required")
	}
	end := len(lines[0])
	closed := false
	for _, line := range lines[1:] {
		if strings.TrimRight(string(line), " \t\r\n") == "---" {
			closed = true
			break
		}
		end += len(line)
	}
	if !closed {
		return nil, fmt.Errorf("Markdown frontmatter is not closed")
	}
	frontmatter := data[len(lines[0]):end]
	var document yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(frontmatter))
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("invalid YAML frontmatter: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("frontmatter must contain exactly one YAML document")
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode || document.Content[0].Style&yaml.FlowStyle != 0 {
		return nil, fmt.Errorf("frontmatter must be a block YAML mapping")
	}
	// Decode as well as parse: Node parsing alone does not reject duplicate keys,
	// including duplicates inside otherwise user-owned nested mappings.
	var decoded any
	if err := document.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("invalid YAML frontmatter: %w", err)
	}
	root := document.Content[0]
	keys, values := map[string]*yaml.Node{}, map[string]*yaml.Node{}
	for i := 0; i < len(root.Content); i += 2 {
		key := root.Content[i]
		if key.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("frontmatter keys must be YAML scalars")
		}
		if keys[key.Value] != nil {
			return nil, fmt.Errorf("duplicate frontmatter field %q", key.Value)
		}
		keys[key.Value], values[key.Value] = key, root.Content[i+1]
	}
	if keys["radar-ignored"] != nil && keys["radar-muted"] != nil {
		return nil, fmt.Errorf("radar-ignored and radar-muted collide")
	}
	for _, name := range []string{"radar-ignored", "radar-muted"} {
		if value := values[name]; value != nil {
			var preference bool
			if value.Kind != yaml.ScalarNode || value.Tag != "!!bool" || value.Decode(&preference) != nil {
				return nil, fmt.Errorf("%s must be a YAML boolean", name)
			}
		}
	}
	key := keys["radar-ignored"]
	if key == nil {
		return data, nil
	}
	edit, err := keyEdit(frontmatter, key)
	if err != nil {
		return nil, err
	}
	edit.start += len(lines[0])
	edit.end += len(lines[0])
	return edited(data, []byteEdit{edit}), nil
}

func keyEdit(data []byte, key *yaml.Node) (byteEdit, error) {
	lines := bytes.SplitAfter(data, []byte("\n"))
	if key.Line < 1 || key.Line > len(lines) {
		return byteEdit{}, fmt.Errorf("invalid YAML key position")
	}
	offset := 0
	for _, line := range lines[:key.Line-1] {
		offset += len(line)
	}
	line := lines[key.Line-1]
	column := 0
	for runeColumn := 1; runeColumn < key.Column && column < len(line); runeColumn++ {
		_, size := utf8.DecodeRune(line[column:])
		column += size
	}
	// Tags and anchors are not part of the key's scalar spelling; retain them.
	for column < len(line) && (line[column] == '!' || line[column] == '&') {
		for column < len(line) && line[column] != ' ' && line[column] != '\t' {
			column++
		}
		for column < len(line) && (line[column] == ' ' || line[column] == '\t') {
			column++
		}
	}
	if bytes.HasPrefix(line[column:], []byte("radar-ignored")) {
		return byteEdit{offset + column, offset + column + len("radar-ignored"), []byte("radar-muted")}, nil
	}
	if column < len(line) && (line[column] == '\'' || line[column] == '"') {
		quote := line[column]
		for end := column + 1; end < len(line) && line[end] != '\r' && line[end] != '\n'; end++ {
			if quote == '"' && line[end] == '\\' {
				end++
			} else if line[end] == quote {
				if quote == '\'' && end+1 < len(line) && line[end+1] == quote {
					end++
					continue
				}
				value := append(append([]byte{quote}, []byte("radar-muted")...), quote)
				return byteEdit{offset + column, offset + end + 1, value}, nil
			}
		}
	}
	// Fail closed rather than collapse a multiline key and alter whitespace.
	return byteEdit{}, fmt.Errorf("radar-ignored must use a single-line key")
}

// RawMessage offsets let us change only typed preference key tokens and the
// version number. Unknown fields, number precision, ordering, and all other
// JSON bytes (including metadata named "ignored") are retained verbatim.
type jsonField struct {
	keyStart, keyEnd, valueStart int
	value                        json.RawMessage
}

type jsonElement struct {
	start int
	value json.RawMessage
}

func jsonFields(data []byte, base int) (map[string]jsonField, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("expected a JSON object")
	}
	fields := map[string]jsonField{}
	for decoder.More() {
		start := int(decoder.InputOffset())
		for start < len(data) && strings.ContainsRune(" \t\r\n,", rune(data[start])) {
			start++
		}
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("expected a JSON object key")
		}
		if _, exists := fields[key]; exists {
			return nil, fmt.Errorf("duplicate JSON field %q", key)
		}
		keyEnd := int(decoder.InputOffset())
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		fields[key] = jsonField{base + start, base + keyEnd, base + int(decoder.InputOffset()) - len(value), value}
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected trailing JSON")
	}
	return fields, nil
}

func jsonElements(field jsonField) ([]jsonElement, error) {
	if bytes.Equal(field.value, []byte("null")) {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(field.value))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('[') {
		return nil, fmt.Errorf("expected a JSON array")
	}
	var elements []jsonElement
	for decoder.More() {
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		elements = append(elements, jsonElement{field.valueStart + int(decoder.InputOffset()) - len(value), value})
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	return elements, nil
}

func migrateCache(data []byte) ([]byte, int, int, error) {
	root, err := jsonFields(data, 0)
	if err != nil {
		return nil, 0, 0, err
	}
	field, exists := root["version"]
	var version int
	if !exists || json.Unmarshal(field.value, &version) != nil || (version != 7 && version != 8) {
		return nil, 0, 0, fmt.Errorf("expected cache version 7 or 8")
	}
	var edits []byteEdit
	preference := func(element jsonElement) (map[string]jsonField, error) {
		fields, err := jsonFields(element.value, element.start)
		if err != nil {
			return nil, err
		}
		old, hasOld := fields["ignored"]
		_, hasNew := fields["muted"]
		if hasOld && hasNew {
			return nil, fmt.Errorf("ignored and muted collide")
		}
		for _, name := range []string{"ignored", "muted"} {
			if value, exists := fields[name]; exists && !bytes.Equal(value.value, []byte("true")) && !bytes.Equal(value.value, []byte("false")) {
				return nil, fmt.Errorf("%s must be a JSON boolean", name)
			}
		}
		if hasOld {
			edits = append(edits, byteEdit{old.keyStart, old.keyEnd, []byte(`"muted"`)})
		}
		return fields, nil
	}
	for _, collection := range []string{"task_records", "source_refs"} {
		field, exists := root[collection]
		if !exists {
			continue
		}
		records, err := jsonElements(field)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("%s: %w", collection, err)
		}
		for i, record := range records {
			location := fmt.Sprintf("%s[%d].snapshot", collection, i)
			fields, err := jsonFields(record.value, record.start)
			if err != nil {
				return nil, 0, 0, fmt.Errorf("%s[%d]: %w", collection, i, err)
			}
			snapshot, exists := fields["snapshot"]
			if !exists {
				continue
			}
			fields, err = preference(jsonElement{snapshot.valueStart, snapshot.value})
			if err != nil {
				return nil, 0, 0, fmt.Errorf("%s: %w", location, err)
			}
			if collection != "task_records" {
				continue
			}
			refs, exists := fields["source_refs"]
			if !exists {
				continue
			}
			elements, err := jsonElements(refs)
			if err != nil {
				return nil, 0, 0, fmt.Errorf("%s.source_refs: %w", location, err)
			}
			for j, ref := range elements {
				if _, err := preference(ref); err != nil {
					return nil, 0, 0, fmt.Errorf("%s.source_refs[%d]: %w", location, j, err)
				}
			}
		}
	}
	count := len(edits)
	if version == 8 {
		if count != 0 {
			return nil, 0, 0, fmt.Errorf("version 8 cache still contains legacy ignored preference keys")
		}
		return data, version, 0, nil
	}
	edits = append(edits, byteEdit{field.valueStart, field.valueStart + len(field.value), []byte("8")})
	return edited(data, edits), version, count, nil
}

// Refuse symlinks in every live path component, not just the leaf note. Use
// canonical absolute paths when invoking the tool (e.g. /private/tmp on macOS).
func realPath(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("an absolute path is required: %s", path)
	}
	if filepath.Clean(path) != path {
		return fmt.Errorf("use a clean canonical path without trailing separators or . / .. components: %s", path)
	}
	current := string(filepath.Separator)
	for _, component := range strings.Split(strings.TrimPrefix(path, current), string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlinks are not allowed: %s", current)
		}
	}
	return nil
}

func readChange(path, relative string) (fileChange, error) {
	if err := realPath(path); err != nil {
		return fileChange{}, err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return fileChange{}, fmt.Errorf("not a regular file: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fileChange{}, err
	}
	return fileChange{path: path, relative: relative, before: data, mode: info.Mode()}, nil
}

// Loading the staged cache validates all production types, not just the fields
// being renamed. Never let NewStore read configured live state. This standalone
// tool is synchronous, so a temporary environment override is safe.
func validateCache(stage string, data []byte) (err error) {
	var expected struct {
		Records []json.RawMessage `json:"task_records"`
		Refs    []json.RawMessage `json:"source_refs"`
	}
	if err := json.Unmarshal(data, &expected); err != nil {
		return err
	}
	path := filepath.Join(stage, "tasks.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	previous, present := os.LookupEnv("RADAR_STATE")
	if err := os.Setenv("RADAR_STATE", path); err != nil {
		return err
	}
	defer func() {
		var restoreErr error
		if present {
			restoreErr = os.Setenv("RADAR_STATE", previous)
		} else {
			restoreErr = os.Unsetenv("RADAR_STATE")
		}
		if restoreErr != nil {
			err = fmt.Errorf("restore RADAR_STATE: %w (validation error: %v)", restoreErr, err)
		}
	}()
	store, err := state.NewStore(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return fmt.Errorf("migrated cache failed production validation: %w", err)
	}
	records, refs := len(store.Records()), len(store.SourceRefs())
	if records != len(expected.Records) || refs != len(expected.Refs) {
		return fmt.Errorf("migrated cache failed production validation: expected %d records/%d refs, loaded %d/%d (cache discarded or records lost)", len(expected.Records), len(expected.Refs), records, refs)
	}
	return nil
}

type layoutEntry struct {
	relative string
	mode     os.FileMode
}

type taskInventory struct {
	root    string
	entries []layoutEntry
}

// Inventory only Tasks and its immediate private/archive children. Do not scan
// the rest of the vault or recurse into attachments. A missing Tasks root is
// distinct from an existing empty root, so later creation also fails closed.
func inventoryTasks(root string) (taskInventory, error) {
	inventory := taskInventory{root: root}
	if err := realPath(root); err != nil {
		if os.IsNotExist(err) {
			return inventory, nil
		}
		return inventory, err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return inventory, err
	}
	if !info.IsDir() {
		return inventory, fmt.Errorf("Tasks must be a real directory: %s", root)
	}
	inventory.entries = append(inventory.entries, layoutEntry{".", info.Mode()})
	entries, err := os.ReadDir(root)
	if err != nil {
		return inventory, err
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return inventory, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return inventory, fmt.Errorf("unexpected symlink in Tasks: %s", entry.Name())
		}
		inventory.entries = append(inventory.entries, layoutEntry{entry.Name(), info.Mode()})
		if !info.IsDir() {
			continue
		}
		children, err := os.ReadDir(filepath.Join(root, entry.Name()))
		if err != nil {
			return inventory, err
		}
		for _, child := range children {
			info, err := child.Info()
			if err != nil {
				return inventory, err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return inventory, fmt.Errorf("unexpected symlink in task directory: %s", child.Name())
			}
			inventory.entries = append(inventory.entries, layoutEntry{filepath.Join(entry.Name(), child.Name()), info.Mode()})
		}
	}
	return inventory, nil
}

func (inventory taskInventory) unchanged() error {
	current, err := inventoryTasks(inventory.root)
	if err != nil {
		return fmt.Errorf("task layout changed since preflight: %s: %w", inventory.root, err)
	}
	if !slices.Equal(inventory.entries, current.entries) {
		return fmt.Errorf("task layout changed since preflight: %s", inventory.root)
	}
	return nil
}

// Only the temporary vault is passed to production Collect: its configured
// vault validation can create Tasks, which must never happen in live preflight.
func preflight(vault, statePath string) (migration, error) {
	var plan migration
	if err := realPath(vault); err != nil {
		return plan, err
	}
	if err := realPath(filepath.Join(vault, ".obsidian")); err != nil {
		return plan, fmt.Errorf("vault must contain a real .obsidian directory: %w", err)
	}
	if info, err := os.Lstat(filepath.Join(vault, ".obsidian")); err != nil || !info.IsDir() {
		return plan, fmt.Errorf("vault must contain .obsidian")
	}
	inventory, err := inventoryTasks(filepath.Join(vault, "Tasks"))
	if err != nil {
		return plan, err
	}
	return stagePreflight(vault, statePath, inventory)
}

func stagePreflight(vault, statePath string, inventory taskInventory) (migration, error) {
	plan := migration{inventory: inventory}
	stage, err := os.MkdirTemp("", "radar-muting-preflight-")
	if err != nil {
		return plan, err
	}
	defer os.RemoveAll(stage)
	for _, dir := range []string{".obsidian", "Tasks"} {
		if err := os.Mkdir(filepath.Join(stage, dir), 0o700); err != nil {
			return plan, err
		}
	}
	root := filepath.Join(vault, "Tasks")
	if err := realPath(root); err != nil && !os.IsNotExist(err) {
		return plan, err
	}
	entries, err := os.ReadDir(root)
	if err != nil && !os.IsNotExist(err) {
		return plan, err
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return plan, fmt.Errorf("unexpected symlink in Tasks: %s", entry.Name())
		}
		if !entry.IsDir() {
			if strings.EqualFold(filepath.Ext(entry.Name()), ".md") || entry.Name() == "Archived" {
				return plan, fmt.Errorf("unexpected task layout: %s", entry.Name())
			}
			continue
		}
		children, err := os.ReadDir(filepath.Join(root, entry.Name()))
		if err != nil {
			return plan, err
		}
		if err := os.Mkdir(filepath.Join(stage, "Tasks", entry.Name()), 0o700); err != nil {
			return plan, err
		}
		for _, child := range children {
			if child.Type()&os.ModeSymlink != 0 {
				return plan, fmt.Errorf("unexpected symlink in task directory: %s", child.Name())
			}
			if child.IsDir() && entry.Name() == "Archived" {
				return plan, fmt.Errorf("archive must be flat: %s", child.Name())
			}
			if !strings.EqualFold(filepath.Ext(child.Name()), ".md") {
				continue
			}
			relative := filepath.Join("Tasks", entry.Name(), child.Name())
			change, err := readChange(filepath.Join(vault, relative), relative)
			if err != nil {
				return plan, err
			}
			change.after, err = migrateNote(change.before)
			if err != nil {
				return plan, fmt.Errorf("%s: %w", change.path, err)
			}
			if err := os.WriteFile(filepath.Join(stage, relative), change.after, 0o600); err != nil {
				return plan, err
			}
			plan.files = append(plan.files, change)
			plan.notes++
			if !bytes.Equal(change.before, change.after) {
				plan.noteChanges++
			}
		}
	}
	result := obsidian.NewSourceAt(stage).Collect(context.Background(), integration.CollectRequest{})
	if !result.Complete {
		return plan, fmt.Errorf("migrated notes failed production validation: %s", result.SourceStatus.Detail)
	}
	cache, err := readChange(statePath, "tasks.json")
	if err != nil {
		return plan, err
	}
	cache.after, plan.cacheVersion, plan.preferences, err = migrateCache(cache.before)
	if err != nil {
		return plan, fmt.Errorf("%s: %w", statePath, err)
	}
	if err := validateCache(stage, cache.after); err != nil {
		return plan, fmt.Errorf("%s: %w", statePath, err)
	}
	plan.files = append(plan.files, cache)
	if err := plan.inventory.unchanged(); err != nil {
		return plan, err
	}
	return plan, nil
}

func unchanged(change fileChange) error {
	if err := realPath(change.path); err != nil {
		return fmt.Errorf("file changed since preflight: %s: %w", change.path, err)
	}
	info, err := os.Lstat(change.path)
	if err != nil || !info.Mode().IsRegular() || info.Mode() != change.mode {
		return fmt.Errorf("file changed since preflight (type or permissions): %s", change.path)
	}
	data, err := os.ReadFile(change.path)
	if err != nil || !bytes.Equal(data, change.before) {
		return fmt.Errorf("file changed since preflight: %s", change.path)
	}
	return nil
}

func writeBackup(path string, change fileChange) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(change.before); err != nil {
		return err
	}
	if err := file.Chmod(change.mode); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return file.Close()
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// replaced records a successful rename even if syncing the directory fails.
func replace(change fileChange) (replaced bool, err error) {
	file, err := os.CreateTemp(filepath.Dir(change.path), ".radar-muting-*")
	if err != nil {
		return false, err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(change.after); err != nil {
		return false, err
	}
	if err := file.Chmod(change.mode); err != nil {
		return false, err
	}
	if err := file.Sync(); err != nil {
		return false, err
	}
	if err := file.Close(); err != nil {
		return false, err
	}
	if err := unchanged(change); err != nil {
		return false, err
	}
	if err := os.Rename(file.Name(), change.path); err != nil {
		return false, err
	}
	return true, syncDirectory(filepath.Dir(change.path))
}

// Runtime discovery deliberately tolerates failed ps with a stale PID file.
// Migration cannot: require a successful, parseable enumeration independently
// and combine it with the production detector without changing runtime policy.
func strictDaemonPIDs() ([]int, error) {
	pids, err := process.DaemonPIDs()
	if err != nil {
		return nil, err
	}
	output, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return nil, fmt.Errorf("strict ps enumeration failed: %w", err)
	}
	seen := map[int]bool{}
	for _, pid := range pids {
		seen[pid] = true
	}
	rows := 0
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 2 {
			return nil, fmt.Errorf("strict ps enumeration contains an incomplete process row")
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			return nil, fmt.Errorf("strict ps enumeration contains an invalid PID")
		}
		rows++
		name := filepath.Base(fields[1])
		if pid != os.Getpid() && !seen[pid] && (name == "radar" || strings.HasPrefix(name, "radar-")) && slices.Contains(fields[2:], "daemon") {
			pids = append(pids, pid)
			seen[pid] = true
		}
	}
	if rows == 0 {
		return nil, fmt.Errorf("strict ps enumeration returned no processes")
	}
	sort.Ints(pids)
	return pids, nil
}

func requireStopped(daemonPIDs func() ([]int, error)) error {
	pids, err := daemonPIDs()
	if err != nil {
		return fmt.Errorf("cannot check daemon processes: %w", err)
	}
	if len(pids) != 0 {
		return fmt.Errorf("stop Radar externally before applying; daemon PIDs: %v", pids)
	}
	return nil
}

func apply(plan migration, backup string, daemonPIDs func() ([]int, error)) (err error) {
	if !filepath.IsAbs(backup) {
		return fmt.Errorf("apply requires an absolute, new backup directory")
	}
	if err := realPath(filepath.Dir(backup)); err != nil {
		return fmt.Errorf("backup parent must already exist without symlinks: %w", err)
	}
	if err := requireStopped(daemonPIDs); err != nil {
		return err
	}
	if err := plan.inventory.unchanged(); err != nil {
		return err
	}
	if err := os.Mkdir(backup, 0o700); err != nil {
		return err
	}
	written, total := 0, 0
	for _, change := range plan.files {
		if !bytes.Equal(change.before, change.after) {
			total++
		}
	}
	defer func() {
		if err != nil {
			err = fmt.Errorf("apply incomplete: %d/%d files replaced; backup retained at %s: %w", written, total, backup, err)
		}
	}()
	// Back up every input (even noops) before the first live replacement. A
	// second pass detects edits to earlier inputs while backups were written.
	for _, change := range plan.files {
		if err := unchanged(change); err != nil {
			return err
		}
		if err := writeBackup(filepath.Join(backup, change.relative), change); err != nil {
			return err
		}
	}
	if err := filepath.WalkDir(backup, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return syncDirectory(path)
		}
		return nil
	}); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(backup)); err != nil {
		return err
	}
	if err := requireStopped(daemonPIDs); err != nil {
		return err
	}
	if err := plan.inventory.unchanged(); err != nil {
		return err
	}
	for _, change := range plan.files {
		if err := unchanged(change); err != nil {
			return err
		}
	}
	for _, change := range plan.files {
		if bytes.Equal(change.before, change.after) {
			continue
		}
		if err := requireStopped(daemonPIDs); err != nil {
			return err
		}
		if err := plan.inventory.unchanged(); err != nil {
			return err
		}
		replaced, err := replace(change)
		if replaced {
			written++
		}
		if err != nil {
			return err
		}
	}
	return plan.inventory.unchanged()
}

func inside(parent, path string) bool {
	relative, err := filepath.Rel(parent, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func run(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("migrate-task-muting", flag.ContinueOnError)
	flags.SetOutput(output)
	vault := flags.String("vault", "", "absolute Obsidian vault path (required)")
	state := flags.String("state", "", "absolute task-cache JSON path (required)")
	write := flags.Bool("apply", false, "apply; default is read-only preflight")
	backup := flags.String("backup", "", "new absolute backup directory outside the vault; its parent must exist (required for apply)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || !filepath.IsAbs(*vault) || !filepath.IsAbs(*state) {
		return fmt.Errorf("explicit absolute --vault and --state paths are required; no positional arguments")
	}
	if *write {
		if !filepath.IsAbs(*backup) || filepath.Clean(*backup) != *backup || inside(*vault, *backup) {
			return fmt.Errorf("apply requires a new absolute --backup directory outside the vault")
		}
		if err := requireStopped(strictDaemonPIDs); err != nil {
			return err
		}
	}
	plan, err := preflight(*vault, *state)
	if err != nil {
		return err
	}
	for _, change := range plan.files {
		if !bytes.Equal(change.before, change.after) {
			fmt.Fprintf(output, "MIGRATE %s\n", change.path)
		}
	}
	fmt.Fprintf(output, "Validated %d managed notes; %d need radar-muted. Cache version %d -> 8; %d typed preferences need muted.\n", plan.notes, plan.noteChanges, plan.cacheVersion, plan.preferences)
	if !*write {
		fmt.Fprintln(output, "Read-only preflight; no live files or backup directories created.")
		return nil
	}
	if err := apply(plan, *backup, strictDaemonPIDs); err != nil {
		return err
	}
	fmt.Fprintf(output, "Applied migration; all original inputs backed up in %s. Keep Radar stopped until the mute-aware binary is installed.\n", *backup)
	return nil
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
