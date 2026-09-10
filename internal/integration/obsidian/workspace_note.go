package obsidian

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"radar/internal/integration"
	"radar/internal/integration/obsidian/settings"
)

// PrepareWorkspaceNote chooses a stable identity without creating a task file.
func (s Source) PrepareWorkspaceNote(ctx context.Context, title string) (integration.DesiredWorkspaceNote, error) {
	title = strings.TrimSpace(title)
	if title == "" || !utf8.ValidString(title) {
		return integration.DesiredWorkspaceNote{}, fmt.Errorf("task title must be non-empty UTF-8")
	}
	filename := taskFilename(title)
	vault, err := s.configuredVault()
	if err != nil {
		return integration.DesiredWorkspaceNote{}, err
	}
	id, err := newUUID()
	if err != nil {
		return integration.DesiredWorkspaceNote{}, err
	}
	note := integration.DesiredWorkspaceNote{Create: true, Title: title, Path: filepath.Join(taskRoot(vault), taskDirectoryName(filename, id), filename+".md"), LinkingKey: "obsidian:task:" + id}
	return note, s.ValidateWorkspaceNote(ctx, note)
}

func (s Source) ValidateWorkspaceNote(_ context.Context, desired integration.DesiredWorkspaceNote) error {
	if err := settings.ValidateWorkspaceNote(desired.Path); err != nil {
		return err
	}
	vault, err := s.configuredVault()
	if err != nil {
		return err
	}
	id := strings.TrimPrefix(desired.LinkingKey, "obsidian:task:")
	if id == desired.LinkingKey || !validID.MatchString(id) || !validManagedNotePath(vault, desired.Path) {
		return fmt.Errorf("invalid canonical Obsidian note identity or path")
	}
	directory := filepath.Dir(desired.Path)
	if info, err := os.Lstat(directory); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("canonical task directory must be a real directory")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if !strings.HasSuffix(filepath.Base(directory), "--"+shortID(id)) {
		return fmt.Errorf("canonical task directory does not match note identity")
	}
	current, err := readNote(desired.Path)
	if err == nil {
		if current.ID != id {
			return fmt.Errorf("canonical note identity does not match %s", desired.LinkingKey)
		}
		return nil
	}
	if !os.IsNotExist(err) || !desired.Create {
		return err
	}
	title := desired.Title
	if title == "" || title != strings.TrimSpace(title) || !utf8.ValidString(title) {
		return fmt.Errorf("planned task note requires a non-empty UTF-8 title without surrounding whitespace")
	}
	filename := taskFilename(title)
	if filepath.Base(desired.Path) != filename+".md" || filepath.Base(filepath.Dir(desired.Path)) != taskDirectoryName(filename, id) {
		return fmt.Errorf("invalid planned task note path")
	}
	if _, err := os.Lstat(filepath.Dir(desired.Path)); err == nil {
		return fmt.Errorf("planned task directory is occupied")
	} else if !os.IsNotExist(err) {
		return err
	}
	discovered, err := discover(vault)
	if err != nil {
		return err
	}
	for _, item := range discovered {
		if item.note.Title == title {
			return fmt.Errorf("task title %q already exists", title)
		}
		// The flat archive shares a filename namespace, even for distinct titles.
		if filepath.Base(item.path) == filename+".md" {
			return fmt.Errorf("task filename %q already exists", filename)
		}
	}
	return nil
}

func (s Source) EnsureWorkspaceNote(ctx context.Context, desired integration.DesiredWorkspaceNote) error {
	if err := s.ValidateWorkspaceNote(ctx, desired); err != nil {
		return err
	}
	if _, err := os.Stat(desired.Path); err == nil {
		return nil
	}
	directory := filepath.Dir(desired.Path)
	if err := os.Mkdir(directory, 0o755); err != nil {
		return err
	}
	id := strings.TrimPrefix(desired.LinkingKey, "obsidian:task:")
	// JSON strings are valid YAML scalars and safely quote punctuation and controls.
	title, err := json.Marshal(desired.Title)
	if err != nil {
		return err
	}
	content := fmt.Sprintf("---\nradar-id: %s\nradar-title: %s\nradar-state: open\nradar-priority: normal\nradar-created-at: %s\nradar-completed-at:\n---\n", id, title, time.Now().UTC().Format(time.RFC3339))
	if err := atomicCreate(desired.Path, []byte(content), 0o644); err != nil {
		_ = os.Remove(directory)
		return err
	}
	return nil
}
