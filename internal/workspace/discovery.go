package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"radar/internal/config"
)

func DiscoverRepos(ctx context.Context, runner Runner, currentDirectory string) ([]string, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	workspaces := ExpandPath(cfg.WorkspaceRoot)
	repos := make([]string, 0)
	seen := map[string]bool{}
	addRepo := func(repo string) {
		repo = filepath.Clean(repo)
		key := pathKey(repo)
		if repo == "" || isSubpath(repo, workspaces) || seen[key] {
			return
		}
		seen[key] = true
		repos = append(repos, repo)
	}

	current, currentErr := runner.Run(ctx, currentDirectory, "git", "rev-parse", "--show-toplevel")
	if currentErr == nil {
		addRepo(current)
	}
	if err := runner.LookPath("fd"); err != nil {
		return nil, err
	}
	roots := RepositoryDirs(cfg)
	fdErrors := make([]error, 0)
	for _, root := range roots {
		args := []string{"-H", "-t", "d", `^\.git$`, "--max-depth", "5", root}
		output, err := runner.Run(ctx, "", "fd", args...)
		if err != nil {
			fdErrors = append(fdErrors, fmt.Errorf("%s: %w", root, err))
			continue
		}
		for _, gitDirectory := range strings.Split(output, "\n") {
			gitDirectory = strings.TrimSpace(gitDirectory)
			if gitDirectory == "" {
				continue
			}
			// Intentionally only ask fd for real .git directories. Linked Git worktrees
			// use a .git file that points at the main repository's metadata; those are
			// existing workspaces and should not appear as base repositories for create.
			// Because the parent of a .git directory is already the repository root, we
			// avoid running git rev-parse for every discovered repository.
			addRepo(filepath.Dir(filepath.Clean(gitDirectory)))
		}
	}
	if len(repos) == 0 && len(fdErrors) > 0 {
		return nil, fmt.Errorf("discover repositories with fd: %w", errors.Join(fdErrors...))
	}
	sort.Strings(repos)
	if currentErr == nil {
		preferred := current
		if sourceRepoName, ok := workspaceSourceRepoName(current, workspaces); ok {
			preferred = sourceRepoName
		}
		preferRepo(repos, preferred)
	}
	return repos, nil
}

func FetchBranches(ctx context.Context, runner Runner, repo string) error {
	_, err := runner.Run(ctx, repo, "git", "fetch", "--prune", "origin")
	return err
}

func Branches(ctx context.Context, runner Runner, repo string) ([]string, error) {
	output, err := runner.Run(ctx, repo, "git", "for-each-ref", "--format=%(refname)\t%(refname:short)\t%(symref)", "refs/heads", "refs/remotes/origin")
	if err != nil {
		return nil, err
	}
	origin := make([]string, 0)
	local := make([]string, 0)
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) < 2 || fields[0] == "" || fields[1] == "" {
			continue
		}
		if strings.HasPrefix(fields[0], "refs/remotes/") {
			if strings.HasSuffix(fields[0], "/HEAD") || (len(fields) > 2 && fields[2] != "") {
				continue
			}
			origin = append(origin, fields[1])
		} else if strings.HasPrefix(fields[0], "refs/heads/") {
			local = append(local, fields[1])
		}
	}
	sortBranches(origin)
	sortBranches(local)
	return append(origin, local...), nil
}

func Paths(workspaceRoot string) ([]string, error) {
	if workspaceRoot == "" {
		root, err := DefaultRoot()
		if err != nil {
			return nil, err
		}
		workspaceRoot = root
	}
	repos, err := os.ReadDir(workspaceRoot)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0)
	for _, repo := range repos {
		if !repo.IsDir() {
			continue
		}
		streams, err := os.ReadDir(filepath.Join(workspaceRoot, repo.Name()))
		if err != nil {
			return nil, err
		}
		for _, stream := range streams {
			if stream.IsDir() {
				paths = append(paths, filepath.Join(workspaceRoot, repo.Name(), stream.Name()))
			}
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func RepositoryDirs(cfg config.Config) []string {
	paths := make([]string, 0, len(cfg.RepositoryDirs))
	for _, root := range cfg.RepositoryDirs {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		paths = append(paths, filepath.Clean(ExpandPath(root)))
	}
	return existingDirs(paths)
}

func DefaultRoot() (string, error) {
	cfg, err := config.Load()
	if err != nil {
		return "", err
	}
	return filepath.Clean(ExpandPath(cfg.WorkspaceRoot)), nil
}

func existingDirs(paths []string) []string {
	roots := make([]string, 0, len(paths))
	seen := map[string]bool{}
	for _, path := range paths {
		key := pathKey(path)
		if seen[key] {
			continue
		}
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			seen[key] = true
			roots = append(roots, path)
		}
	}
	return roots
}

func ExpandPath(path string) string {
	if path == "~" {
		home, _ := os.UserHomeDir()
		return home
	}
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}

func sortBranches(branches []string) {
	sort.Slice(branches, func(i int, j int) bool {
		return branchSortKey(branches[i]) < branchSortKey(branches[j])
	})
}

func branchSortKey(branch string) string {
	name := strings.TrimPrefix(branch, "origin/")
	switch name {
	case "main":
		return "0"
	case "master":
		return "1"
	default:
		return "2" + name
	}
}

func workspaceSourceRepoName(path string, root string) (string, bool) {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", false
	}
	parts := strings.Split(relative, string(os.PathSeparator))
	if len(parts) < 2 || parts[0] == "" || parts[0] == "." {
		return "", false
	}
	return parts[0], true
}

func preferRepo(repos []string, preferred string) {
	for i, repo := range repos {
		if pathKey(repo) == pathKey(preferred) || pathKey(filepath.Base(repo)) == pathKey(preferred) {
			copy(repos[1:i+1], repos[0:i])
			repos[0] = repo
			return
		}
	}
}

func isSubpath(path string, root string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func pathKey(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}
