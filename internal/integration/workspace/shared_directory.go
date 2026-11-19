package workspace

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"radar/internal/hosttemp"
	workspacegroup "radar/internal/integration/workspace/group"
)

// Pi preserves its original host temp root before changing TMPDIR. Radar
// subprocesses must not allocate another workspace inside the current one's
// shared directory, which would give it the wrong ownership and lifetime.
const hostTempDirEnv = hosttemp.EnvironmentVariable

func newSharedDirectory(workspacePath string) (string, error) {
	root := hosttemp.Dir()
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("host temporary directory must be absolute: %q", root)
	}
	return filepath.Join(root, workspacegroup.SharedDirectoryParent, workspacegroup.ID(workspacePath)), nil
}

// A more specific read-only mount must not be advertised as a writable exchange.
func sharedDirectoryReady(path string, mounts []string) bool {
	if path == "" {
		return false
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	return sharedDirectoryWritable(path, mounts)
}

func sharedDirectoryWritable(path string, mounts []string) bool {
	writableDepth, readOnlyDepth := -1, -1
	for _, mount := range mounts {
		mountPath := strings.TrimSuffix(mount, ":ro")
		if !pathContains(mountPath, path) {
			continue
		}
		depth := len(filepath.Clean(mountPath))
		if strings.HasSuffix(mount, ":ro") {
			readOnlyDepth = max(readOnlyDepth, depth)
		} else {
			writableDepth = max(writableDepth, depth)
		}
	}
	return writableDepth > readOnlyDepth
}

func ensureSharedDirectory(group workspacegroup.Workspace) error {
	if group.Sandbox == nil || group.Sandbox.SharedDirectory == "" {
		return nil
	}
	path := group.Sandbox.SharedDirectory
	if err := workspacegroup.ValidateSharedDirectory(group.Path, path); err != nil {
		return err
	}
	for _, directory := range []string{filepath.Dir(path), path} {
		if err := os.Mkdir(directory, 0o700); err != nil && !os.IsExist(err) {
			return fmt.Errorf("create shared directory %s: %w", directory, err)
		}
		info, err := os.Lstat(directory)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode().Perm() != 0o700 {
			return fmt.Errorf("shared directory must be a private directory (0700), not a symlink: %s", directory)
		}
	}
	return nil
}

// Remove only the recorded workspace child, never the shared parent or siblings.
func removeSharedDirectory(group workspacegroup.Workspace) error {
	return removeSharedDirectoryFiles(group, func() error { return nil }, (*os.Root).RemoveAll)
}

// Keep filesystem work outside the caller's note transaction. Pin the original
// parent and child, and recheck metadata and path identity before each effect.
// Like anchor deletion, nonrecursive final removal refuses late/replaced data.
func removeSharedDirectoryFiles(group workspacegroup.Workspace, revalidate func() error, removeAll func(*os.Root, string) error) error {
	if group.Sandbox == nil || group.Sandbox.SharedDirectory == "" {
		return nil
	}
	path := group.Sandbox.SharedDirectory
	if err := workspacegroup.ValidateSharedDirectory(group.Path, path); err != nil {
		return err
	}
	if err := revalidate(); err != nil {
		return err
	}
	// The recorded host temp root may be an alias (for example /var on macOS),
	// but the shared parent and workspace child below it must be real directories.
	a, err := openCleanupAnchor(filepath.Dir(filepath.Dir(path)), path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect shared directory: %w", err)
	}
	defer a.close()
	check := func() error {
		if err := revalidate(); err != nil {
			return err
		}
		if err := a.validatePath(path); err != nil {
			return fmt.Errorf("shared directory changed during cleanup: %w", err)
		}
		return nil
	}
	if a.anchor == nil {
		return check()
	}
	entries, err := fs.ReadDir(a.anchor.FS(), ".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := check(); err != nil {
			return err
		}
		if err := removeAll(a.anchor, entry.Name()); err != nil {
			return fmt.Errorf("remove shared directory entry %q: %w", entry.Name(), err)
		}
	}
	if err := check(); err != nil {
		return err
	}
	if err := a.parent.Remove(a.rel); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
