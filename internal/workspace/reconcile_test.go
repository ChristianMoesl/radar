package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/workspacegroup"
)

func TestReconcileWorkspaceAddsAndRemovesWorktreeWithoutSandbox(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	ctx := context.Background()
	tmp := t.TempDir()
	root := filepath.Join(tmp, "workspaces")
	primaryRepo := filepath.Join(tmp, "primary-source")
	targetRepo := filepath.Join(tmp, "target-source")
	initRepository(t, ctx, primaryRepo)
	initRepository(t, ctx, targetRepo)
	runGitE2E(t, ctx, targetRepo, "branch", "feature/api")
	primaryPath := filepath.Join(root, "primary", "RAD-20-work")
	runGitE2E(t, ctx, primaryRepo, "worktree", "add", "-b", "RAD-20-work", primaryPath, "HEAD")
	group := workspacegroup.Workspace{
		ID: workspacegroup.ID(primaryPath), Name: "RAD-20-work", PrimaryPath: primaryPath,
		Members: []workspacegroup.Member{{Repository: primaryRepo, Path: primaryPath, Branch: "RAD-20-work", Primary: true, SetupScheduled: true}},
	}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	loaded := loadTestWorkspace(t, root, primaryPath)
	revision, err := workspaceRevision(loaded, nil)
	if err != nil {
		t.Fatal(err)
	}
	primary := DesiredWorkspaceWorktree{Repository: loaded.Members[0].Repository, BranchMode: integration.WorkspaceBranchExisting, Branch: loaded.Members[0].Branch}
	request := ReconcileWorkspaceRequest{
		Workspace: primaryPath, WorkspaceRoot: root, Revision: revision,
		Desired: DesiredWorkspaceDescription{Worktrees: []DesiredWorkspaceWorktree{
			primary,
			{Repository: targetRepo, BranchMode: integration.WorkspaceBranchExisting, Branch: "feature/api"},
		}},
	}
	runner := &sbxRejectingRunner{Runner: ExecRunner{}}
	plan, err := PreviewReconcileWorkspace(ctx, runner, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 1 || plan.Changes[0].Resource != "worktree" || plan.Changes[0].Action != "add" {
		t.Fatalf("plan = %+v", plan)
	}
	stalePlanRequest := request
	stalePlanRequest.ExpectedPlanID = "different-plan"
	if _, err := ApplyReconcileWorkspace(ctx, runner, stalePlanRequest); err == nil || !strings.Contains(err.Error(), "plan changed") {
		t.Fatalf("stale plan error = %v", err)
	}
	if _, err := os.Stat(plan.Changes[0].Path); !os.IsNotExist(err) {
		t.Fatalf("stale plan mutated the worktree: %v", err)
	}
	request.ExpectedPlanID = plan.PlanID
	result, err := ApplyReconcileWorkspace(ctx, runner, request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.WorktreesAdded != 1 || !result.SandboxReconciled || result.PortsPublished != 0 {
		t.Fatalf("result = %+v", result)
	}
	loaded = loadTestWorkspace(t, root, primaryPath)
	if loaded.Sandbox != nil || len(loaded.Members) != 2 {
		t.Fatalf("workspace = %+v", loaded)
	}

	revision, err = workspaceRevision(loaded, nil)
	if err != nil {
		t.Fatal(err)
	}
	remove := ReconcileWorkspaceRequest{
		Workspace: primaryPath, WorkspaceRoot: root, Revision: revision,
		Desired: DesiredWorkspaceDescription{Worktrees: []DesiredWorkspaceWorktree{primary}},
	}
	memberPath := ""
	for _, member := range loaded.Members {
		if !member.Primary {
			memberPath = member.Path
		}
	}
	if err := os.WriteFile(filepath.Join(memberPath, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := PreviewReconcileWorkspace(ctx, runner, remove); err == nil || !strings.Contains(err.Error(), "local changes") {
		t.Fatalf("dirty removal error = %v", err)
	}
	if err := os.Remove(filepath.Join(memberPath, "dirty.txt")); err != nil {
		t.Fatal(err)
	}
	removePlan, err := PreviewReconcileWorkspace(ctx, runner, remove)
	if err != nil {
		t.Fatal(err)
	}
	remove.ExpectedPlanID = removePlan.PlanID
	removed, err := ApplyReconcileWorkspace(ctx, runner, remove)
	if err != nil {
		t.Fatal(err)
	}
	if !removed.OK || removed.WorktreesRemoved != 1 || removed.PortsUnpublished != 0 {
		t.Fatalf("removed = %+v", removed)
	}
	loaded = loadTestWorkspace(t, root, primaryPath)
	if len(loaded.Members) != 1 {
		t.Fatalf("workspace after removal = %+v", loaded)
	}
	if _, err := os.Stat(plan.Changes[0].Path); !os.IsNotExist(err) {
		t.Fatalf("removed worktree still exists: %v", err)
	}
	if runner.sbxCalls != 0 {
		t.Fatalf("sandbox-less reconciliation made %d sbx calls", runner.sbxCalls)
	}
}

type sbxRejectingRunner struct {
	Runner
	sbxCalls int
}

func (r *sbxRejectingRunner) LookPath(name string) error {
	if name == "sbx" {
		r.sbxCalls++
		return fmt.Errorf("sbx must not be inspected")
	}
	return r.Runner.LookPath(name)
}

func (r *sbxRejectingRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	if name == "sbx" {
		r.sbxCalls++
		return "", fmt.Errorf("sbx must not be called")
	}
	return r.Runner.Run(ctx, cwd, name, args...)
}

func TestReconcileWorkspacePlansAdditionalSandboxMount(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "source")
	primary := filepath.Join(root, "repo", "work")
	common := filepath.Join(repository, ".git")
	additional := filepath.Join(root, "shared")
	for _, path := range []string{repository, primary, common} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	group := workspacegroup.Workspace{
		ID: workspacegroup.ID(primary), Name: "work", PrimaryPath: primary,
		Sandbox: &workspacegroup.Sandbox{Name: "work-sandbox", Agent: "shell", Mounts: []string{primary, common}},
		Members: []workspacegroup.Member{{Repository: repository, Path: primary, Branch: "work", Primary: true}},
	}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	group = loadTestWorkspace(t, root, primary)
	revision, err := workspaceRevision(group, nil)
	if err != nil {
		t.Fatal(err)
	}
	runner := &sandboxPlanningRunner{primary: primary, common: common, sandboxName: group.Sandbox.Name, mounts: group.Sandbox.Mounts, exists: true}
	request := ReconcileWorkspaceRequest{
		Workspace: primary, WorkspaceRoot: root, Revision: revision,
		Desired: DesiredWorkspaceDescription{
			Worktrees: []DesiredWorkspaceWorktree{{Repository: repository, BranchMode: integration.WorkspaceBranchExisting, Branch: "work"}},
			Sandbox: &DesiredWorkspaceSandbox{
				AdditionalMounts: []DesiredSandboxMount{{Path: additional}},
				Ports:            []workspacegroup.SandboxPort{},
			},
		},
	}
	plan, err := PreviewReconcileWorkspace(context.Background(), runner, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 2 || plan.Changes[0].Resource != "sandbox_mount" || plan.Changes[0].Action != "add" || plan.Changes[0].Path != additional || plan.Changes[0].ReadOnly == nil || !*plan.Changes[0].ReadOnly {
		t.Fatalf("plan changes = %+v", plan.Changes)
	}
	if plan.Changes[1].Resource != "sandbox" || plan.Changes[1].Action != "recreate" {
		t.Fatalf("plan changes = %+v", plan.Changes)
	}
	if len(plan.group.Sandbox.AdditionalMounts) != 1 || !plan.group.Sandbox.AdditionalMounts[0].ReadOnly {
		t.Fatalf("desired additional mounts = %+v", plan.group.Sandbox.AdditionalMounts)
	}
	if !containsPath(plan.group.Sandbox.Mounts, additional+":ro") {
		t.Fatalf("sandbox mounts = %+v", plan.group.Sandbox.Mounts)
	}

	writable := false
	request.Desired.Sandbox.AdditionalMounts = []DesiredSandboxMount{{Path: additional, ReadOnly: &writable}}
	writablePlan, err := PreviewReconcileWorkspace(context.Background(), runner, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(writablePlan.Warnings) != 2 || !strings.Contains(writablePlan.Warnings[1], "write access") {
		t.Fatalf("writable mount warnings = %+v", writablePlan.Warnings)
	}

	request.Desired.Sandbox.AdditionalMounts = []DesiredSandboxMount{{Path: primary}}
	if _, err := PreviewReconcileWorkspace(context.Background(), runner, request); err == nil || !strings.Contains(err.Error(), "already managed") {
		t.Fatalf("managed mount collision error = %v", err)
	}

	request.Desired.Sandbox.AdditionalMounts = []DesiredSandboxMount{{Path: additional}}
	request.ExpectedPlanID = plan.PlanID
	result, err := ApplyReconcileWorkspace(context.Background(), runner, request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || !result.SandboxReconciled || result.MountsAdded != 1 || result.MountsRemoved != 0 {
		t.Fatalf("result = %+v", result)
	}
	stored := loadTestWorkspace(t, root, primary)
	if len(stored.Sandbox.AdditionalMounts) != 1 || stored.Sandbox.AdditionalMounts[0].Path != additional || !stored.Sandbox.AdditionalMounts[0].ReadOnly {
		t.Fatalf("stored additional mounts = %+v", stored.Sandbox.AdditionalMounts)
	}
	if !containsPath(runner.mounts, additional+":ro") {
		t.Fatalf("actual sandbox mounts = %+v", runner.mounts)
	}
}

type sandboxPlanningRunner struct {
	primary     string
	common      string
	sandboxName string
	mounts      []string
	exists      bool
}

func (r *sandboxPlanningRunner) LookPath(string) error { return nil }
func (r *sandboxPlanningRunner) Run(_ context.Context, _ string, name string, args ...string) (string, error) {
	command := name + " " + strings.Join(args, " ")
	if name == "sbx" && len(args) > 0 {
		switch args[0] {
		case "rm":
			r.exists = false
			return "", nil
		case "create":
			if len(args) < 5 || args[1] != "--name" || args[2] != r.sandboxName {
				return "", fmt.Errorf("unexpected create command: %s", command)
			}
			r.mounts = append([]string(nil), args[4:]...)
			r.exists = true
			return "", nil
		}
	}
	switch command {
	case "git rev-parse --show-toplevel":
		return r.primary, nil
	case "git rev-parse --path-format=absolute --git-common-dir":
		return r.common, nil
	case "sbx ls --json":
		sandboxes := []any{}
		if r.exists {
			sandboxes = append(sandboxes, map[string]any{"name": r.sandboxName, "agent": "shell", "status": "running", "workspaces": r.mounts})
		}
		data, _ := json.Marshal(map[string]any{"sandboxes": sandboxes})
		return string(data), nil
	case "sbx ports " + r.sandboxName + " --json":
		return `[]`, nil
	default:
		return "", fmt.Errorf("unexpected command: %s", command)
	}
}

func TestReconcileWorkspaceRejectsStaleRevision(t *testing.T) {
	group := workspacegroup.Workspace{
		ID: "workspace", Name: "work", PrimaryPath: "/workspaces/repo/work",
		Members: []workspacegroup.Member{{Repository: "/repos/repo", Path: "/workspaces/repo/work", Branch: "work", Primary: true}},
	}
	runner := &reconcileResolveRunner{group: group}
	_, err := PreviewReconcileWorkspace(context.Background(), runner, ReconcileWorkspaceRequest{
		Workspace: group.PrimaryPath, WorkspaceRoot: runner.root(t), Revision: "stale",
		Desired: DesiredWorkspaceDescription{Worktrees: []DesiredWorkspaceWorktree{{Repository: group.Members[0].Repository, BranchMode: integration.WorkspaceBranchExisting, Branch: "work"}}},
	})
	if err == nil || !strings.Contains(err.Error(), "workspace changed") {
		t.Fatalf("error = %v", err)
	}
}

type reconcileResolveRunner struct {
	group workspacegroup.Workspace
	base  string
}

func (r *reconcileResolveRunner) root(t *testing.T) string {
	t.Helper()
	if r.base == "" {
		r.base = t.TempDir()
		r.group.PrimaryPath = filepath.Join(r.base, "repo", "work")
		r.group.Members[0].Path = r.group.PrimaryPath
		r.group.ID = workspacegroup.ID(r.group.PrimaryPath)
		if err := os.MkdirAll(r.group.PrimaryPath, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := workspacegroup.Save(r.base, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{r.group}}); err != nil {
			t.Fatal(err)
		}
		registry, err := workspacegroup.Load(r.base)
		if err != nil {
			t.Fatal(err)
		}
		r.group = registry.Workspaces[0]
	}
	return r.base
}

func (r *reconcileResolveRunner) LookPath(string) error { return nil }
func (r *reconcileResolveRunner) Run(_ context.Context, _ string, name string, args ...string) (string, error) {
	if name == "git" && strings.Join(args, " ") == "rev-parse --show-toplevel" {
		return r.group.PrimaryPath, nil
	}
	return "", fmt.Errorf("unexpected command: %s %s", name, strings.Join(args, " "))
}

type portStateRunner struct {
	ports []workspacegroup.SandboxPort
}

func (r *portStateRunner) LookPath(string) error { return nil }
func (r *portStateRunner) Run(_ context.Context, _ string, name string, args ...string) (string, error) {
	if name != "sbx" || len(args) < 3 || args[0] != "ports" {
		return "", fmt.Errorf("unexpected command: %s %s", name, strings.Join(args, " "))
	}
	if args[2] == "--json" {
		data, _ := json.Marshal(r.ports)
		return string(data), nil
	}
	if len(args) != 4 {
		return "", fmt.Errorf("unexpected ports command: %s", strings.Join(args, " "))
	}
	parts := strings.Split(args[3], ":")
	host, _ := strconv.Atoi(parts[0])
	sandbox, _ := strconv.Atoi(parts[1])
	port := workspacegroup.SandboxPort{HostPort: host, SandboxPort: sandbox}
	switch args[2] {
	case "--publish":
		r.ports = append(r.ports, port)
	case "--unpublish":
		next := r.ports[:0]
		for _, current := range r.ports {
			if current != port {
				next = append(next, current)
			}
		}
		r.ports = next
	default:
		return "", fmt.Errorf("unexpected action %s", args[2])
	}
	return "", nil
}

func TestParseSandboxPortsJSONAcceptsSBXObjectShape(t *testing.T) {
	ports, err := parseSandboxPortsJSON([]byte(`{"ports":[{"hostPort":"3000","sandboxPort":8080}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) != 1 || ports[0].HostPort != 3000 || ports[0].SandboxPort != 8080 {
		t.Fatalf("ports = %+v", ports)
	}
}

func TestReconcileSandboxPortsAppliesSetDifference(t *testing.T) {
	runner := &portStateRunner{ports: []workspacegroup.SandboxPort{{HostPort: 8080, SandboxPort: 8080}}}
	published, unpublished, err := reconcileSandboxPorts(context.Background(), runner, "sandbox", []workspacegroup.SandboxPort{{HostPort: 3000, SandboxPort: 3000}})
	if err != nil {
		t.Fatal(err)
	}
	if published != 1 || unpublished != 1 || len(runner.ports) != 1 || runner.ports[0].HostPort != 3000 {
		t.Fatalf("published=%d unpublished=%d ports=%+v", published, unpublished, runner.ports)
	}
}

func initRepository(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitE2E(t, ctx, path, "init")
	runGitE2E(t, ctx, path, "config", "user.email", "radar@example.test")
	runGitE2E(t, ctx, path, "config", "user.name", "Radar Test")
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitE2E(t, ctx, path, "add", "README.md")
	runGitE2E(t, ctx, path, "commit", "-m", "initial")
}

func loadTestWorkspace(t *testing.T, root, memberPath string) workspacegroup.Workspace {
	t.Helper()
	registry, err := workspacegroup.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	group, ok := workspacegroup.FindByMemberPath(registry, memberPath)
	if !ok {
		t.Fatalf("workspace for %s was not found: %+v", memberPath, registry)
	}
	return group
}
