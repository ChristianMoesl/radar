package obsidian

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"radar/internal/integration"
	"radar/internal/integration/obsidian/settings"
	"radar/internal/taskdb"
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
	if err := RequireMigratedNotes(vault); err != nil {
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
		owned, e := taskdb.Find(id)
		if e != nil || owned.Path != desired.Path {
			return fmt.Errorf("planned task directory is occupied")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	discovered, err := discover(vault)
	if err != nil {
		return err
	}
	for _, item := range discovered {
		if item.note.Title == title && item.note.ID != id {
			return fmt.Errorf("task title %q already exists", title)
		}
		// The flat archive shares a filename namespace, even for distinct titles.
		if filepath.Base(item.path) == filename+".md" && item.note.ID != id {
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
	return createRegisteredNote(desired, "", newTaskNote(desired))
}

func newTaskNote(desired integration.DesiredWorkspaceNote) note {
	return note{Task: taskdb.Task{ID: strings.TrimPrefix(desired.LinkingKey, "obsidian:task:"), Title: desired.Title, State: "open", Priority: "normal", CreatedAt: time.Now().UTC().Format(time.RFC3339), Path: desired.Path, Vault: filepath.Dir(filepath.Dir(filepath.Dir(desired.Path)))}, fields: map[string]fieldRange{}}
}

func createRegisteredNote(desired integration.DesiredWorkspaceNote, content string, current note) error {
	// Commit identity before creating content. If the process stops here the
	// durable association remains and creation can resume with the same identity.
	existing, err := taskdb.Find(current.ID)
	if err == nil {
		if existing.Path != desired.Path || existing.Vault != current.Vault {
			return fmt.Errorf("prepared task association changed")
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		if err := taskdb.Put(&current.Task); err != nil {
			return err
		}
	} else {
		return err
	}
	return createWorkspaceNote(desired, content)
}

func createWorkspaceNote(desired integration.DesiredWorkspaceNote, content string) error {
	directory := filepath.Dir(desired.Path)
	if err := os.Mkdir(directory, 0o755); err != nil && !os.IsExist(err) {
		return err
	}
	if err := atomicCreate(desired.Path, []byte(content), 0o644); err != nil {
		_ = os.Remove(directory)
		return err
	}
	return nil
}
