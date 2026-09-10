// One-time, explicit rollout tool. This is not part of Radar's runtime reader.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"

	"radar/internal/integration"
	"radar/internal/integration/obsidian"
	workspacegroup "radar/internal/integration/workspace/group"
)

type noteChange struct {
	path, relative, title string
	before, after         []byte
	mode                  os.FileMode
}

// Guess only a word-ending hyphen followed by whitespace. Preserve ticket keys,
// hyphenated words, arrows, double hyphens, and spaced dash separators.
var likelyColon = regexp.MustCompile(`([\pL\pN])-([ \t]+)`)

func migrateTitle(path string, data []byte) ([]byte, string, error) {
	lines := bytes.SplitAfter(data, []byte("\n"))
	if len(lines) < 3 || strings.TrimSpace(string(lines[0])) != "---" {
		return nil, "", fmt.Errorf("Markdown frontmatter is required")
	}
	end := len(lines[0])
	closed := false
	for _, line := range lines[1:] {
		if strings.TrimSpace(string(line)) == "---" {
			closed = true
			break
		}
		end += len(line)
	}
	if !closed {
		return nil, "", fmt.Errorf("Markdown frontmatter is not closed")
	}
	var fields map[string]yaml.Node
	if err := yaml.Unmarshal(data[len(lines[0]):end], &fields); err != nil {
		return nil, "", fmt.Errorf("invalid YAML frontmatter")
	}
	if _, exists := fields["radar-title"]; exists {
		return data, "", nil // Never guess over an explicitly authored title.
	}
	title := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	title = likelyColon.ReplaceAllString(title, "${1}:${2}")
	encoded, err := json.Marshal(title)
	if err != nil {
		return nil, "", err
	}
	newline := "\n"
	if bytes.HasSuffix(lines[0], []byte("\r\n")) {
		newline = "\r\n"
	}
	insert := append([]byte("radar-title: "), encoded...)
	insert = append(insert, newline...)
	out := append([]byte{}, lines[0]...)
	out = append(out, insert...)
	out = append(out, data[len(lines[0]):]...)
	return out, title, nil
}

// Stage every managed note and validate with the new production reader before
// changing any real file. Duplicate IDs/titles and malformed layouts abort the
// entire preflight. The live vault is read-only during this step.
func preflight(vault string) ([]noteChange, error) {
	info, err := os.Stat(filepath.Join(vault, ".obsidian"))
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("vault must contain .obsidian")
	}
	root := filepath.Join(vault, "Tasks")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp("", "radar-title-preflight-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	for _, dir := range []string{".obsidian", "Tasks"} {
		if err := os.Mkdir(filepath.Join(stage, dir), 0o700); err != nil {
			return nil, err
		}
	}
	var changes []noteChange
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("unexpected symlink in Tasks: %s", entry.Name())
		}
		if !entry.IsDir() {
			if strings.EqualFold(filepath.Ext(entry.Name()), ".md") || entry.Name() == "Archived" {
				return nil, fmt.Errorf("unexpected task layout: %s", entry.Name())
			}
			continue
		}
		directory := filepath.Join(root, entry.Name())
		children, err := os.ReadDir(directory)
		if err != nil {
			return nil, err
		}
		if err := os.Mkdir(filepath.Join(stage, "Tasks", entry.Name()), 0o700); err != nil {
			return nil, err
		}
		for _, child := range children {
			if child.IsDir() && entry.Name() == "Archived" {
				return nil, fmt.Errorf("archive must be flat: %s", child.Name())
			}
			if !strings.EqualFold(filepath.Ext(child.Name()), ".md") {
				continue
			}
			path := filepath.Join(directory, child.Name())
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() {
				return nil, fmt.Errorf("not a regular note: %s", path)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			after, title, err := migrateTitle(path, data)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			relative := filepath.Join(entry.Name(), child.Name())
			if err := os.WriteFile(filepath.Join(stage, "Tasks", relative), after, 0o600); err != nil {
				return nil, err
			}
			changes = append(changes, noteChange{path, relative, title, data, after, info.Mode().Perm()})
		}
	}
	result := obsidian.NewSourceAt(stage).Collect(context.Background(), integration.CollectRequest{})
	if !result.Complete {
		return nil, fmt.Errorf("migrated notes failed validation: %s", result.SourceStatus.Detail)
	}
	return changes, nil
}

func apply(changes []noteChange, backup string) error {
	if !filepath.IsAbs(backup) {
		return fmt.Errorf("apply requires an absolute, new backup directory")
	}
	if err := os.MkdirAll(filepath.Dir(backup), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(backup, 0o700); err != nil {
		return err
	}
	// Back up every note, including ones already migrated, before any writes or
	// optional archival. Refuse concurrent edits instead of overwriting them.
	for _, change := range changes {
		if err := unchanged(change); err != nil {
			return err
		}
		path := filepath.Join(backup, change.relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, change.before, change.mode); err != nil {
			return err
		}
	}
	for _, change := range changes {
		if bytes.Equal(change.before, change.after) {
			continue
		}
		if err := unchanged(change); err != nil {
			return err
		}
		if err := replace(change); err != nil {
			return err
		}
	}
	return nil
}

func unchanged(change noteChange) error {
	info, err := os.Lstat(change.path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != change.mode {
		return fmt.Errorf("note changed since preflight: %s", change.path)
	}
	data, err := os.ReadFile(change.path)
	if err != nil || !bytes.Equal(data, change.before) {
		return fmt.Errorf("note changed since preflight: %s", change.path)
	}
	return nil
}

func replace(change noteChange) error {
	file, err := os.CreateTemp(filepath.Dir(change.path), ".radar-title-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(change.mode); err != nil {
		return err
	}
	if _, err := file.Write(change.after); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := unchanged(change); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), change.path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(change.path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func run() error {
	vault := flag.String("vault", "", "absolute Obsidian vault path")
	root := flag.String("workspace-root", "", "absolute configured workspace root (required for apply)")
	write := flag.Bool("apply", false, "apply the migration; default is read-only preflight")
	backup := flag.String("backup", "", "new absolute backup directory outside the vault (required for apply)")
	archive := flag.Bool("archive-done", false, "after migration, archive unreferenced done notes using Radar's safety checks")
	flag.Parse()
	if !filepath.IsAbs(*vault) {
		return fmt.Errorf("an absolute -vault is required")
	}
	changes, err := preflight(*vault)
	if err != nil {
		return err
	}
	count := 0
	for _, change := range changes {
		if !bytes.Equal(change.before, change.after) {
			fmt.Printf("TITLE %s -> %q\n", change.relative, change.title)
			count++
		}
	}
	fmt.Printf("Validated %d notes; %d need title metadata.\n", len(changes), count)
	if !*write {
		fmt.Println("Read-only preflight; no notes changed. Archival is checked on apply.")
		return nil
	}
	if !filepath.IsAbs(*root) || !filepath.IsAbs(*backup) {
		return fmt.Errorf("apply requires absolute -workspace-root and -backup")
	}
	relative, err := filepath.Rel(*vault, *backup)
	if err != nil || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return fmt.Errorf("backup must be outside the vault")
	}
	if err := workspacegroup.WithNoteLock(*root, func() error { return apply(changes, *backup) }); err != nil {
		return err
	}
	fmt.Printf("Migrated %d titles; original notes backed up in %s\n", count, *backup)
	source := obsidian.NewSourceAt(*vault)
	result := source.Collect(context.Background(), integration.CollectRequest{})
	if !result.Complete {
		return fmt.Errorf("post-migration validation failed: %s", result.SourceStatus.Detail)
	}
	if *archive {
		for _, observation := range result.Observations {
			ref := observation.Ref
			path := ref.Metadata["note_path"]
			if ref.Status != "done" || filepath.Base(filepath.Dir(path)) == "Archived" {
				continue
			}
			if err := source.ArchiveCompletedNote(*root, path, ref.ID); err != nil {
				fmt.Printf("LEFT IN PLACE %s: %v\n", path, err)
			} else if _, err := os.Lstat(path); os.IsNotExist(err) {
				fmt.Printf("ARCHIVED %s\n", path)
			} else {
				fmt.Printf("LEFT IN PLACE (workspace reference) %s\n", path)
			}
		}
	}
	result = source.Collect(context.Background(), integration.CollectRequest{})
	if !result.Complete {
		return fmt.Errorf("final validation failed: %s", result.SourceStatus.Detail)
	}
	fmt.Printf("Final validation: %d valid notes.\n", len(result.Observations))
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
