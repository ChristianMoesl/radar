package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeleteWorkspaceIgnoresMissingConfiguredSandboxE2E(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	withWorkspaceGOOS(t, "darwin")

	ctx := context.Background()
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "commands.log")
	installDeleteWorkspaceFakeTools(t, tmp, logPath)

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

	worktree := filepath.Join(tmp, "Workspace", "rb3ca-experience-center")
	runGitE2E(t, ctx, repo, "worktree", "add", "-b", "rb3ca-experience-center", worktree)
	if err := os.WriteFile(filepath.Join(worktree, ".radar.json"), []byte(`{"sandbox":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	deleted, err := Delete(ctx, ExecRunner{}, worktree, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.SandboxName != "radar-Workspace-rb3ca-experience-center" {
		t.Fatalf("sandbox name = %q", deleted.SandboxName)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists after delete: %v", err)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := strings.TrimSpace(string(logData))
	if !strings.Contains(log, "sbx|") || !strings.Contains(log, "|rm --force radar-Workspace-rb3ca-experience-center") {
		t.Fatalf("command log = %q, want forced sbx removal", log)
	}
	if strings.Contains(log, "sbx|"+worktree+"|") {
		t.Fatalf("sbx ran from deleted worktree cwd: %q", log)
	}
}

func installDeleteWorkspaceFakeTools(t *testing.T, tmp string, logPath string) {
	t.Helper()
	bin := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	tmux := `#!/bin/sh
printf 'tmux|%s|%s\n' "$PWD" "$*" >> "$RADAR_DELETE_E2E_LOG"
if [ "$1" = "has-session" ]; then
  exit 1
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(tmux), 0o755); err != nil {
		t.Fatal(err)
	}
	sbx := `#!/bin/sh
printf 'sbx|%s|%s\n' "$PWD" "$*" >> "$RADAR_DELETE_E2E_LOG"
echo "Error: sandbox '$3' not found (run 'sbx ls' to see your sandboxes)" >&2
exit 1
`
	if err := os.WriteFile(filepath.Join(bin, "sbx"), []byte(sbx), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RADAR_DELETE_E2E_LOG", logPath)
}

func runGitE2E(t *testing.T, ctx context.Context, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
}
