package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateWorkspaceReusesExistingLocalBranchE2E(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	ctx := context.Background()
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "commands.log")
	installCreateWorkspaceFakeTools(t, tmp, logPath)

	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitE2E(t, ctx, repo, "init")
	runGitE2E(t, ctx, repo, "config", "user.email", "radar@example.test")
	runGitE2E(t, ctx, repo, "config", "user.name", "Radar Test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitE2E(t, ctx, repo, "add", "README.md")
	runGitE2E(t, ctx, repo, "commit", "-m", "initial")
	runGitE2E(t, ctx, repo, "branch", "chore-install-helper-binaries")

	root := filepath.Join(tmp, "workspaces")
	created, err := Create(ctx, ExecRunner{}, CreateOptions{
		Repo:          repo,
		Name:          "chore-install-helper-binaries",
		Branch:        "chore/install-helper-binaries",
		Base:          "origin/chore/install-helper-binaries",
		WorkspaceRoot: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Branch != "chore-install-helper-binaries" {
		t.Fatalf("branch = %q", created.Branch)
	}
	if _, err := os.Stat(created.Path); err != nil {
		t.Fatalf("created path missing: %v", err)
	}
	branch := strings.TrimSpace(gitOutputE2E(t, ctx, created.Path, "branch", "--show-current"))
	if branch != "chore-install-helper-binaries" {
		t.Fatalf("worktree branch = %q", branch)
	}

	if _, err := os.ReadFile(logPath); err != nil {
		t.Fatal(err)
	}
}

func installCreateWorkspaceFakeTools(t *testing.T, tmp string, logPath string) {
	t.Helper()
	bin := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	tmux := `#!/bin/sh
printf 'tmux|%s|%s\n' "$PWD" "$*" >> "$RADAR_CREATE_E2E_LOG"
if [ "$1" = "has-session" ]; then
  exit 1
fi
exit 0
`
	for _, name := range []string{"tmux", "pi", "nvim"} {
		script := tmux
		if name != "tmux" {
			script = `#!/bin/sh
printf '` + name + `|%s|%s\n' "$PWD" "$*" >> "$RADAR_CREATE_E2E_LOG"
exit 0
`
		}
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RADAR_CREATE_E2E_LOG", logPath)
}

func gitOutputE2E(t *testing.T, ctx context.Context, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}
