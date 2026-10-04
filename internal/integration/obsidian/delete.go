package obsidian

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"radar/internal/integration/obsidian/settings"
	"radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func (s Source) PreviewDelete(ctx context.Context, ref protocol.SourceRef) (protocol.TaskDeletionPreview, error) {
	root, err := workspacegroup.DefaultRoot()
	if err != nil {
		return protocol.TaskDeletionPreview{}, err
	}
	var preview protocol.TaskDeletionPreview
	err = workspacegroup.WithNoteLock(root, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var err error
		preview, err = s.previewDelete(root, ref)
		return err
	})
	return preview, err
}

func (s Source) previewDelete(root string, ref protocol.SourceRef) (protocol.TaskDeletionPreview, error) {
	current, err := s.noteForRef(ref)
	if err != nil {
		return protocol.TaskDeletionPreview{}, err
	}
	referenced, err := noteReferenced(root, current)
	if err != nil {
		return protocol.TaskDeletionPreview{}, err
	}
	if referenced {
		return protocol.TaskDeletionPreview{}, fmt.Errorf("task note is still referenced by a workspace; clean up its workspace first with x or radar cleanup <task-id>")
	}
	vault, err := s.configuredVault()
	if err != nil {
		return protocol.TaskDeletionPreview{}, err
	}
	// Never move data through a symlinked Tasks or trash directory. A private
	// task's parent and the note itself are also checked by noteForRef.
	if info, err := os.Lstat(taskRoot(vault)); err != nil || !info.IsDir() {
		return protocol.TaskDeletionPreview{}, fmt.Errorf("task root must be a real directory: %s", taskRoot(vault))
	}
	trash := filepath.Join(vault, ".trash")
	if info, err := os.Lstat(trash); err == nil {
		if !info.IsDir() {
			return protocol.TaskDeletionPreview{}, fmt.Errorf("trash must be a real directory: %s", trash)
		}
	} else if !os.IsNotExist(err) {
		return protocol.TaskDeletionPreview{}, err
	}

	path := current.Path
	description := "Move the task note to recoverable vault trash."
	hash := sha256.New()
	fmt.Fprintf(hash, "%q\n%q\n%q\n", current.content, current.Path, trash)
	if !settings.IsArchivedNote(path) {
		path = filepath.Dir(path)
		description = "Move the entire private task directory, including accompanying files, to recoverable vault trash."
		entries, err := os.ReadDir(path)
		if err != nil {
			return protocol.TaskDeletionPreview{}, err
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".md") && entry.Name() != filepath.Base(current.Path) {
				return protocol.TaskDeletionPreview{}, fmt.Errorf("task directory must contain exactly one Markdown note: %s", path)
			}
			// A changed directory listing requires a fresh confirmation. File
			// contents are not read: attachments move intact, without following
			// symlinks or rewriting relative links.
			fmt.Fprintf(hash, "%q\n%v\n", entry.Name(), entry.Type())
		}
	}
	return protocol.TaskDeletionPreview{
		TaskTitle: current.Title, SourceRefID: ref.ID, Path: path,
		TrashDirectory: trash, Description: description, Revision: fmt.Sprintf("%x", hash.Sum(nil)),
	}, nil
}

func (s Source) Delete(ctx context.Context, ref protocol.SourceRef, preview protocol.TaskDeletionPreview) (protocol.TaskDeletionResult, error) {
	root, err := workspacegroup.DefaultRoot()
	if err != nil {
		return protocol.TaskDeletionResult{}, err
	}
	var result protocol.TaskDeletionResult
	err = workspacegroup.WithNoteLock(root, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		fresh, err := s.previewDelete(root, ref)
		if err != nil {
			return err
		}
		fresh.TaskID = preview.TaskID
		if fresh != preview {
			return fmt.Errorf("task deletion preview changed; preview and confirm deletion again")
		}
		if err := ensureRealDirectory(fresh.TrashDirectory); err != nil {
			return fmt.Errorf("prepare task trash: %w", err)
		}
		// A unique container preserves the original basename and prevents any
		// overwrite, including after restoring and deleting the same task again.
		container, err := os.MkdirTemp(fresh.TrashDirectory, "radar-")
		if err != nil {
			return fmt.Errorf("prepare task trash: %w", err)
		}
		destination := filepath.Join(container, filepath.Base(fresh.Path))
		// This is one atomic no-replace move, never a copy-and-recursive-delete.
		// Private directories move as a unit; archives move only the one note.
		if err := renameNote(fresh.Path, destination); err != nil {
			_ = os.Remove(container) // empty only; never remove user content
			return fmt.Errorf("move task to trash; source left at %s: %w", fresh.Path, err)
		}
		result = protocol.TaskDeletionResult{
			TaskID: preview.TaskID, SourceRefID: ref.ID, OriginalPath: fresh.Path, TrashPath: destination,
		}
		return nil
	})
	return result, err
}
