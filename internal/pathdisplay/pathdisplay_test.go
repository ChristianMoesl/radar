package pathdisplay

import (
	"path/filepath"
	"testing"
)

func TestHomeRelativeShortensOnlyHomePaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	child := filepath.Join(home, "workspace", "radar")
	sibling := home + "-other"
	for _, test := range []struct {
		path string
		want string
	}{
		{path: home, want: "~"},
		{path: child, want: filepath.Join("~", "workspace", "radar")},
		{path: sibling, want: sibling},
		{path: "workspace/radar", want: "workspace/radar"},
	} {
		if got := HomeRelative(test.path); got != test.want {
			t.Errorf("HomeRelative(%q) = %q, want %q", test.path, got, test.want)
		}
	}
}

func TestHomeRelativeSuffixShortensPathBasedDisplayValue(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, "workspaces", "radar", "small-fix")
	id := "git:worktree:" + path

	if got, want := HomeRelativeSuffix(id, path), "git:worktree:"+filepath.Join("~", "workspaces", "radar", "small-fix"); got != want {
		t.Fatalf("HomeRelativeSuffix() = %q, want %q", got, want)
	}
	if got := HomeRelativeSuffix("git:worktree:stable-id", path); got != "git:worktree:stable-id" {
		t.Fatalf("HomeRelativeSuffix() changed unrelated value to %q", got)
	}
}
