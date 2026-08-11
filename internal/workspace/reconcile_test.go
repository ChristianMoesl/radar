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
