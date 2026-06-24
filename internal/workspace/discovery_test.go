package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"radar/internal/config"
)

type discoveryRunner struct {
	repos    map[string]string
	branches string
}

func (d discoveryRunner) LookPath(string) error { return nil }

func (d discoveryRunner) Run(_ context.Context, cwd string, name string, args ...string) (string, error) {
	if name == "git" && strings.Join(args, " ") == "rev-parse --show-toplevel" {
		if repo := d.repos[cwd]; repo != "" {
			return repo, nil
		}
		return "", os.ErrNotExist
	}
	return d.branches, nil
}

type countingDiscoveryRunner struct {
	repos    map[string]string
	fdOutput string
	fdByRoot map[string]string
	fdErrors map[string]error
	fdArgs   []string
	gitCalls int
	fdCalls  int
}

func (d *countingDiscoveryRunner) LookPath(name string) error {
	if name == "fd" {
		return nil
	}
	return os.ErrNotExist
}

func (d *countingDiscoveryRunner) Run(_ context.Context, cwd string, name string, args ...string) (string, error) {
	if name == "git" && strings.Join(args, " ") == "rev-parse --show-toplevel" {
		d.gitCalls++
		if repo := d.repos[cwd]; repo != "" {
			return repo, nil
		}
	}
	if name == "fd" {
		d.fdCalls++
		d.fdArgs = append([]string(nil), args...)
		root := args[len(args)-1]
		if err := d.fdErrors[root]; err != nil {
			return "", err
		}
		if d.fdByRoot != nil {
			return d.fdByRoot[root], nil
		}
		return d.fdOutput, nil
	}
	return "", os.ErrNotExist
}

func TestDiscoverReposUsesGitDirectoriesWithoutResolvingEveryRepo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))

	current := filepath.Join(home, "workspace", "current")
	currentSubdir := filepath.Join(current, "src")
	repo := filepath.Join(home, "workspace", "repo")
	worktree := filepath.Join(home, "workspace", "worktree")
	generatedWorkspace := filepath.Join(home, "workspaces", "repo", "feature")
	for _, path := range []string{
		filepath.Join(current, ".git"),
		currentSubdir,
		filepath.Join(repo, ".git"),
		worktree,
		filepath.Join(generatedWorkspace, ".git"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: ../repo/.git/worktrees/worktree\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := &countingDiscoveryRunner{
		repos: map[string]string{currentSubdir: current},
		fdOutput: strings.Join([]string{
			filepath.Join(current, ".git"),
			filepath.Join(repo, ".git"),
			filepath.Join(generatedWorkspace, ".git"),
		}, "\n"),
	}
	got, err := DiscoverRepos(context.Background(), runner, currentSubdir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{current, repo}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DiscoverRepos() = %#v, want %#v", got, want)
	}
	if runner.gitCalls != 1 {
		t.Fatalf("DiscoverRepos() ran git %d times, want 1", runner.gitCalls)
	}
	if runner.fdCalls != 1 {
		t.Fatalf("DiscoverRepos() ran fd %d times, want 1", runner.fdCalls)
	}
	wantPrefix := []string{"-H", "-t", "d", `^\.git$`, "--max-depth", "5"}
	if len(runner.fdArgs) < len(wantPrefix) || !reflect.DeepEqual(runner.fdArgs[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("fd args = %#v, want prefix %#v", runner.fdArgs, wantPrefix)
	}
}

func TestDiscoverReposContinuesWhenOneRepositoryDirFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))

	one := filepath.Join(home, "one")
	two := filepath.Join(home, "two")
	for _, path := range []string{one, filepath.Join(two, "repo", ".git")} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeDiscoveryConfig(t, home, []string{one, two})

	runner := &countingDiscoveryRunner{
		repos:    map[string]string{},
		fdByRoot: map[string]string{two: filepath.Join(two, "repo", ".git")},
		fdErrors: map[string]error{one: errors.New("permission denied")},
	}
	got, err := DiscoverRepos(context.Background(), runner, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(two, "repo")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DiscoverRepos() = %#v, want %#v", got, want)
	}
	if runner.fdCalls != 2 {
		t.Fatalf("DiscoverRepos() ran fd %d times, want 2", runner.fdCalls)
	}
}

func TestDiscoverReposReportsFdErrorsWhenNoRepositoriesCanBeDiscovered(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))

	one := filepath.Join(home, "one")
	if err := os.MkdirAll(one, 0o755); err != nil {
		t.Fatal(err)
	}
	writeDiscoveryConfig(t, home, []string{one})

	runner := &countingDiscoveryRunner{
		repos:    map[string]string{},
		fdErrors: map[string]error{one: errors.New("permission denied")},
	}
	_, err := DiscoverRepos(context.Background(), runner, "")
	if err == nil {
		t.Fatal("DiscoverRepos() error = nil, want fd error")
	}
	if !strings.Contains(err.Error(), one) || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("DiscoverRepos() error = %q, want root and cause", err)
	}
}

func TestDiscoverReposPrefersSourceRepoForCurrentWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))

	current := filepath.Join(home, "workspaces", "radar", "small-fix")
	currentSubdir := filepath.Join(current, "src")
	radar := filepath.Join(home, "workspace", "radar")
	alpha := filepath.Join(home, "workspace", "alpha")
	for _, path := range []string{
		filepath.Join(radar, ".git"),
		filepath.Join(alpha, ".git"),
		currentSubdir,
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	runner := &countingDiscoveryRunner{
		repos: map[string]string{currentSubdir: current},
		fdOutput: strings.Join([]string{
			filepath.Join(alpha, ".git"),
			filepath.Join(radar, ".git"),
		}, "\n"),
	}
	got, err := DiscoverRepos(context.Background(), runner, currentSubdir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{radar, alpha}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DiscoverRepos() = %#v, want %#v", got, want)
	}
}

func writeDiscoveryConfig(t *testing.T, home string, repositoryDirs []string) {
	t.Helper()
	path := filepath.Join(home, "config", "radar", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(config.Config{RepositoryDirs: repositoryDirs})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryDirsUsesConfig(t *testing.T) {
	home := t.TempDir()
	one := filepath.Join(home, "one")
	two := filepath.Join(home, "two")
	missing := filepath.Join(home, "missing")
	if err := os.MkdirAll(one, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(two, 0o755); err != nil {
		t.Fatal(err)
	}
	got := RepositoryDirs(config.Config{RepositoryDirs: []string{one, missing, two, one}})
	want := []string{one, two}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RepositoryDirs() = %#v, want %#v", got, want)
	}
}

func TestDefaultRootUsesConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	path := filepath.Join(home, "config", "radar", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"workspace_root":"~/streams"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := DefaultRoot()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "streams")
	if got != want {
		t.Fatalf("DefaultRoot() = %q, want %q", got, want)
	}
}

func TestFetchBranchesPrunesOrigin(t *testing.T) {
	runner := &fetchBranchesRunner{}
	if err := FetchBranches(context.Background(), runner, "/repo"); err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"fetch", "--prune", "origin"}
	if runner.cwd != "/repo" || runner.name != "git" || !reflect.DeepEqual(runner.args, wantArgs) {
		t.Fatalf("FetchBranches() ran cwd=%q name=%q args=%#v, want cwd=%q name=%q args=%#v", runner.cwd, runner.name, runner.args, "/repo", "git", wantArgs)
	}
}

type fetchBranchesRunner struct {
	cwd  string
	name string
	args []string
}

func (f *fetchBranchesRunner) LookPath(string) error { return nil }

func (f *fetchBranchesRunner) Run(_ context.Context, cwd string, name string, args ...string) (string, error) {
	f.cwd = cwd
	f.name = name
	f.args = append([]string(nil), args...)
	return "", nil
}

func TestBranchesOrdersOriginBeforeLocal(t *testing.T) {
	runner := discoveryRunner{branches: strings.Join([]string{
		"refs/heads/feature\tfeature\t",
		"refs/remotes/origin/main\torigin/main\t",
		"refs/remotes/origin/HEAD\torigin/HEAD\trefs/remotes/origin/main",
		"refs/heads/main\tmain\t",
		"refs/remotes/origin/fix\torigin/fix\t",
	}, "\n")}
	got, err := Branches(context.Background(), runner, "/repo")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"origin/main", "origin/fix", "main", "feature"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Branches() = %#v, want %#v", got, want)
	}
}

func TestPathsListsWorkspaces(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{
		filepath.Join(root, "repo-b", "two"),
		filepath.Join(root, "repo-a", "one"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Paths(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(root, "repo-a", "one"), filepath.Join(root, "repo-b", "two")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Paths() = %#v, want %#v", got, want)
	}
}
