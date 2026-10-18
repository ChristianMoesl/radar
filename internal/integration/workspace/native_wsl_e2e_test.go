package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	sbxclient "radar/internal/integration/sbx/client"
	sbxsettings "radar/internal/integration/sbx/settings"
	workspacegroup "radar/internal/integration/workspace/group"
)

// Separate opt-in from the Windows lifecycle probe. Nothing is installed or
// authenticated by go test; supply a dedicated authenticated test HOME.
func TestNativeWSLFilesystemMatrixE2E(t *testing.T) {
	if os.Getenv("RADAR_SBX_NATIVE_WSL_E2E") != "1" {
		t.Skip("set RADAR_SBX_NATIVE_WSL_E2E=1 and dedicated native SBX test paths")
	}
	if !sbxclient.IsWSL() {
		t.Skip("requires WSL2 with native Linux SBX and accessible KVM")
	}
	cli, home, windows := os.Getenv("RADAR_SBX_NATIVE_CLI"), os.Getenv("RADAR_SBX_NATIVE_HOME"), os.Getenv("RADAR_SBX_NATIVE_WINDOWS_TEMP")
	if cli == "" || home == "" || windows == "" {
		t.Fatal("provide RADAR_SBX_NATIVE_CLI, RADAR_SBX_NATIVE_HOME, and RADAR_SBX_NATIVE_WINDOWS_TEMP; never use real SBX state or the vault")
	}
	_, file, _, _ := runtime.Caller(0)
	script := filepath.Join(filepath.Dir(file), "..", "..", "..", "scripts", "test-native-wsl-sbx.py")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", script, "--cli", cli, "--isolated-home", home, "--windows-temp", windows)
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) } // Let the Python finally block remove owned resources.
	cmd.WaitDelay = 2 * time.Minute
	output, err := cmd.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatalf("native WSL filesystem qualification failed: %v", err)
	}
}

// Exercise real desired-state attachment, not merely direct sbx create. The
// enclosing test environment owns its isolated authenticated HOME/config/state.
func TestNativeWSLWorkspaceAttachmentE2E(t *testing.T) {
	if os.Getenv("RADAR_SBX_NATIVE_WORKSPACE_E2E") != "1" {
		t.Skip("set RADAR_SBX_NATIVE_WORKSPACE_E2E=1 inside dedicated authenticated native SBX HOME")
	}
	if !sbxclient.IsWSL() {
		t.Skip("requires native SBX on WSL2")
	}
	home := os.Getenv("RADAR_SBX_NATIVE_HOME")
	currentHome, _ := os.UserHomeDir()
	if home == "" || !filepath.IsAbs(home) || filepath.Clean(home) == filepath.Clean(currentHome) {
		t.Fatal("RADAR_SBX_NATIVE_HOME must designate dedicated test state, not the normal HOME")
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("DOCKER_CONFIG", filepath.Join(home, "docker"))
	t.Setenv(hostTempDirEnv, t.TempDir())
	withWorkspaceGOOS(t, "linux")
	workspaceWSL = true
	runner := ExecRunner{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := managedClient(runner).Preflight(ctx); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	anchor := filepath.Join(root, "Existing note workspace")
	if err := os.MkdirAll(anchor, 0700); err != nil {
		t.Fatal(err)
	}
	author := testNoteAuthor(t)
	note, err := author.PrepareWorkspaceNote(ctx, "Synthetic task")
	if err != nil {
		t.Fatal(err)
	}
	if err := author.EnsureWorkspaceNote(ctx, note); err != nil {
		t.Fatal(err)
	}
	if err := ensureNoteLink(anchor, note.Path); err != nil {
		t.Fatal(err)
	}
	group := workspacegroup.Workspace{ID: workspacegroup.ID(anchor), Name: "ABC-123", Path: anchor, SessionName: "native-test-absent", NotePath: note.Path, TaskLinkingKey: note.LinkingKey, Members: []workspacegroup.Member{}}
	if err := registerWorkspace(root, group); err != nil {
		t.Fatal(err)
	}
	revision, _ := workspaceRevision(group, nil)
	cfg := sbxsettings.Default()
	cfg.Kit.Name = "shell"
	request := ReconcileWorkspaceRequest{Workspace: anchor, WorkspaceRoot: root, Revision: revision, SandboxConfig: &cfg, Desired: DesiredWorkspaceDescription{Worktrees: []DesiredWorkspaceWorktree{}, Sandbox: &DesiredWorkspaceSandbox{}}}
	plan, err := PreviewReconcileWorkspace(ctx, runner, request)
	if err != nil {
		t.Fatal(err)
	}
	name := plan.group.Sandbox.Name
	// Preview has validated name ownership. Retain independent cleanup deadlines.
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 2*time.Minute)
		defer done()
		_, found, err := findSandbox(cleanup, runner, anchor, name)
		if err != nil {
			t.Errorf("cleanup cannot establish ownership of %s: %v", name, err)
			return
		}
		if found {
			if _, err := stopSandbox(cleanup, runner, "", name); err != nil {
				t.Errorf("sandbox cleanup %s: %v", name, err)
				return
			}
		}
		if err := removeSharedDirectory(plan.group); err != nil {
			t.Error(err)
		}
	})
	request.ExpectedPlanID = plan.PlanID
	result, err := ApplyReconcileWorkspace(ctx, runner, nil, request)
	if err != nil || !result.OK {
		t.Fatalf("attach: %+v, %v", result, err)
	}
	output, err := sbxclient.New(runner).Run(ctx, anchor, "exec", "--workdir", anchor, name, "readlink", filepath.Join(anchor, "notes.md"))
	if err != nil || output != note.Path {
		t.Fatalf("canonical link %q, %v", output, err)
	}
	stored := loadAttachmentWorkspace(t, root, anchor)
	if stored.ID != group.ID || stored.SessionName != group.SessionName || len(stored.Members) != 0 {
		t.Fatalf("identity changed: %+v", stored)
	}
	request.Revision = result.Revision
	request.ExpectedPlanID = ""
	repeated, err := ApplyReconcileWorkspace(ctx, runner, nil, request)
	if err != nil || !repeated.OK {
		t.Fatalf("repeat: %+v, %v", repeated, err)
	}
}
