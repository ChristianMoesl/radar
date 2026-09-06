package settings

import (
	"fmt"
	"path/filepath"
)

const ArchiveDirectory = "Archived"

func IsArchivedNote(path string) bool {
	directory := filepath.Dir(filepath.Clean(path))
	return filepath.Base(directory) == ArchiveDirectory && filepath.Base(filepath.Dir(directory)) == "Tasks"
}

// ValidateWorkspaceNote prevents the shared archive from becoming an implicit
// writable sandbox mount. Archived tasks must be reopened before activation.
func ValidateWorkspaceNote(path string) error {
	if IsArchivedNote(path) {
		return fmt.Errorf("reopen the archived task before attaching its note to a workspace: %s", path)
	}
	return nil
}
