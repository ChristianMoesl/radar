package obsidian

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"radar/internal/integration/obsidian/settings"
	"radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

// ArchiveCompletedNote is called only after workspace cleanup has succeeded.
// The same lock protects lifecycle changes and new workspace creation.
func (s Source) ArchiveCompletedNote(root, path, identity string) error {
	return workspacegroup.WithNoteLock(root, func() error {
		return s.archiveCompletedNote(root, path, identity)
	})
}

func (s Source) archiveCompletedNote(root, path, identity string) error {
	current, err := s.noteForRef(protocol.SourceRef{
		ID: identity, Source: "obsidian", Kind: "task", Authority: protocol.SourceRefAuthorityPrimary,
		Metadata: map[string]string{"note_path": path},
	})
	if err != nil {
		return err
	}
	_, err = archiveCompleted(root, current)
	return err
}

func archiveCompleted(root string, current note) (note, error) {
	path := current.Path
	if current.State != "done" || settings.IsArchivedNote(current.Path) {
		return current, nil
	}
	referenced, err := noteReferenced(root, current)
	if err != nil || referenced {
		return current, err
	}
	// Moving a note away from accompanying files can break attachments. Leave
	// such tasks completed in their private directory for explicit handling.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return current, err
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		return current, fmt.Errorf("task is done but cannot be archived: %s contains accompanying files; note left in place", filepath.Dir(path))
	}
	if hasRelativeLinks(current.content) {
		return current, fmt.Errorf("task is done but cannot be archived: %s contains relative links; note left in place", path)
	}
	archive := filepath.Join(filepath.Dir(filepath.Dir(path)), settings.ArchiveDirectory)
	if err := ensureRealDirectory(archive); err != nil {
		return current, err
	}
	destination := filepath.Join(archive, filepath.Base(path))
	if err := renameNote(path, destination); err != nil {
		return current, fmt.Errorf("task is done but archiving failed; note left at %s: %w", path, err)
	}
	current.Path = destination
	// Never recursively remove the directory: another process may have added
	// a file since the preflight. A failed rmdir leaves that data untouched.
	if err := os.Remove(filepath.Dir(path)); err != nil {
		return current, fmt.Errorf("note archived at %s, but could not remove its former directory: %w", destination, err)
	}
	return current, nil
}

func (s Source) restoreNote(root string, current note) (string, error) {
	referenced, err := noteReferenced(root, current)
	if err != nil {
		return "", err
	}
	if referenced {
		return "", fmt.Errorf("cannot move archived note while a workspace references it: %s", current.Path)
	}
	if hasRelativeLinks(current.content) {
		return "", fmt.Errorf("cannot restore %s with relative links; note left in place", current.Path)
	}
	directory := filepath.Join(filepath.Dir(filepath.Dir(current.Path)), taskDirectoryName(taskFilename(current.Title), current.ID))
	if err := os.Mkdir(directory, 0o755); err != nil {
		return "", fmt.Errorf("restore private task directory: %w", err)
	}
	destination := filepath.Join(directory, filepath.Base(current.Path))
	if err := renameNote(current.Path, destination); err != nil {
		_ = os.Remove(directory)
		return "", fmt.Errorf("restore archived note: %w", err)
	}
	return destination, nil
}

func noteReferenced(root string, current note) (bool, error) {
	registry, err := workspacegroup.Load(root)
	if err != nil {
		return false, fmt.Errorf("check note workspace references: %w", err)
	}
	for _, workspace := range registry.Workspaces {
		if (workspace.NoteKey() == "obsidian:task:"+current.ID || workspace.TaskLinkingKey == "obsidian:task:"+current.ID) ||
			(workspace.NotePath != "" && filepath.Clean(workspace.NotePath) == filepath.Clean(current.Path)) {
			return true, nil
		}
	}
	return false, nil
}

func ensureRealDirectory(path string) error {
	if err := os.Mkdir(path, 0o755); err != nil && !os.IsExist(err) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("archive must be a real directory: %s", path)
	}
	return nil
}

// Be conservative rather than rewriting user Markdown. Vault-relative wikilinks
// and absolute URLs survive relocation; relative Markdown destinations do not.
var markdownDestinations = regexp.MustCompile(`(?m)\]\(\s*<?([^\s)>]+)|^\s*\[[^\]]+\]:\s*<?([^\s>]+)|(?i:src|href)\s*=\s*["']([^"']+)`)

func hasRelativeLinks(content string) bool {
	for _, match := range markdownDestinations.FindAllStringSubmatch(content, -1) {
		for _, destination := range match[1:] {
			if destination == "" || strings.HasPrefix(destination, "#") || strings.HasPrefix(destination, "/") {
				continue
			}
			parsed, err := url.Parse(destination)
			if err != nil || parsed.Scheme == "" {
				return true
			}
		}
	}
	return strings.Contains(content, "[[./") || strings.Contains(content, "[[../")
}
