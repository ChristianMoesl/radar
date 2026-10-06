package git

import (
	"fmt"
	"path/filepath"
)

// Bind the concrete .git file (linked worktree) or directory (main checkout),
// not the reusable worktree path. Its inode and birth time survive ordinary Git
// writes, but change when another checkout takes its place. Collection still
// works when the filesystem cannot supply a lifetime identity; an empty key
// must not be used to author a durable path-based ignore binding.
func worktreeBindingKey(path string) (string, error) {
	gitPath := filepath.Join(path, ".git")
	identity, err := gitFileIdentity(gitPath)
	if err != nil {
		return "", fmt.Errorf("could not determine concrete Git worktree lifetime from %s: %w", gitPath, err)
	}
	return "git:worktree:" + identity, nil
}
