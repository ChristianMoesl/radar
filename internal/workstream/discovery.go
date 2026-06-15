package workstream

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

func DiscoverRepos(ctx context.Context, runner Runner, currentDirectory string) ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	workstreams := filepath.Join(home, "workstreams")
	repos := make([]string, 0)
	seen := map[string]bool{}
	addRepo := func(repo string) {
		repo = filepath.Clean(repo)
		key := pathKey(repo)
		if repo == "" || isSubpath(repo, workstreams) || seen[key] {
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
	roots := existingDiscoveryRoots(home)
	if len(roots) > 0 {
		args := []string{"-H", "-t", "d", `^\.git$`, "--max-depth", "5"}
		args = append(args, roots...)
		output, err := runner.Run(ctx, "", "fd", args...)
		if err != nil {
			return nil, err
		}
		for _, gitDirectory := range strings.Split(output, "\n") {
			gitDirectory = strings.TrimSpace(gitDirectory)
			if gitDirectory == "" {
				continue
			}
			// Intentionally only ask fd for real .git directories. Linked Git worktrees
			// use a .git file that points at the main repository's metadata; those are
			// existing workstreams and should not appear as base repositories for create.
			// Because the parent of a .git directory is already the repository root, we
			// avoid running git rev-parse for every discovered repository.
			addRepo(filepath.Dir(filepath.Clean(gitDirectory)))
		}
	}
	sort.Strings(repos)
	if currentErr == nil {
		for i, repo := range repos {
			if pathKey(repo) == pathKey(current) {
				copy(repos[1:i+1], repos[0:i])
				repos[0] = repo
				break
			}
		}
	}
	return repos, nil
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
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		workspaceRoot = filepath.Join(home, "workstreams")
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

func existingDiscoveryRoots(home string) []string {
	roots := make([]string, 0, 5)
	for _, root := range []string{"workspace", "code", "src", "dev", "projects"} {
		path := filepath.Join(home, root)
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			roots = append(roots, path)
		}
	}
	return roots
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
