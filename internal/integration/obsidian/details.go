package obsidian

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"radar/internal/integration"
	"radar/internal/integration/obsidian/settings"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/taskdb"
)

func (s Source) SetTitle(_ context.Context, ref protocol.SourceRef, title string) (integration.AuthoredTaskIdentity, error) {
	title = strings.TrimSpace(title)
	if title == "" || !utf8.ValidString(title) {
		return integration.AuthoredTaskIdentity{}, fmt.Errorf("title must be non-empty UTF-8")
	}
	root, err := workspacegroup.DefaultRoot()
	if err != nil {
		return integration.AuthoredTaskIdentity{}, err
	}
	err = workspacegroup.WithNoteLock(root, func() error {
		current, err := s.noteForRef(ref)
		if err != nil {
			return err
		}
		if current.Title == title {
			return nil
		}
		current.Title = title
		return taskdb.Put(&current.Task)
	})
	return integration.AuthoredTaskIdentity{SourceRefID: ref.ID}, err
}

// ReassociateNote explicitly repairs an external move. No identity is inferred
// from the new file's content. A live workspace must be cleaned up first.
func (s Source) ReassociateNote(_ context.Context, ref protocol.SourceRef, path string) (integration.AuthoredTaskIdentity, error) {
	root, err := workspacegroup.DefaultRoot()
	if err != nil {
		return integration.AuthoredTaskIdentity{}, err
	}
	err = workspacegroup.WithNoteLock(root, func() error {
		vault, err := s.configuredVault()
		if err != nil {
			return err
		}
		path, err = filepath.Abs(path)
		if err != nil {
			return err
		}
		if !validManagedNotePath(vault, path) {
			return fmt.Errorf("note must be inside the managed task layout")
		}
		tasks, err := taskdb.List(vault)
		if err != nil {
			return err
		}
		for _, task := range tasks {
			if "obsidian:task:"+task.ID != ref.ID {
				continue
			}
			if task.Path == path {
				return nil
			}
			if _, err := os.Lstat(task.Path); !os.IsNotExist(err) {
				return fmt.Errorf("original note must be missing before reassociation")
			}
			current := note{Task: task}
			referenced, err := noteReferenced(root, current)
			if err != nil {
				return err
			}
			if referenced {
				return fmt.Errorf("clean up referencing workspace before reassociating its note")
			}
			if !settings.IsArchivedNote(path) && !strings.HasSuffix(filepath.Base(filepath.Dir(path)), "--"+shortID(task.ID)) {
				return fmt.Errorf("private task directory must retain its stable suffix --%s", shortID(task.ID))
			}
			task.Path = path
			if _, err := readTaskNote(task); err != nil {
				return err
			}
			return taskdb.Put(&task)
		}
		return fmt.Errorf("task identity not found")
	})
	return integration.AuthoredTaskIdentity{SourceRefID: ref.ID}, err
}
