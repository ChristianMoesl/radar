package workstream

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func DiscoverRepos(ctx context.Context, runner Runner, currentDirectory string) []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	workstreams := filepath.Join(home, "workstreams")
	repos := make([]string, 0)
	seen := map[string]bool{}
	add := func(path string) {
		repo, err := runner.Run(ctx, path, "git", "rev-parse", "--show-toplevel")
		if err != nil || repo == "" || isSubpath(repo, workstreams) || seen[repo] {
			return
		}
		seen[repo] = true
		repos = append(repos, repo)
	}

	add(currentDirectory)
	for _, root := range []string{"workspace", "code", "src", "dev", "projects"} {
		discoverGitDirectories(filepath.Join(home, root), 4, add)
	}
	sort.Strings(repos)
	if current, err := runner.Run(ctx, currentDirectory, "git", "rev-parse", "--show-toplevel"); err == nil {
		for i, repo := range repos {
			if repo == current {
				copy(repos[1:i+1], repos[0:i])
				repos[0] = current
				break
			}
		}
	}
	return repos
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

func discoverGitDirectories(root string, maxDepth int, add func(string)) {
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		depth := 0
		if relative != "." {
			depth = len(strings.Split(relative, string(os.PathSeparator)))
		}
		if entry.IsDir() && depth > maxDepth {
			return filepath.SkipDir
		}
		if entry.IsDir() && entry.Name() == ".git" {
			add(filepath.Dir(path))
			return filepath.SkipDir
		}
		return nil
	})
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
