package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/tmuxlayout"
)

type call struct {
	cwd  string
	name string
	args []string
}

type fakeRunner struct {
	repo            string
	gitCommonDir    string
	hasSession      bool
	failSetupWindow bool
	sbxListOutput   string
	refs            map[string]bool
	worktrees       string
	calls           []call
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
	if name == "tmux" && len(args) > 0 && (args[0] == "new-session" || args[0] == "new-window" || args[0] == "split-window") {
		if f.failSetupWindow && args[0] == "new-window" && strings.Contains(strings.Join(args, " "), "-n setup") {
			return "", errors.New("setup window failed")
		}
		return "@1 %1", nil
	}
	if name == "git" && len(args) > 0 && args[0] == "show-ref" {
		if f.refs[args[len(args)-1]] {
			return "", nil
		}
		return "", errors.New("missing")
	}
	if name == "git" && len(args) > 0 && strings.Join(args, " ") == "worktree list --porcelain" {
		return f.worktrees, nil
	}
	if name == "git" && len(args) > 5 && args[0] == "worktree" && args[1] == "add" && args[2] == "--track" {
		return "", os.MkdirAll(args[5], 0o755)
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
		BranchMode:    integration.WorkspaceBranchNew,
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
	assertNotCalled(t, runner.calls, "sh")
	assertCalledContains(t, runner.calls, "tmux", "pi --model 'anthropic/claude-sonnet-4' --thinking 'high' --session-id '"+workspace.SessionName+"'")
	assertCalled(t, runner.calls, "tmux", "new-session -d -s "+workspace.SessionName)
	assertCalled(t, runner.calls, "tmux", "new-window -t "+workspace.SessionName+":")
	assertCalledContains(t, runner.calls, "tmux", "new-window -t "+workspace.SessionName+": -n setup -c "+workspace.Path+" -P -F #{window_id} #{pane_id}")
	assertSetupWindowIsForeground(t, runner.calls)
	assertCalled(t, runner.calls, "tmux", "set-option -p -t %1 remain-on-exit off")
	assertCalledContains(t, runner.calls, "tmux", "send-keys -l -t %1 sh -lc 'pnpm install --frozen-lockfile' && exit")
	assertCalled(t, runner.calls, "tmux", "send-keys -t %1 Enter")
	assertNotCalledContains(t, runner.calls, "tmux", "kill-window")
	assertCallOrder(t, runner.calls,
		call{name: "tmux", args: []string{"select-pane", "-t"}},
		call{name: "tmux", args: []string{"new-window", "-t", workspace.SessionName + ":", "-n", "setup"}},
	)
	assertCalled(t, runner.calls, "tmux", "switch-client -t "+workspace.SessionName)
}

func TestCreateTracksExistingOriginBranch(t *testing.T) {
	repo := t.TempDir()
	root := t.TempDir()
	runner := &fakeRunner{
		repo: repo,
		refs: map[string]bool{"refs/remotes/origin/main": true},
	}

	created, err := Create(context.Background(), runner, CreateOptions{
		Repo:          repo,
		BranchMode:    integration.WorkspaceBranchExisting,
		Branch:        "main",
		WorkspaceRoot: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "main" || created.Branch != "main" {
		t.Fatalf("created workspace = %+v", created)
	}
	assertCalled(t, runner.calls, "git", "worktree add --track -b main "+created.Path+" origin/main")
}

func TestCreateNewBranchRejectsExistingOriginBranch(t *testing.T) {
	repo := t.TempDir()
	runner := &fakeRunner{
		repo: repo,
		refs: map[string]bool{"refs/remotes/origin/feature/existing": true},
	}

	_, err := Create(context.Background(), runner, CreateOptions{
		Repo:          repo,
		BranchMode:    integration.WorkspaceBranchNew,
		Name:          "feature/existing",
		Branch:        "feature/existing",
		Base:          "origin/main",
		WorkspaceRoot: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "choose the existing branch") {
		t.Fatalf("Create() error = %v, want existing branch guidance", err)
	}
}

func TestCreateExistingBranchRejectsSourceCheckout(t *testing.T) {
	repo := t.TempDir()
	runner := &fakeRunner{
		repo: repo,
		worktrees: strings.Join([]string{
			"worktree " + repo,
			"HEAD abc",
			"branch refs/heads/main",
			"",
		}, "\n"),
	}

	_, err := Create(context.Background(), runner, CreateOptions{
		Repo:          repo,
		BranchMode:    integration.WorkspaceBranchExisting,
		Branch:        "main",
		WorkspaceRoot: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "detach that checkout") {
		t.Fatalf("Create() error = %v, want detach guidance", err)
	}
}

func TestCreateUsesConfiguredTmuxWindowsAndHorizontalPanes(t *testing.T) {
	repo := t.TempDir()
	root := t.TempDir()
	runner := &fakeRunner{repo: repo}

	created, err := Create(context.Background(), runner, CreateOptions{
		BranchMode:    integration.WorkspaceBranchNew,
		Repo:          repo,
		Name:          "small fix",
		Base:          "origin/main",
		WorkspaceRoot: root,
		Model:         "anthropic/claude-sonnet-4",
		Tmux: tmuxlayout.Config{Windows: []tmuxlayout.Window{{
			Name:   "workspace",
			Layout: "horizontal",
			Panes: []tmuxlayout.Pane{
				{Command: "pi-wrapper " + tmuxlayout.PiArgsPlaceholder},
				{Command: "nvim ."},
			},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	assertCalledContains(t, runner.calls, "tmux", "new-session -d -s "+created.SessionName+" -n workspace")
	assertCalledContains(t, runner.calls, "tmux", "pi-wrapper --model 'anthropic/claude-sonnet-4' --session-id '")
	assertCalledContains(t, runner.calls, "tmux", "split-window -d -t %1")
	assertCalledContains(t, runner.calls, "tmux", "nvim .")
	assertCalledContains(t, runner.calls, "tmux", "select-layout -t @1 even-horizontal")
	assertNotCalledContains(t, runner.calls, "tmux", "new-window")
	assertNotCalledContains(t, runner.calls, "tmux", tmuxlayout.PiArgsPlaceholder)
}

func TestCreateStartsPiOnHostWithConfiguredSandbox(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".radar.json"), []byte(`{
  "sbx": {"enabled": true}
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{repo: repo}

	workspace, err := Create(context.Background(), runner, CreateOptions{
		BranchMode:     integration.WorkspaceBranchNew,
		Repo:           repo,
		Name:           "small fix",
		Base:           "origin/main",
		WorkspaceRoot:  root,
		SandboxKitName: "radar",
		SandboxKitPath: "~/kits/radar",
	})
	if err != nil {
		t.Fatal(err)
	}

	if want := SandboxName(filepath.Base(repo), "small fix"); workspace.SandboxName != want {
		t.Fatalf("sandbox name = %q, want %q", workspace.SandboxName, want)
	}
	assertCalled(t, runner.calls, "sbx", "create --name "+workspace.SandboxName+" --kit "+filepath.Join(home, "kits", "radar")+" radar "+workspace.Path+" "+filepath.Join(repo, ".git"))
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

func TestCreateSchedulesSetupInsideConfiguredSandbox(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	repo := t.TempDir()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".radar.json"), []byte(`{
  "setup": ["pnpm install --frozen-lockfile", "pnpm build"],
  "sbx": {"enabled": true}
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{repo: repo}

	created, err := Create(context.Background(), runner, CreateOptions{
		BranchMode:    integration.WorkspaceBranchNew,
		Repo:          repo,
		Name:          "small fix",
		Base:          "origin/main",
		WorkspaceRoot: root,
	})
	if err != nil {
		t.Fatal(err)
	}

	assertCalled(t, runner.calls, "sbx", "create --name "+created.SandboxName)
	assertNotCalledContains(t, runner.calls, "sbx", "exec")
	assertNotCalled(t, runner.calls, "sh")
	assertCalledContains(t, runner.calls, "tmux", "new-window -t "+created.SessionName+": -n setup -c "+created.Path+" -P -F #{window_id} #{pane_id} sbx exec --workdir '"+created.Path+"' '"+created.SandboxName+"' sh -i")
	assertSetupWindowIsForeground(t, runner.calls)
	assertCalled(t, runner.calls, "tmux", "set-option -p -t %1 remain-on-exit off")
	assertCalledContains(t, runner.calls, "tmux", "send-keys -l -t %1 sh -lc 'pnpm install --frozen-lockfile' && sh -lc 'pnpm build' && exit")
	assertCalled(t, runner.calls, "tmux", "send-keys -t %1 Enter")
	assertNotCalledContains(t, runner.calls, "tmux", "remain-on-exit on")
	assertNotCalledContains(t, runner.calls, "tmux", "kill-window")
	assertCallOrder(t, runner.calls,
		call{name: "sbx", args: []string{"create", "--name", created.SandboxName}},
		call{name: "tmux", args: []string{"new-session", "-d", "-s", created.SessionName}},
	)
}

func TestCreateDoesNotScheduleSetupWithoutCommands(t *testing.T) {
	repo := t.TempDir()
	runner := &fakeRunner{repo: repo}

	_, err := Create(context.Background(), runner, CreateOptions{
		BranchMode:    integration.WorkspaceBranchNew,
		Repo:          repo,
		Name:          "small fix",
		Base:          "origin/main",
		WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}

	assertNotCalledContains(t, runner.calls, "tmux", "-n setup")
}

func TestCreatePreservesWorkspaceWhenSetupCannotBeScheduled(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".radar.json"), []byte(`{"setup":["pnpm install"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{repo: repo, failSetupWindow: true}

	created, err := Create(context.Background(), runner, CreateOptions{
		BranchMode:    integration.WorkspaceBranchNew,
		Repo:          repo,
		Name:          "small fix",
		Base:          "origin/main",
		WorkspaceRoot: t.TempDir(),
		Switch:        true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(created.Warning, "setup window failed") {
		t.Fatalf("workspace warning = %q, want setup scheduling failure", created.Warning)
	}
	if _, err := os.Stat(created.Path); err != nil {
		t.Fatalf("workspace path was not preserved: %v", err)
	}
	assertNotCalledContains(t, runner.calls, "tmux", "kill-session")
	assertNotCalledContains(t, runner.calls, "git", "worktree remove")
	assertCalled(t, runner.calls, "tmux", "switch-client -t "+created.SessionName)
}

func TestCreateDoesNotRerunSetupWhenOpeningExistingWorkspace(t *testing.T) {
	repo := t.TempDir()
	root := t.TempDir()
	path := filepath.Join(root, filepath.Base(repo), "existing")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".radar.json"), []byte(`{"setup":["pnpm install"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{
		repo: repo,
		worktrees: strings.Join([]string{
			"worktree " + path,
			"HEAD abc",
			"branch refs/heads/existing",
			"",
		}, "\n"),
	}

	_, err := Create(context.Background(), runner, CreateOptions{
		BranchMode:    integration.WorkspaceBranchExisting,
		Repo:          repo,
		Branch:        "existing",
		WorkspaceRoot: root,
	})
	if err != nil {
		t.Fatal(err)
	}

	assertNotCalledContains(t, runner.calls, "tmux", "-n setup")
}

func TestSetupInteractiveCommandExitsOnlyAfterEveryCommandSucceeds(t *testing.T) {
	command := setupInteractiveCommand([]string{"printf \"it's ready\"", "pnpm build"})
	want := `sh -lc 'printf "it'"'"'s ready"' && sh -lc 'pnpm build' && exit`
	if command != want {
		t.Fatalf("setupInteractiveCommand() = %q, want %q", command, want)
	}
}

func TestCreateStartsSandboxEnabledByUserConfig(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	repo := t.TempDir()
	root := t.TempDir()
	shared := t.TempDir()
	runner := &fakeRunner{repo: repo}

	workspace, err := Create(context.Background(), runner, CreateOptions{
		BranchMode:              integration.WorkspaceBranchNew,
		Repo:                    repo,
		Name:                    "small fix",
		Base:                    "origin/main",
		WorkspaceRoot:           root,
		Sandbox:                 true,
		SandboxKitName:          "radar",
		AdditionalSandboxMounts: []string{shared},
	})
	if err != nil {
		t.Fatal(err)
	}

	if want := SandboxName(filepath.Base(repo), "small fix"); workspace.SandboxName != want {
		t.Fatalf("sandbox name = %q, want %q", workspace.SandboxName, want)
	}
	assertCalled(t, runner.calls, "sbx", "create --name "+workspace.SandboxName+" radar "+workspace.Path+" "+filepath.Join(repo, ".git")+" "+shared)
	assertCalledContains(t, runner.calls, "tmux", "pi --session-id")
	assertNotCalledContains(t, runner.calls, "tmux", "sbx exec")
	assertNotCalledContains(t, runner.calls, "tmux", "PI_CODING_AGENT_SESSION_DIR=")
	assertNotCalledContains(t, runner.calls, "tmux", "pi --approve")
}

func TestWorkspaceSandboxConfigAppliesRepoOverrides(t *testing.T) {
	disabled := false
	settings := workspaceSandboxConfig(RepoConfig{SBX: &SandboxConfig{
		Enabled:          &disabled,
		Kit:              &SandboxKitConfig{Name: "repo-kit", Path: "/repo/kit"},
		AdditionalMounts: []string{"/repo/shared"},
	}}, true, "user-kit", "/user/kit", []string{"/user/shared"})

	if settings.Enabled {
		t.Fatal("sandbox enabled = true, want repo override to disable it")
	}
	if settings.Kit.Name != "repo-kit" || settings.Kit.Path != "/repo/kit" {
		t.Fatalf("sandbox kit = %#v", settings.Kit)
	}
	wantMounts := []string{"/user/shared", "/repo/shared"}
	if strings.Join(settings.AdditionalMounts, "\n") != strings.Join(wantMounts, "\n") {
		t.Fatalf("additional mounts = %#v, want %#v", settings.AdditionalMounts, wantMounts)
	}
}

func TestWorkspaceSandboxConfigInheritsUserSettings(t *testing.T) {
	settings := workspaceSandboxConfig(RepoConfig{SBX: &SandboxConfig{
		AdditionalMounts: []string{"/repo/shared"},
	}}, true, "user-kit", "/user/kit", []string{"/user/shared"})

	if !settings.Enabled || settings.Kit.Name != "user-kit" || settings.Kit.Path != "/user/kit" {
		t.Fatalf("sandbox settings = %+v, want inherited enabled state and kit", settings)
	}
}

func TestCreateRejectsConfiguredSandboxOutsideMacOS(t *testing.T) {
	withWorkspaceGOOS(t, "linux")
	repo := t.TempDir()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".radar.json"), []byte(`{"sbx":{"enabled":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{repo: repo}

	_, err := Create(context.Background(), runner, CreateOptions{
		BranchMode:    integration.WorkspaceBranchNew,
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
		BranchMode:    integration.WorkspaceBranchNew,
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
		BranchMode:    integration.WorkspaceBranchNew,
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
		BranchMode:    integration.WorkspaceBranchNew,
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
		BranchMode:    integration.WorkspaceBranchNew,
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
		BranchMode:    integration.WorkspaceBranchNew,
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
		BranchMode:    integration.WorkspaceBranchNew,
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
		Path:           path,
		SessionName:    "repo-small-fix",
		Sandbox:        true,
		SandboxKitName: "radar",
	})
	if err != nil {
		t.Fatal(err)
	}

	assertCalled(t, runner.calls, "sbx", "create --name "+created.SandboxName+" radar "+path+" "+commonDir)
}

func TestSandboxMountsDoesNotRepeatGitDirectoryInsideWorkspace(t *testing.T) {
	path := t.TempDir()
	runner := &fakeRunner{gitCommonDir: filepath.Join(path, ".git")}

	mounts, err := sandboxMounts(context.Background(), runner, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 1 || mounts[0] != path {
		t.Fatalf("sandbox mounts = %#v, want workspace only", mounts)
	}
}

func TestSandboxMountsExpandsAndDeduplicatesAdditionalMounts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := t.TempDir()
	runner := &fakeRunner{gitCommonDir: filepath.Join(path, ".git")}

	mounts, err := sandboxMounts(context.Background(), runner, path, []string{"~/shared", "", path, "~/shared"})
	if err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(home, "shared")
	want := []string{path, shared}
	if strings.Join(mounts, "\n") != strings.Join(want, "\n") {
		t.Fatalf("sandbox mounts = %#v, want %#v", mounts, want)
	}
	if info, err := os.Stat(shared); err != nil || !info.IsDir() {
		t.Fatalf("additional sandbox mount was not created as a directory: info=%v err=%v", info, err)
	}
}

func TestSandboxMountsRejectsRelativeAdditionalMount(t *testing.T) {
	path := t.TempDir()
	runner := &fakeRunner{gitCommonDir: filepath.Join(path, ".git")}

	_, err := sandboxMounts(context.Background(), runner, path, []string{"shared"})
	if err == nil || !strings.Contains(err.Error(), "must be absolute") {
		t.Fatalf("sandboxMounts() error = %v, want absolute path error", err)
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

func assertSetupWindowIsForeground(t *testing.T, calls []call) {
	t.Helper()
	for _, call := range calls {
		args := strings.Join(call.args, " ")
		if call.name != "tmux" || !strings.Contains(args, "-n setup") {
			continue
		}
		for _, arg := range call.args {
			if arg == "-d" {
				t.Fatalf("setup window was created detached; call: %#v", call)
			}
		}
		return
	}
	t.Fatalf("setup window was not created; calls: %#v", calls)
}

func assertCallOrder(t *testing.T, calls []call, first call, second call) {
	t.Helper()
	firstIndex := -1
	secondIndex := -1
	for i, actual := range calls {
		args := strings.Join(actual.args, " ")
		if firstIndex == -1 && actual.name == first.name && strings.HasPrefix(args, strings.Join(first.args, " ")) {
			firstIndex = i
		}
		if secondIndex == -1 && actual.name == second.name && strings.HasPrefix(args, strings.Join(second.args, " ")) {
			secondIndex = i
		}
	}
	if firstIndex == -1 || secondIndex == -1 || firstIndex >= secondIndex {
		t.Fatalf("expected %#v before %#v; calls: %#v", first, second, calls)
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
