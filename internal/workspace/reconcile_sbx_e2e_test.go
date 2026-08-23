package workspace

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"radar/internal/workspacegroup"
)

func TestReconcileAdditionalSandboxMountReadOnlyE2E(t *testing.T) {
	ctx, runner, root, primary, sandboxName := setupSandboxMountE2E(t)
	mount := filepath.Join(t.TempDir(), "read-only")
	if err := os.MkdirAll(mount, 0o755); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(mount, "fixture.txt")
	if err := os.WriteFile(fixture, []byte("visible in sbx\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := reconcileAdditionalMountsE2E(t, ctx, runner, root, primary, []DesiredSandboxMount{{Path: mount}})
	if result.MountsAdded != 1 || result.MountsRemoved != 0 || !result.SandboxReconciled {
		t.Fatalf("result = %+v", result)
	}
	if output := runSbxE2E(t, ctx, runner, sandboxName, "cat", fixture); output != "visible in sbx" {
		t.Fatalf("mounted fixture = %q", output)
	}
	if _, err := runner.Run(ctx, "", "sbx", "exec", sandboxName, "sh", "-lc", "printf changed > "+shellQuote(fixture)); err == nil {
		t.Fatal("write to read-only sandbox mount succeeded")
	}
	stored := loadTestWorkspace(t, root, primary)
	if len(stored.Sandbox.AdditionalMounts) != 1 || stored.Sandbox.AdditionalMounts[0].Path != mount || !stored.Sandbox.AdditionalMounts[0].ReadOnly {
		t.Fatalf("stored additional mounts = %+v", stored.Sandbox.AdditionalMounts)
	}
}

func TestReconcileAdditionalSandboxMountWritableAndRemovalE2E(t *testing.T) {
	ctx, runner, root, primary, sandboxName := setupSandboxMountE2E(t)
	mount := filepath.Join(t.TempDir(), "writable")
	if err := os.MkdirAll(mount, 0o755); err != nil {
		t.Fatal(err)
	}
	writable := false

	result := reconcileAdditionalMountsE2E(t, ctx, runner, root, primary, []DesiredSandboxMount{{Path: mount, ReadOnly: &writable}})
	if result.MountsAdded != 1 || result.MountsRemoved != 0 || !result.SandboxReconciled {
		t.Fatalf("add result = %+v", result)
	}
	created := filepath.Join(mount, "created-in-sbx.txt")
	runSbxE2E(t, ctx, runner, sandboxName, "sh", "-lc", "printf 'written in sbx\\n' > "+shellQuote(created))
	data, err := os.ReadFile(created)
	if err != nil {
		t.Fatalf("read sandbox-created host file: %v", err)
	}
	if string(data) != "written in sbx\n" {
		t.Fatalf("sandbox-created host file = %q", data)
	}

	result = reconcileAdditionalMountsE2E(t, ctx, runner, root, primary, []DesiredSandboxMount{})
	if result.MountsAdded != 0 || result.MountsRemoved != 1 || !result.SandboxReconciled {
		t.Fatalf("remove result = %+v", result)
	}
	runSbxE2E(t, ctx, runner, sandboxName, "sh", "-lc", "test ! -e "+shellQuote(mount))
	stored := loadTestWorkspace(t, root, primary)
	if len(stored.Sandbox.AdditionalMounts) != 0 {
		t.Fatalf("stored additional mounts = %+v", stored.Sandbox.AdditionalMounts)
	}
}

func setupSandboxMountE2E(t *testing.T) (context.Context, ExecRunner, string, string, string) {
	t.Helper()
	if os.Getenv("RADAR_SBX_E2E") != "1" {
		t.Skip("set RADAR_SBX_E2E=1 to run SBX E2E tests")
	}
	if runtime.GOOS != "darwin" {
		t.Skip("SBX workspace tests require macOS")
	}
	for _, dependency := range []string{"git", "sbx"} {
		if _, err := exec.LookPath(dependency); err != nil {
			t.Skipf("%s not found", dependency)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	runner := ExecRunner{}
	tmp := t.TempDir()
	root := filepath.Join(tmp, "workspaces")
	repository := filepath.Join(tmp, "source")
	initRepository(t, ctx, repository)
	primary := filepath.Join(root, "repo--work")
	runGitE2E(t, ctx, repository, "worktree", "add", "-b", "sbx-e2e", primary, "HEAD")
	common := filepath.Join(repository, ".git")
	sandboxName := fmt.Sprintf("radar-mount-e2e-%d-%d", os.Getpid(), time.Now().UnixNano())

	if _, err := runner.Run(ctx, primary, "sbx", "create", "--name", sandboxName, "shell", primary, common); err != nil {
		t.Fatalf("create SBX sandbox: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		if _, err := stopSandbox(cleanupCtx, runner, primary, sandboxName); err != nil {
			t.Errorf("remove SBX sandbox %s: %v", sandboxName, err)
		}
	})

	group := workspacegroup.Workspace{
		ID: workspacegroup.ID(primary), Name: "work", PrimaryPath: primary,
		Sandbox: &workspacegroup.Sandbox{Name: sandboxName, Agent: "shell", Mounts: []string{primary, common}},
		Members: []workspacegroup.Member{{Repository: repository, Path: primary, Branch: "sbx-e2e", Primary: true, SetupScheduled: true}},
	}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	return ctx, runner, root, primary, sandboxName
}

func reconcileAdditionalMountsE2E(t *testing.T, ctx context.Context, runner ExecRunner, root, primary string, mounts []DesiredSandboxMount) ReconcileWorkspaceResult {
	t.Helper()
	workspaceContext, err := InspectWorkspace(ctx, runner, primary, root)
	if err != nil {
		t.Fatal(err)
	}
	if workspaceContext.Desired.Sandbox == nil {
		t.Fatal("workspace context has no sandbox desired state")
	}
	workspaceContext.Desired.Sandbox.AdditionalMounts = mounts
	request := ReconcileWorkspaceRequest{
		Workspace: primary, WorkspaceRoot: root, Revision: workspaceContext.Revision,
		Desired: workspaceContext.Desired,
	}
	plan, err := PreviewReconcileWorkspace(ctx, runner, request)
	if err != nil {
		t.Fatal(err)
	}
	if !hasWorkspaceChange(plan.Changes, "sandbox_mount") || !hasWorkspaceChange(plan.Changes, "sandbox") {
		t.Fatalf("plan changes = %+v", plan.Changes)
	}
	request.ExpectedPlanID = plan.PlanID
	result, err := ApplyReconcileWorkspace(ctx, runner, nil, request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Retryable || strings.TrimSpace(result.Error) != "" {
		t.Fatalf("result = %+v", result)
	}
	return result
}

func hasWorkspaceChange(changes []WorkspaceChange, resource string) bool {
	for _, change := range changes {
		if change.Resource == resource {
			return true
		}
	}
	return false
}

func runSbxE2E(t *testing.T, ctx context.Context, runner ExecRunner, sandboxName string, args ...string) string {
	t.Helper()
	command := append([]string{"exec", sandboxName}, args...)
	output, err := runner.Run(ctx, "", "sbx", command...)
	if err != nil {
		t.Fatalf("sbx %s failed: %v", strings.Join(command, " "), err)
	}
	return output
}
