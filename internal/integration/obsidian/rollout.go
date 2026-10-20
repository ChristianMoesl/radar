package obsidian

import (
	"fmt"
	"os"
	"path/filepath"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/taskdb"
	"strings"
)

// RequireMigratedNotes detects old task records without interpreting their
// state. Only the explicit importer may read legacy operational frontmatter.
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
				continue
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if hasLegacyFields(string(data)) {
				return fmt.Errorf("existing task frontmatter requires migration: stop Radar and run radar migrate-state, then radar migrate-state --apply (%s)", path)
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
