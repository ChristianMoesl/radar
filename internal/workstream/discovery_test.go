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
