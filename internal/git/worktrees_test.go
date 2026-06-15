package git

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGitRootsDefaultsToCwdAndWorkspaces(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(home, "not-a-workspace")
	workspace := filepath.Join(home, "workspaces", "repo", "feature")
	otherWorkspace := filepath.Join(home, "workspaces", "other", "fix")
	for _, dir := range []string{cwd, workspace, otherWorkspace, filepath.Join(home, "workspaces", "repo")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("RADAR_GIT_REPOS", "")
	t.Chdir(cwd)

	roots := gitRoots()
	assertContainsRoot(t, roots, cwd)
	assertContainsRoot(t, roots, workspace)
	assertContainsRoot(t, roots, otherWorkspace)
	assertMissingRoot(t, roots, filepath.Join(home, "workspaces", "repo"))
}

func TestGitRootsIncludesTmuxSessionPaths(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(home, "cwd")
	sessionPath := filepath.Join(home, "work", "repo")
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sessionPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte("#!/bin/sh\nprintf '%s\\n' '"+sessionPath+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("PATH", bin)
	t.Setenv("RADAR_GIT_REPOS", "")
	t.Chdir(cwd)

	roots := gitRoots()
	assertContainsRoot(t, roots, cwd)
	assertContainsRoot(t, roots, sessionPath)
}

func TestGitRootsEnvOverridesDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("RADAR_GIT_REPOS", "~/repo:/tmp/other")

	roots := gitRoots()
	want := []string{filepath.Join(home, "repo"), "/tmp/other"}
	if len(roots) != len(want) {
		t.Fatalf("gitRoots() = %#v, want %#v", roots, want)
	}
	for i := range want {
		if roots[i] != want[i] {
			t.Fatalf("gitRoots() = %#v, want %#v", roots, want)
		}
	}
}

func assertContainsRoot(t *testing.T, roots []string, want string) {
	t.Helper()
	for _, root := range roots {
		if root == want {
			return
		}
	}
	t.Fatalf("gitRoots() = %#v, missing %q", roots, want)
}

func assertMissingRoot(t *testing.T, roots []string, want string) {
	t.Helper()
	for _, root := range roots {
		if root == want {
			t.Fatalf("gitRoots() = %#v, unexpectedly contained %q", roots, want)
		}
	}
}
