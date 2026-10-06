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
	"radar/internal/integration"
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
	root   string
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
	a := &cleanupAnchor{root: root, parent: parent}
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

type anchorEntries struct {
	disposable []string
	expired    []string
}

func (e anchorEntries) names() []string {
	return append(slices.Clone(e.disposable), e.expired...)
}

// ValidateExpiredWorkspace lets earlier cleanup providers recheck the same
// anchor and member protections immediately before their destructive step.
// Reload registration and configuration rather than trusting preview state.
func ValidateExpiredWorkspace(root, id string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	registry, err := workspacegroup.Load(root)
	if err != nil {
		return err
	}
	group, found := workspacegroup.FindByID(registry, id)
	if !found {
		return fmt.Errorf("expired cleanup requires a registered workspace: %s", id)
	}
	_, err = anchorCleanupEntries(root, registry, group, cfg.Workspace.Cleanup.DisposableEntries, integration.CleanupExpired)
	return err
}

// Collection, preview and execution classify root entries with the same policy.
// Managed members always remain Git-owned, even in expired mode.
func anchorCleanupEntries(root string, registry workspacegroup.Registry, group workspacegroup.Workspace, disposable []string, mode integration.CleanupMode) (anchorEntries, error) {
	a, err := openCleanupAnchor(root, group.Path)
	if err != nil {
		return anchorEntries{}, err
	}
	defer a.close()
	return a.entries(registry, group, disposable, mode)
}

func (a *cleanupAnchor) entries(registry workspacegroup.Registry, group workspacegroup.Workspace, disposable []string, mode integration.CleanupMode) (anchorEntries, error) {
	if mode != integration.CleanupSafe && mode != integration.CleanupConfirmed && mode != integration.CleanupExpired {
		return anchorEntries{}, fmt.Errorf("invalid workspace cleanup mode: %d", mode)
	}
	if mode == integration.CleanupExpired {
		if err := validateExpiredAnchor(a.root, registry, group); err != nil {
			return anchorEntries{}, err
		}
	}
	if a.anchor == nil {
		return anchorEntries{}, nil
	}
	entries, err := fs.ReadDir(a.anchor.FS(), ".")
	if err != nil {
		return anchorEntries{}, err
	}
	managed := map[string]bool{}
	for _, member := range group.Members {
		managed[pathKey(filepath.Base(member.Path))] = true
	}
	var unknown []string
	var removable anchorEntries
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(group.Path, name)
		validNoteLink := name == "notes.md" && group.NotePath != "" && entry.Type()&os.ModeSymlink != 0
		switch {
		case validNoteLink:
			// Unlink the workspace view only, never its canonical target.
		case managed[pathKey(name)]:
			// Git owns these, even when their names are configured as disposable.
		case slices.Contains(disposable, name) && !strings.EqualFold(name, "notes.md"):
			// Keep the existing safe/confirmed config policy conservative.
			if entry.Type()&os.ModeSymlink == 0 && group.NotePath != "" &&
				pathContains(physicalPath(path), physicalPath(group.NotePath)) {
				return anchorEntries{}, fmt.Errorf("disposable entry contains the canonical note: %s", path)
			}
			removable.disposable = append(removable.disposable, name)
		default:
			if mode == integration.CleanupExpired {
				removable.expired = append(removable.expired, name)
			} else {
				unknown = append(unknown, path)
			}
		}
	}
	if len(unknown) > 0 {
		return anchorEntries{}, fmt.Errorf("workspace anchor contains unknown files: %s", strings.Join(unknown, ", "))
	}
	if mode == integration.CleanupExpired {
		if err := a.inspectExpiredAnchor(group, managed); err != nil {
			return anchorEntries{}, err
		}
	}
	return removable, nil
}

func (a *cleanupAnchor) inspectExpiredAnchor(group workspacegroup.Workspace, managed map[string]bool) error {
	// Walk from the already-open anchor, not an entry path: fs.WalkDir follows
	// a symlink used as its starting path, but never follows nested symlinks.
	// Unknown directories and members can contain primary checkouts without
	// Git cleanup targets. Inspect members too, before any bundle side effects.
	return fs.WalkDir(a.anchor.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == "." {
			return nil
		}
		localPath := filepath.FromSlash(path)
		if filepath.Dir(localPath) == "." && managed[pathKey(entry.Name())] && entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("managed workspace member is a symlink: %s", filepath.Join(group.Path, localPath))
		}
		if strings.EqualFold(entry.Name(), ".git") {
			parent := filepath.Dir(localPath)
			if filepath.Dir(parent) == "." && managed[pathKey(parent)] && entry.Type().IsRegular() {
				// The member's Git pointer is owned and verified by Git cleanup.
				return nil
			}
			return fmt.Errorf("workspace entry contains a primary or unidentified Git checkout: %s", filepath.Join(group.Path, localPath))
		}
		return nil
	})
}

// Expiry grants permission to discard local data, not permission to delete
// another resource. Check both lexical and physical paths: a registered path
// reached through a symlink must not lose its workspace view either.
func validateExpiredAnchor(root string, registry workspacegroup.Registry, group workspacegroup.Workspace) error {
	physicalRoot := physicalPath(root)
	if !filepath.IsAbs(root) || filepath.Dir(physicalRoot) == physicalRoot {
		return fmt.Errorf("unsafe configured workspace root for expired cleanup: %s", root)
	}
	contains := func(path string) bool {
		return path != "" && (pathContains(pathKey(group.Path), pathKey(path)) ||
			pathContains(pathKey(physicalPath(group.Path)), pathKey(physicalPath(path))))
	}
	protect := func(path, kind string) error {
		if contains(path) {
			return fmt.Errorf("workspace anchor contains %s: %s", kind, path)
		}
		return nil
	}
	for _, other := range registry.Workspaces {
		if err := protect(other.NotePath, "the canonical note"); err != nil {
			return err
		}
		if other.ID != group.ID {
			if err := protect(other.Path, "another registered workspace"); err != nil {
				return err
			}
		}
		for _, member := range other.Members {
			if err := protect(member.Repository, "a primary repository"); err != nil {
				return err
			}
			if other.ID != group.ID {
				if err := protect(member.Path, "another registered workspace member"); err != nil {
					return err
				}
			}
		}
		if other.Sandbox == nil {
			continue
		}
		for _, mount := range other.Sandbox.AdditionalMounts {
			if err := protect(mount.Path, "an external sandbox mount"); err != nil {
				return err
			}
		}
		for _, mount := range other.Sandbox.Mounts {
			path := strings.TrimSuffix(mount, ":ro")
			// The owner's anchor and direct members are the normal workspace
			// mounts. Every other mount is outside anchor cleanup ownership.
			if other.ID == group.ID && (sameCleanPath(path, group.Path) || slices.ContainsFunc(group.Members, func(member workspacegroup.Member) bool {
				return sameCleanPath(path, member.Path)
			})) {
				continue
			}
			if err := protect(path, "an external sandbox mount"); err != nil {
				return err
			}
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !sameCleanPath(root, ExpandPath(cfg.Workspace.RootDir)) {
		return fmt.Errorf("configured workspace root changed; preview cleanup again")
	}
	mounts, err := normalizeConfiguredMounts(cfg.SBX.AdditionalMounts)
	if err != nil {
		return err
	}
	for _, mount := range mounts {
		if err := protect(strings.TrimSuffix(mount, ":ro"), "a configured external sandbox mount"); err != nil {
			return err
		}
	}
	return nil
}

func addDisposableEntriesPreview(target *protocol.CleanupTarget, entries []string) {
	if len(entries) == 0 {
		return
	}
	// Provider-owned operation data pins the names shown by this preview. A
	// later config expansion or new root entry cannot silently extend deletion.
	encoded, _ := json.Marshal(entries)
	if target.Operation == nil {
		target.Operation = map[string]string{}
	}
	target.Operation["disposable_entries"] = string(encoded)
	message := "delete configured disposable entries (including directory contents): " + strings.Join(entries, ", ")
	// CLI confirmation uses Description; the compact TUI uses Safety.Summary.
	target.Description += "; " + message
	target.Safety = append(target.Safety, protocol.CleanupSafety{
		Kind: "disposable_entries", Summary: "delete disposable entries: " + strings.Join(entries, ", "),
		Message: message,
	})
}

func addExpiredEntriesPreview(target *protocol.CleanupTarget, entries []string) {
	if len(entries) == 0 {
		return
	}
	encoded, _ := json.Marshal(entries)
	if target.Operation == nil {
		target.Operation = map[string]string{}
	}
	target.Operation["expired_entries"] = string(encoded)
	message := "delete expired workspace contents (including directory contents): " + strings.Join(entries, ", ")
	target.Description += "; " + message
	target.Safety = append(target.Safety, protocol.CleanupSafety{
		Kind: "expired_entries", Summary: "delete expired contents: " + strings.Join(entries, ", "),
		Message: message, BlocksAutomatic: true, Expires: true,
	})
}

func removeWorkspaceAnchor(root string, target protocol.CleanupTarget, mode integration.CleanupMode) (workspacegroup.Workspace, error) {
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
	if mode == integration.CleanupExpired && (!sameCleanPath(target.Path, group.Path) || target.WorkspaceID != group.ID) {
		return workspacegroup.Workspace{}, fmt.Errorf("expired workspace cleanup target no longer matches its registration")
	}
	if len(group.Members) > 0 {
		return workspacegroup.Workspace{}, fmt.Errorf("workspace still contains %d managed worktree(s)", len(group.Members))
	}
	a, err := openCleanupAnchor(root, group.Path)
	if err != nil {
		return workspacegroup.Workspace{}, err
	}
	defer a.close()
	entries, err := a.entries(registry, group, cfg.Workspace.Cleanup.DisposableEntries, mode)
	if err != nil {
		return workspacegroup.Workspace{}, err
	}
	var previewed []string
	for _, key := range []string{"disposable_entries", "expired_entries"} {
		if encoded := target.Operation[key]; encoded != "" {
			if key == "expired_entries" && mode != integration.CleanupExpired {
				return workspacegroup.Workspace{}, fmt.Errorf("expired entries require expired workspace cleanup mode")
			}
			var names []string
			if err := json.Unmarshal([]byte(encoded), &names); err != nil {
				return workspacegroup.Workspace{}, fmt.Errorf("invalid %s preview: %w", strings.ReplaceAll(key, "_", " "), err)
			}
			previewed = append(previewed, names...)
		}
	}
	for _, name := range entries.names() {
		if !slices.Contains(previewed, name) {
			return workspacegroup.Workspace{}, fmt.Errorf("workspace entry %q was not in the cleanup preview; preview cleanup again", name)
		}
	}
	if a.anchor != nil {
		for _, name := range entries.names() {
			if err := a.anchor.RemoveAll(name); err != nil {
				return workspacegroup.Workspace{}, fmt.Errorf("remove workspace entry %q: %w", name, err)
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
