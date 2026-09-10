package workspace

import (
	"fmt"
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

// Remove only the recorded, validated workspace child, never the shared parent
// or another workspace's files. RemoveAll does not follow symlinks in the child.
func removeSharedDirectory(group workspacegroup.Workspace) error {
	if group.Sandbox == nil || group.Sandbox.SharedDirectory == "" {
		return nil
	}
	path := group.Sandbox.SharedDirectory
	if err := workspacegroup.ValidateSharedDirectory(group.Path, path); err != nil {
		return err
	}
	for _, directory := range []string{filepath.Dir(path), path} {
		info, err := os.Lstat(directory)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("refusing to clean up shared directory through a symlink or non-directory: %s", directory)
		}
	}
	return os.RemoveAll(path)
}
