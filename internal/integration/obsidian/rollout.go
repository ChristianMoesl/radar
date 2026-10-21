package obsidian

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/taskdb"
	"strings"
)

var ErrUnmigratedNotes = errors.New("existing task frontmatter requires migration")

// RequireMigratedNotes detects old task records without interpreting their
// state. Only the one-time importer may read legacy operational frontmatter.
func RequireMigratedNotes(vault string) error {
	if vault == "" {
		return nil
	}
	root := taskRoot(vault)
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("task root contains a symlink: %s", entry.Name())
		}
		if !entry.IsDir() {
			continue
		}
		children, err := os.ReadDir(filepath.Join(root, entry.Name()))
		if err != nil {
			return err
		}
		for _, child := range children {
			if child.IsDir() || !strings.EqualFold(filepath.Ext(child.Name()), ".md") {
				continue
			}
			path := filepath.Join(root, entry.Name(), child.Name())
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("task note must be a regular file: %s", path)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if hasLegacyFields(string(data)) {
				return fmt.Errorf("%w: restart Radar to migrate automatically; use radar migrate-state for inspection (%s)", ErrUnmigratedNotes, path)
			}
		}
	}
	return nil
}

func RecoverTaskMoves() error {
	root, err := workspacegroup.DefaultRoot()
	if err != nil {
		return err
	}
	return workspacegroup.WithNoteLock(root, func() error { return taskdb.RecoverMoves(renameNote) })
}
