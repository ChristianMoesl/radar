package workstream

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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
		return d.fdOutput, nil
	}
	return "", os.ErrNotExist
}

func TestDiscoverReposUsesGitDirectoriesWithoutResolvingEveryRepo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	current := filepath.Join(home, "workspace", "current")
	currentSubdir := filepath.Join(current, "src")
	repo := filepath.Join(home, "workspace", "repo")
	worktree := filepath.Join(home, "workspace", "worktree")
	generatedWorkstream := filepath.Join(home, "workstreams", "repo", "feature")
	for _, path := range []string{
		filepath.Join(current, ".git"),
		currentSubdir,
		filepath.Join(repo, ".git"),
		worktree,
		filepath.Join(generatedWorkstream, ".git"),
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
			filepath.Join(generatedWorkstream, ".git"),
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

func TestPathsListsWorkstreams(t *testing.T) {
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
