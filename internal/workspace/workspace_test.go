package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type call struct {
	cwd  string
	name string
	args []string
}

type fakeRunner struct {
	repo       string
	hasSession bool
	calls      []call
}

func (f *fakeRunner) LookPath(string) error { return nil }

func (f *fakeRunner) Run(_ context.Context, cwd string, name string, args ...string) (string, error) {
	f.calls = append(f.calls, call{cwd: cwd, name: name, args: args})
	if name == "git" && strings.Join(args, " ") == "rev-parse --show-toplevel" {
		return f.repo, nil
	}
	if name == "tmux" && len(args) > 0 && args[0] == "has-session" {
		if !f.hasSession {
			return "", errors.New("missing")
		}
		return "", nil
	}
	if name == "git" && len(args) > 4 && args[0] == "worktree" && args[1] == "add" {
		return "", os.MkdirAll(args[4], 0o755)
	}
	return "", nil
}

func TestCreateBuildsWorktreeAndTmuxSession(t *testing.T) {
	repo := t.TempDir()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("SECRET=local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".radar.json"), []byte(`{
  "copy_files": [".env"],
  "setup": ["pnpm install --frozen-lockfile"],
  "model": "anthropic/claude-sonnet-4",
  "thinking": "high"
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{repo: repo}

	workspace, err := Create(context.Background(), runner, CreateOptions{
		Repo:          repo,
		Name:          "small fix",
		Base:          "origin/main",
		WorkspaceRoot: root,
		Switch:        true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if workspace.Branch != "small-fix" || workspace.SessionName != filepath.Base(repo)+"-small-fix" {
		t.Fatalf("unexpected workspace: %#v", workspace)
	}
	data, err := os.ReadFile(filepath.Join(workspace.Path, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "SECRET=local\n" {
		t.Fatalf("copied .env = %q", data)
	}
	assertCalled(t, runner.calls, "git", "worktree add -b small-fix "+workspace.Path+" origin/main")
	assertCalled(t, runner.calls, "sh", "-lc pnpm install --frozen-lockfile")
	assertCalledContains(t, runner.calls, "tmux", "pi --model 'anthropic/claude-sonnet-4' --thinking 'high' --session-id '"+workspace.SessionName+"'")
	assertCalled(t, runner.calls, "tmux", "new-session -d -s "+workspace.SessionName)
	assertCalled(t, runner.calls, "tmux", "new-window -t "+workspace.SessionName+":")
	assertCalled(t, runner.calls, "tmux", "switch-client -t "+workspace.SessionName)
}

func TestCreateStartsConfiguredSandbox(t *testing.T) {
	repo := t.TempDir()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".radar.json"), []byte(`{
  "sandbox": {}
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{repo: repo}

	workspace, err := Create(context.Background(), runner, CreateOptions{
		Repo:          repo,
		Name:          "small fix",
		Base:          "origin/main",
		WorkspaceRoot: root,
	})
	if err != nil {
		t.Fatal(err)
	}

	if workspace.SandboxName != "radar-"+filepath.Base(repo)+"-small-fix" {
		t.Fatalf("sandbox name = %q", workspace.SandboxName)
	}
	assertCalled(t, runner.calls, "docker", "sandbox create --name "+workspace.SandboxName+" shell "+workspace.Path)
	assertCalled(t, runner.calls, "tmux", "set-option -t "+workspace.SessionName+" default-command docker sandbox run '"+workspace.SandboxName+"'")
	assertCalled(t, runner.calls, "tmux", "new-window -t "+workspace.SessionName+": -n shell -c "+workspace.Path+" docker sandbox run '"+workspace.SandboxName+"'")
}

func TestCreateForksPiSession(t *testing.T) {
	repo := t.TempDir()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".radar.json"), []byte(`{"model":"google/gemini-2.5-pro","thinking":"xhigh"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{repo: repo}

	workspace, err := Create(context.Background(), runner, CreateOptions{
		Repo:          repo,
		Name:          "follow up",
		Base:          "HEAD",
		WorkspaceRoot: root,
		Model:         "openai-codex/gpt-5.4",
		Thinking:      "medium",
		ForkPiSession: "repo-current-task",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCalledContains(t, runner.calls, "tmux", "pi --fork 'repo-current-task' --model 'google/gemini-2.5-pro' --thinking 'xhigh' --session-id '"+workspace.SessionName+"'")
}

func TestCreateRejectsInvalidRepoThinking(t *testing.T) {
	repo := t.TempDir()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".radar.json"), []byte(`{"thinking":"maximum"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{repo: repo}

	_, err := Create(context.Background(), runner, CreateOptions{
		Repo:          repo,
		Name:          "small fix",
		Base:          "origin/main",
		WorkspaceRoot: root,
	})
	if err == nil {
		t.Fatal("Create() error = nil, want invalid thinking error")
	}
}

func TestCreateRejectsInvalidDefaultThinking(t *testing.T) {
	repo := t.TempDir()
	root := t.TempDir()
	runner := &fakeRunner{repo: repo}

	_, err := Create(context.Background(), runner, CreateOptions{
		Repo:          repo,
		Name:          "small fix",
		Base:          "origin/main",
		WorkspaceRoot: root,
		Thinking:      "maximum",
	})
	if err == nil {
		t.Fatal("Create() error = nil, want invalid thinking error")
	}
}

func TestCreateDoesNotCopyEnvWithoutRepoConfig(t *testing.T) {
	repo := t.TempDir()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("SECRET=local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{repo: repo}

	workspace, err := Create(context.Background(), runner, CreateOptions{
		Repo:          repo,
		Name:          "small fix",
		Base:          "origin/main",
		WorkspaceRoot: root,
		Model:         "github-copilot/claude-sonnet-4.5",
		Thinking:      "low",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace.Path, ".env")); !os.IsNotExist(err) {
		t.Fatalf(".env was copied without .radar.json config: %v", err)
	}
	assertCalledContains(t, runner.calls, "tmux", "pi --model 'github-copilot/claude-sonnet-4.5' --thinking 'low' --session-id '"+workspace.SessionName+"'")
}

func TestCreateEscapesWorktreeNamePathSegment(t *testing.T) {
	repo := t.TempDir()
	root := t.TempDir()
	runner := &fakeRunner{repo: repo}

	workspace, err := Create(context.Background(), runner, CreateOptions{
		Repo:          repo,
		Name:          "feature/nested fix",
		Base:          "origin/main",
		WorkspaceRoot: root,
	})
	if err != nil {
		t.Fatal(err)
	}

	wantPath := filepath.Join(root, filepath.Base(repo), "feature-nested-fix")
	if workspace.Path != wantPath {
		t.Fatalf("workspace path = %q, want %q", workspace.Path, wantPath)
	}
	if filepath.Dir(workspace.Path) != filepath.Join(root, filepath.Base(repo)) {
		t.Fatalf("workspace path created nested directories: %q", workspace.Path)
	}
	if workspace.Branch != "feature-nested-fix" {
		t.Fatalf("workspace branch = %q, want sanitized name", workspace.Branch)
	}
}

func TestCreateSessionCreatesTmuxSessionForWorktree(t *testing.T) {
	runner := &fakeRunner{}
	path := filepath.Join(t.TempDir(), "repo", "small-fix")
	created, err := CreateSession(context.Background(), runner, path, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if created.SessionName != "repo-small-fix" || created.Path != path {
		t.Fatalf("unexpected session workspace: %#v", created)
	}
	assertCalled(t, runner.calls, "tmux", "new-session -d -s repo-small-fix")
	assertCalled(t, runner.calls, "tmux", "new-window -t repo-small-fix:")
	assertCalled(t, runner.calls, "tmux", "switch-client -t repo-small-fix")
}

func TestDeleteKillsSessionAndRemovesWorktree(t *testing.T) {
	runner := &fakeRunner{hasSession: true}
	path := filepath.Join(t.TempDir(), "repo", "small-fix")
	if _, err := Delete(context.Background(), runner, path, "repo-small-fix", false); err != nil {
		t.Fatal(err)
	}
	assertCalled(t, runner.calls, "tmux", "kill-session -t repo-small-fix")
	assertCalled(t, runner.calls, "git", "-C "+path+" worktree remove "+path)
}

func TestDeleteStopsConfiguredSandbox(t *testing.T) {
	runner := &fakeRunner{hasSession: true}
	path := filepath.Join(t.TempDir(), "repo", "small-fix")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, ".radar.json"), []byte(`{"sandbox":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	deleted, err := Delete(context.Background(), runner, path, "repo-small-fix", false)
	if err != nil {
		t.Fatal(err)
	}

	if deleted.SandboxName != "radar-repo-small-fix" {
		t.Fatalf("sandbox name = %q", deleted.SandboxName)
	}
	assertCalled(t, runner.calls, "tmux", "kill-session -t repo-small-fix")
	assertCalled(t, runner.calls, "docker", "sandbox rm radar-repo-small-fix")
	assertCalled(t, runner.calls, "git", "-C "+path+" worktree remove "+path)
}

func TestDeleteSessionKillsOnlyTmuxSession(t *testing.T) {
	runner := &fakeRunner{}
	deleted, err := DeleteSession(context.Background(), runner, "repo-small-fix")
	if err != nil {
		t.Fatal(err)
	}
	if deleted.SessionName != "repo-small-fix" || deleted.Path != "" {
		t.Fatalf("unexpected deleted session: %#v", deleted)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("DeleteSession() calls = %#v, want one tmux call", runner.calls)
	}
	assertCalled(t, runner.calls, "tmux", "kill-session -t repo-small-fix")
}

func TestDeleteRefusesDirtyWorktreeBeforeKillingSession(t *testing.T) {
	runner := &dirtyRunner{fakeRunner: fakeRunner{hasSession: true}}
	path := filepath.Join(t.TempDir(), "repo", "small-fix")
	if _, err := Delete(context.Background(), runner, path, "repo-small-fix", false); err == nil {
		t.Fatal("Delete() error = nil, want dirty worktree error")
	}
	for _, call := range runner.calls {
		if call.name == "tmux" && len(call.args) > 0 && call.args[0] == "kill-session" {
			t.Fatalf("Delete() killed session before refusing dirty worktree: %#v", runner.calls)
		}
	}
}

func TestDeleteForceRemovesDirtyWorktree(t *testing.T) {
	runner := &dirtyRunner{fakeRunner: fakeRunner{hasSession: true}}
	path := filepath.Join(t.TempDir(), "repo", "small-fix")
	if _, err := Delete(context.Background(), runner, path, "repo-small-fix", true); err != nil {
		t.Fatal(err)
	}
	assertCalled(t, runner.calls, "tmux", "kill-session -t repo-small-fix")
	assertCalled(t, runner.calls, "git", "-C "+path+" worktree remove --force "+path)
}

func TestWorktreeNameSanitizesNames(t *testing.T) {
	if got, want := WorktreeName("feature/nested fix"), "feature-nested-fix"; got != want {
		t.Fatalf("WorktreeName() = %q, want %q", got, want)
	}
}

func TestBranchNameSanitizesNames(t *testing.T) {
	cases := map[string]string{
		"feature/nested fix": "feature-nested-fix",
		"...":                "workspace",
		"HEAD":               "workspace-HEAD",
	}
	for input, want := range cases {
		if got := BranchName(input); got != want {
			t.Fatalf("BranchName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSessionNameSanitizesNames(t *testing.T) {
	if got, want := SessionName("my.repo", "small fix"), "my-repo-small-fix"; got != want {
		t.Fatalf("SessionName() = %q, want %q", got, want)
	}
}

func TestSandboxNameSanitizesNames(t *testing.T) {
	if got, want := SandboxName("my.repo", "small fix"), "radar-my-repo-small-fix"; got != want {
		t.Fatalf("SandboxName() = %q, want %q", got, want)
	}
}

func assertCalled(t *testing.T, calls []call, name string, argsPrefix string) {
	t.Helper()
	for _, call := range calls {
		if call.name == name && strings.HasPrefix(strings.Join(call.args, " "), argsPrefix) {
			return
		}
	}
	t.Fatalf("%s %s was not called; calls: %#v", name, argsPrefix, calls)
}

func assertCalledContains(t *testing.T, calls []call, name string, argsPart string) {
	t.Helper()
	for _, call := range calls {
		if call.name == name && strings.Contains(strings.Join(call.args, " "), argsPart) {
			return
		}
	}
	t.Fatalf("%s containing %s was not called; calls: %#v", name, argsPart, calls)
}

type dirtyRunner struct {
	fakeRunner
}

func (r *dirtyRunner) Run(ctx context.Context, cwd string, name string, args ...string) (string, error) {
	if name == "git" && len(args) > 3 && args[len(args)-2] == "status" && args[len(args)-1] == "--porcelain" {
		r.calls = append(r.calls, call{cwd: cwd, name: name, args: args})
		return "?? .env", nil
	}
	return r.fakeRunner.Run(ctx, cwd, name, args...)
}
