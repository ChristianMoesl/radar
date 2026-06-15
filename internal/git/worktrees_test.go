package git

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGitRootsDefaultsToCwdAndWorkstreams(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(home, "not-a-workstream")
	workstream := filepath.Join(home, "workstreams", "repo", "feature")
	otherWorkstream := filepath.Join(home, "workstreams", "other", "fix")
	for _, dir := range []string{cwd, workstream, otherWorkstream, filepath.Join(home, "workstreams", "repo")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("RADAR_GIT_REPOS", "")
	t.Chdir(cwd)

	roots := gitRoots()
	assertContainsRoot(t, roots, cwd)
	assertContainsRoot(t, roots, workstream)
	assertContainsRoot(t, roots, otherWorkstream)
	assertMissingRoot(t, roots, filepath.Join(home, "workstreams", "repo"))
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
