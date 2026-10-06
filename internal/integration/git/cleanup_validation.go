package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"radar/internal/cleanup"
	workspacegroup "radar/internal/integration/workspace/group"
)

// validateCleanupMember checks the actual checkout, not just cached source
// metadata. Expiry never turns an invalid registration into deletion authority.
func validateCleanupMember(ctx context.Context, root string, member workspacegroup.Member) error {
	if issue := cleanup.LocationIssue(member.Path, root); issue != "" {
		return fmt.Errorf("%s: %s", member.Path, issue)
	}
	rel, err := filepath.Rel(root, member.Path)
	if err != nil {
		return err
	}
	path := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("managed worktree and its parents must be directories, not symlinks: %s", path)
		}
	}
	gitFile, err := os.Lstat(filepath.Join(member.Path, ".git"))
	if err != nil {
		return err
	}
	if !gitFile.Mode().IsRegular() {
		return fmt.Errorf("managed member must be a linked worktree, not a primary checkout or symlink: %s", member.Path)
	}
	top, err := gitOutput(ctx, member.Path, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	common, err := gitOutput(ctx, member.Path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	repositoryCommon, err := gitOutput(ctx, member.Repository, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	branch, err := gitOutput(ctx, member.Path, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return err
	}
	if cleanPhysicalPath(top) != cleanPhysicalPath(member.Path) || cleanPhysicalPath(common) != cleanPhysicalPath(repositoryCommon) || strings.TrimSpace(branch) != member.Branch {
		return fmt.Errorf("managed worktree does not match its registered repository, path and branch: %s", member.Path)
	}
	gitDir, err := gitOutput(ctx, member.Path, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return err
	}
	if cleanPhysicalPath(gitDir) == cleanPhysicalPath(common) {
		return fmt.Errorf("managed member points at a primary repository checkout: %s", member.Path)
	}
	// A copied .git pointer can make rev-parse describe the current directory
	// while Git's actual worktree registration still belongs to another path.
	backlink, err := os.ReadFile(filepath.Join(strings.TrimSpace(gitDir), "gitdir"))
	if err != nil {
		return err
	}
	registeredGitFile := strings.TrimSpace(string(backlink))
	if !filepath.IsAbs(registeredGitFile) {
		registeredGitFile = filepath.Join(strings.TrimSpace(gitDir), registeredGitFile)
	}
	if cleanPhysicalPath(registeredGitFile) != cleanPhysicalPath(filepath.Join(member.Path, ".git")) {
		return fmt.Errorf("Git worktree registration points at a different path: %s", member.Path)
	}
	if _, err := os.Lstat(filepath.Join(strings.TrimSpace(gitDir), "locked")); !os.IsNotExist(err) {
		if err != nil {
			return err
		}
		return fmt.Errorf("managed worktree is locked: %s", member.Path)
	}
	return nil
}
