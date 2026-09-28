package workspace

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"radar/internal/cleanup"
	"radar/internal/config"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func loadAnchorCleanupSettings() (string, []string, error) {
	cfg, err := config.Load()
	if err != nil {
		return "", nil, err
	}
	return filepath.Clean(ExpandPath(cfg.Workspace.RootDir)), cfg.Workspace.Cleanup.DisposableEntries, nil
}

type cleanupAnchor struct {
	parent *os.Root
	anchor *os.Root
	rel    string
}

func (a *cleanupAnchor) close() {
	if a.anchor != nil {
		a.anchor.Close()
	}
	a.parent.Close()
}

// Pin both directories so recursive deletion cannot be redirected through a
// replaced anchor or an escaping symlink. Never open a symlink as the anchor.
func openCleanupAnchor(root, path string) (*cleanupAnchor, error) {
	if reason := cleanup.LocationIssue(path, root); reason != "" {
		return nil, fmt.Errorf("%s: %s", path, reason)
	}
	parent, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		parent.Close()
		return nil, err
	}
	a := &cleanupAnchor{parent: parent}
	parts := strings.Split(rel, string(filepath.Separator))
	for i, name := range parts {
		a.rel = name
		info, err := a.parent.Lstat(name)
		if os.IsNotExist(err) {
			return a, nil
		}
		if err != nil {
			a.close()
			return nil, err
		}
		if !info.IsDir() {
			a.close()
			return nil, fmt.Errorf("workspace anchor and its parents below the workspace root must be directories, not symlinks: %s", path)
		}
		a.anchor, err = a.parent.OpenRoot(name)
		if err != nil {
			a.close()
			return nil, err
		}
		opened, err := a.anchor.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			a.close()
			return nil, fmt.Errorf("workspace anchor changed while inspecting: %s", path)
		}
		if i < len(parts)-1 {
			a.parent.Close()
			a.parent, a.anchor = a.anchor, nil
		}
	}
	return a, nil
}

// Collection, preview and execution classify root entries with the same policy.
// The result contains existing disposable names only, never managed members.
func anchorCleanupEntries(root string, group workspacegroup.Workspace, disposable []string) ([]string, error) {
	a, err := openCleanupAnchor(root, group.Path)
	if err != nil {
		return nil, err
	}
	defer a.close()
	return a.entries(group, disposable)
}

func (a *cleanupAnchor) entries(group workspacegroup.Workspace, disposable []string) ([]string, error) {
	if a.anchor == nil {
		return nil, nil
	}
	entries, err := fs.ReadDir(a.anchor.FS(), ".")
	if err != nil {
		return nil, err
	}
	managed := map[string]bool{}
	for _, member := range group.Members {
		managed[pathKey(filepath.Base(member.Path))] = true
	}
	var unknown, removable []string
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(group.Path, name)
		switch {
		case strings.EqualFold(name, "notes.md"):
			if name != "notes.md" || group.NotePath == "" || entry.Type()&os.ModeSymlink == 0 {
				unknown = append(unknown, path)
			}
		case managed[pathKey(name)]:
			// Git owns these, even when their names are configured as disposable.
		case slices.Contains(disposable, name):
			// Invalid registrations must not turn a canonical note into a cache.
			// A disposable symlink is unlinked, so its target remains untouched.
			if entry.Type()&os.ModeSymlink == 0 && group.NotePath != "" &&
				pathContains(physicalPath(path), physicalPath(group.NotePath)) {
				return nil, fmt.Errorf("disposable entry contains the canonical note: %s", path)
			}
			removable = append(removable, name)
		default:
			unknown = append(unknown, path)
		}
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("workspace anchor contains unknown files: %s", strings.Join(unknown, ", "))
	}
	return removable, nil
}

func addDisposableEntriesPreview(target *protocol.CleanupTarget, entries []string) {
	if len(entries) == 0 {
		return
	}
	// Provider-owned operation data pins the names shown by this preview. A
	// later config expansion or new root entry cannot silently extend deletion.
	encoded, _ := json.Marshal(entries)
	target.Operation = map[string]string{"disposable_entries": string(encoded)}
	message := "delete configured disposable entries (including directory contents): " + strings.Join(entries, ", ")
	// CLI confirmation uses Description; the compact TUI uses Safety.Summary.
	target.Description += "; " + message
	target.Safety = append(target.Safety, protocol.CleanupSafety{
		Kind: "disposable_entries", Summary: "delete disposable entries: " + strings.Join(entries, ", "),
		Message: message,
	})
}

func removeWorkspaceAnchor(root string, target protocol.CleanupTarget) (workspacegroup.Workspace, error) {
	cfg, err := config.Load()
	if err != nil {
		return workspacegroup.Workspace{}, err
	}
	registry, err := workspacegroup.Load(root)
	if err != nil {
		return workspacegroup.Workspace{}, err
	}
	id := strings.TrimPrefix(target.SourceRefID, "workspace:")
	group, found := workspacegroup.FindByID(registry, id)
	if !found {
		return workspacegroup.Workspace{}, nil
	}
	if len(group.Members) > 0 {
		return workspacegroup.Workspace{}, fmt.Errorf("workspace still contains %d managed worktree(s)", len(group.Members))
	}
	a, err := openCleanupAnchor(root, group.Path)
	if err != nil {
		return workspacegroup.Workspace{}, err
	}
	defer a.close()
	entries, err := a.entries(group, cfg.Workspace.Cleanup.DisposableEntries)
	if err != nil {
		return workspacegroup.Workspace{}, err
	}
	var previewed []string
	if encoded := target.Operation["disposable_entries"]; encoded != "" {
		if err := json.Unmarshal([]byte(encoded), &previewed); err != nil {
			return workspacegroup.Workspace{}, fmt.Errorf("invalid disposable entries preview: %w", err)
		}
	}
	for _, name := range entries {
		if !slices.Contains(previewed, name) {
			return workspacegroup.Workspace{}, fmt.Errorf("disposable entry %q was not in the cleanup preview; preview cleanup again", name)
		}
	}
	if a.anchor != nil {
		for _, name := range entries {
			if err := a.anchor.RemoveAll(name); err != nil {
				return workspacegroup.Workspace{}, fmt.Errorf("remove disposable entry %q: %w", name, err)
			}
		}
		if info, err := a.anchor.Lstat("notes.md"); err == nil && info.Mode()&os.ModeSymlink != 0 {
			if err := a.anchor.Remove("notes.md"); err != nil {
				return workspacegroup.Workspace{}, err
			}
		}
		// Do not recursively remove the anchor: newly appearing unknown files
		// must still prevent its removal.
		if err := a.parent.Remove(a.rel); err != nil && !os.IsNotExist(err) {
			return workspacegroup.Workspace{}, err
		}
	}
	if err := removeSharedDirectory(group); err != nil {
		return workspacegroup.Workspace{}, err
	}
	if err := workspacegroup.RemoveWorkspace(root, group.ID); err != nil {
		return workspacegroup.Workspace{}, err
	}
	return group, nil
}
