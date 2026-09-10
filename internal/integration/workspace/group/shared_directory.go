package workspacegroup

import (
	"fmt"
	"path/filepath"
)

const SharedDirectoryParent = "radar-workspaces"

// An empty value means this managed resource has not been provisioned. Runtime
// consumers never infer a directory from an absent value; reconciliation adds it.
func ValidateSharedDirectory(workspacePath, path string) error {
	if path == "" {
		return nil
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path ||
		filepath.Base(filepath.Dir(path)) != SharedDirectoryParent || filepath.Base(path) != ID(workspacePath) {
		return fmt.Errorf("shared_directory must be an absolute %s/%s directory", SharedDirectoryParent, ID(workspacePath))
	}
	return nil
}
