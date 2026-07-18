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
	repo          string
	gitCommonDir  string
	hasSession    bool
	sbxListOutput string
	calls         []call
}

func (f *fakeRunner) LookPath(string) error { return nil }

func (f *fakeRunner) Run(_ context.Context, cwd string, name string, args ...string) (string, error) {
	f.calls = append(f.calls, call{cwd: cwd, name: name, args: args})
	if name == "git" && strings.Join(args, " ") == "rev-parse --show-toplevel" {
		return f.repo, nil
	}
	if name == "git" && strings.Join(args, " ") == "rev-parse --path-format=absolute --git-common-dir" {
		if f.gitCommonDir != "" {
			return f.gitCommonDir, nil
		}
		if f.repo != "" {
			return filepath.Join(f.repo, ".git"), nil
		}
		return filepath.Join(cwd, ".git"), nil
	}
	if name == "tmux" && len(args) > 0 && args[0] == "has-session" {
		if !f.hasSession {
			return "", errors.New("missing")
		}
		return "", nil
	}
	if name == "git" && len(args) > 0 && args[0] == "show-ref" {
		return "", errors.New("missing")
	}
	if name == "git" && len(args) > 0 && strings.Join(args, " ") == "worktree list --porcelain" {
		return "", nil
	}
	if name == "git" && len(args) > 4 && args[0] == "worktree" && args[1] == "add" && args[2] == "-b" {
		return "", os.MkdirAll(args[4], 0o755)
	}
	if name == "git" && len(args) > 3 && args[0] == "worktree" && args[1] == "add" {
		return "", os.MkdirAll(args[2], 0o755)
	}
	if name == "sbx" && strings.Join(args, " ") == "ls --json" {
		if f.sbxListOutput != "" {
			return f.sbxListOutput, nil
		}
		return `{"sandboxes":[]}`, nil
	}
	return "", nil
}

func TestExecRunnerSkipsPathEntriesWithExecFormatErrors(t *testing.T) {
	badBin := t.TempDir()
	goodBin := t.TempDir()
	name := "radar-test-tool"
	if err := os.WriteFile(filepath.Join(badBin, name), []byte("not a runnable executable\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(goodBin, name), []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", badBin+string(os.PathListSeparator)+goodBin)

	output, err := ExecRunner{}.Run(context.Background(), "", name)
	if err != nil {
		t.Fatal(err)
	}
	if output != "ok" {
		t.Fatalf("ExecRunner.Run() = %q, want ok", output)
	}
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

func TestCreateStartsPiOnHostWithConfiguredSandbox(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	repo := t.TempDir()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".radar.json"), []byte(`{
  "sandbox": {}
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{repo: repo}

	workspace, err := Create(context.Background(), runner, CreateOptions{
		Repo:            repo,
		Name:            "small fix",
		Base:            "origin/main",
		WorkspaceRoot:   root,
		SandboxTemplate: "example/radar-sandbox:test",
	})
	if err != nil {
		t.Fatal(err)
	}

	if want := SandboxName(filepath.Base(repo), "small fix"); workspace.SandboxName != want {
		t.Fatalf("sandbox name = %q, want %q", workspace.SandboxName, want)
	}
	assertCalled(t, runner.calls, "sbx", "create --name "+workspace.SandboxName+" --template example/radar-sandbox:test shell "+workspace.Path+" "+filepath.Join(repo, ".git"))
	assertCalledContains(t, runner.calls, "tmux", "pi --session-id")
	assertNotCalledContains(t, runner.calls, "tmux", "sbx exec")
	assertNotCalledContains(t, runner.calls, "tmux", "PI_CODING_AGENT_DIR=")
	assertNotCalledContains(t, runner.calls, "tmux", "PI_CODING_AGENT_SESSION_DIR=")
	assertNotCalledContains(t, runner.calls, "tmux", "pi --approve")
	assertCalledContains(t, runner.calls, "tmux", "--session-id")
	assertCalledContains(t, runner.calls, "tmux", workspace.SessionName)
	assertNotCalled(t, runner.calls, "docker")
	assertNotCalledContains(t, runner.calls, "tmux", "default-command")
	assertNotCalledContains(t, runner.calls, "tmux", "-n shell")
}

func TestCreateStartsSandboxEnabledByUserConfig(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	repo := t.TempDir()
	root := t.TempDir()
	runner := &fakeRunner{repo: repo}

	workspace, err := Create(context.Background(), runner, CreateOptions{
		Repo:            repo,
		Name:            "small fix",
		Base:            "origin/main",
		WorkspaceRoot:   root,
		Sandbox:         true,
		SandboxTemplate: "example/radar-sandbox:test",
	})
	if err != nil {
		t.Fatal(err)
	}

	if want := SandboxName(filepath.Base(repo), "small fix"); workspace.SandboxName != want {
		t.Fatalf("sandbox name = %q, want %q", workspace.SandboxName, want)
	}
	assertCalled(t, runner.calls, "sbx", "create --name "+workspace.SandboxName+" --template example/radar-sandbox:test shell "+workspace.Path+" "+filepath.Join(repo, ".git"))
	assertCalledContains(t, runner.calls, "tmux", "pi --session-id")
	assertNotCalledContains(t, runner.calls, "tmux", "sbx exec")
	assertNotCalledContains(t, runner.calls, "tmux", "PI_CODING_AGENT_SESSION_DIR=")
	assertNotCalledContains(t, runner.calls, "tmux", "pi --approve")
}

func TestCreateRejectsConfiguredSandboxOutsideMacOS(t *testing.T) {
	withWorkspaceGOOS(t, "linux")
	repo := t.TempDir()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".radar.json"), []byte(`{"sandbox": {}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{repo: repo}

	_, err := Create(context.Background(), runner, CreateOptions{
		Repo:          repo,
		Name:          "small fix",
		Base:          "origin/main",
		WorkspaceRoot: root,
	})
	if err == nil || !strings.Contains(err.Error(), "only supported on macOS") {
		t.Fatalf("Create() error = %v, want macOS-only sandbox error", err)
	}
	assertNotCalled(t, runner.calls, "sbx")
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

func TestCreatePreservesExplicitBranchName(t *testing.T) {
	repo := t.TempDir()
	root := t.TempDir()
	runner := &fakeRunner{repo: repo}

	workspace, err := Create(context.Background(), runner, CreateOptions{
		Repo:          repo,
		Name:          "feature/nested fix",
		Branch:        "feature/nested-fix",
		Base:          "origin/feature/nested-fix",
		WorkspaceRoot: root,
	})
	if err != nil {
		t.Fatal(err)
	}

	if workspace.Branch != "feature/nested-fix" {
		t.Fatalf("workspace branch = %q, want explicit branch", workspace.Branch)
	}
	assertCalled(t, runner.calls, "git", "worktree add -b feature/nested-fix "+workspace.Path+" origin/feature/nested-fix")
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

func TestCreateSessionMountsLinkedWorktreeGitDirectoryInNewSandbox(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	root := t.TempDir()
	path := filepath.Join(root, "worktrees", "small-fix")
	commonDir := filepath.Join(root, "repo", ".git")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{gitCommonDir: commonDir}

	created, err := CreateSessionWithOptions(context.Background(), runner, CreateSessionOptions{
		Path:            path,
		SessionName:     "repo-small-fix",
		Sandbox:         true,
		SandboxTemplate: "example/radar-sandbox:test",
	})
	if err != nil {
		t.Fatal(err)
	}

	assertCalled(t, runner.calls, "sbx", "create --name "+created.SandboxName+" --template example/radar-sandbox:test shell "+path+" "+commonDir)
}

func TestSandboxMountsDoesNotRepeatGitDirectoryInsideWorkspace(t *testing.T) {
	path := t.TempDir()
	runner := &fakeRunner{gitCommonDir: filepath.Join(path, ".git")}

	mounts, err := sandboxMounts(context.Background(), runner, path)
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 1 || mounts[0] != path {
		t.Fatalf("sandbox mounts = %#v, want workspace only", mounts)
	}
}

func TestCreateSessionUsesLinkedSandboxForHostPiTools(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	path := filepath.Join(t.TempDir(), "repo", "small-fix")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{sbxListOutput: `{"sandboxes":[{"name":"existing-sandbox"}]}`}

	created, err := CreateSessionWithOptions(context.Background(), runner, CreateSessionOptions{Path: path, SessionName: "repo-small-fix", SandboxName: "existing-sandbox"})
	if err != nil {
		t.Fatal(err)
	}

	if created.SandboxName != "existing-sandbox" {
		t.Fatalf("sandbox name = %q, want existing-sandbox", created.SandboxName)
	}
	assertCalled(t, runner.calls, "sbx", "ls --json")
	assertNotCalledContains(t, runner.calls, "sbx", "create")
	assertCalledContains(t, runner.calls, "tmux", "pi --session-id 'repo-small-fix'")
	assertNotCalledContains(t, runner.calls, "tmux", "sbx exec")
	assertNotCalledContains(t, runner.calls, "tmux", "PI_CODING_AGENT_SESSION_DIR=")
	assertNotCalledContains(t, runner.calls, "tmux", "pi --approve")
	assertCalled(t, runner.calls, "tmux", "new-window -t repo-small-fix:")
}

func TestSandboxCommandErrorSuggestsLoginForAuthFailure(t *testing.T) {
	err := sbxCommandError(errors.New("sbx create failed: docker Hub session has no access token (run 'sbx login' to refresh)"))
	if err == nil || err.Error() != "sbx is not signed in; run sbx login" {
		t.Fatalf("sbxCommandError() = %v, want login suggestion", err)
	}
}

func TestRemoveSessionKillsExistingSession(t *testing.T) {
	runner := &fakeRunner{hasSession: true}
	deleted, err := RemoveSession(context.Background(), runner, "repo-small-fix")
	if err != nil {
		t.Fatal(err)
	}
	if deleted.SessionName != "repo-small-fix" || deleted.Path != "" {
		t.Fatalf("unexpected deleted session: %#v", deleted)
	}
	assertCalled(t, runner.calls, "tmux", "has-session -t repo-small-fix")
	assertCalled(t, runner.calls, "tmux", "kill-session -t repo-small-fix")
}

func TestRemoveSessionIgnoresMissingSession(t *testing.T) {
	runner := &fakeRunner{}
	if _, err := RemoveSession(context.Background(), runner, "repo-small-fix"); err != nil {
		t.Fatal(err)
	}
	assertNotCalledContains(t, runner.calls, "tmux", "kill-session")
}

func TestRemoveWorktreeRefusesDirtyWorktree(t *testing.T) {
	runner := &dirtyRunner{}
	path := filepath.Join(t.TempDir(), "repo", "small-fix")
	if _, err := RemoveWorktree(context.Background(), runner, path, false); err == nil {
		t.Fatal("RemoveWorktree() error = nil, want dirty worktree error")
	}
	assertNotCalledContains(t, runner.calls, "git", "worktree remove")
}

func TestRemoveWorktreeForceRemovesDirtyWorktree(t *testing.T) {
	runner := &dirtyRunner{}
	path := filepath.Join(t.TempDir(), "repo", "small-fix")
	if _, err := RemoveWorktree(context.Background(), runner, path, true); err != nil {
		t.Fatal(err)
	}
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
	if got, want := SandboxName("my.repo", "small fix"), "small-fix-1d922a0c"; got != want {
		t.Fatalf("SandboxName() = %q, want %q", got, want)
	}
}

func TestSandboxNameTruncatesLongNames(t *testing.T) {
	got := SandboxName("rb3ca-experience-center", "DPSCAP-602 Page Asset variables displayed incorrectly for none asset based configurations")
	if len(got) > maxSandboxNameLength {
		t.Fatalf("SandboxName() length = %d, want <= %d: %q", len(got), maxSandboxNameLength, got)
	}
	if !strings.HasPrefix(got, "DPSCAP-602-Page-Asset-variables-displayed-incorrectly") {
		t.Fatalf("SandboxName() = %q, want readable workspace prefix", got)
	}
	if !strings.Contains(got, "-") {
		t.Fatalf("SandboxName() = %q, want hash suffix", got)
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

func assertNotCalled(t *testing.T, calls []call, name string) {
	t.Helper()
	for _, call := range calls {
		if call.name == name {
			t.Fatalf("%s was called unexpectedly; calls: %#v", name, calls)
		}
	}
}

func assertNotCalledContains(t *testing.T, calls []call, name string, argsPart string) {
	t.Helper()
	for _, call := range calls {
		if call.name == name && strings.Contains(strings.Join(call.args, " "), argsPart) {
			t.Fatalf("%s containing %s was called unexpectedly; calls: %#v", name, argsPart, calls)
		}
	}
}

func withWorkspaceGOOS(t *testing.T, value string) {
	t.Helper()
	previous := workspaceGOOS
	workspaceGOOS = value
	t.Cleanup(func() { workspaceGOOS = previous })
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
