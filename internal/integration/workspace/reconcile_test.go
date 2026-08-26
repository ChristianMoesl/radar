package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/integration/workspace/group"
)

func TestReconcileWorkspaceAddsAndRemovesWorktreeWithoutSandbox(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	ctx := context.Background()
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(tmp, "workspaces")
	primaryRepo := filepath.Join(tmp, "primary-source")
	targetRepo := filepath.Join(tmp, "target-source")
	initRepository(t, ctx, primaryRepo)
	initRepository(t, ctx, targetRepo)
	runGitE2E(t, ctx, targetRepo, "branch", "feature/api")
	anchor := filepath.Join(root, "XYZ-20-work")
	primaryPath := filepath.Join(anchor, "primary-source--XYZ-20-work")
	runGitE2E(t, ctx, primaryRepo, "worktree", "add", "-b", "XYZ-20-work", primaryPath, "HEAD")
	group := workspacegroup.Workspace{
		ID: workspacegroup.ID(anchor), Name: "XYZ-20-work", Path: anchor,
		Members: []workspacegroup.Member{{Repository: primaryRepo, Path: primaryPath, Branch: "XYZ-20-work", SetupScheduled: true}},
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
	expectedChangeCount := len(plan.Changes)
	stalePlanRequest.ExpectedPlanChangeCount = &expectedChangeCount
	var reconfirmLogs bytes.Buffer
	reconfirmResult, err := ApplyReconcileWorkspace(ctx, runner, slog.New(slog.NewTextHandler(&reconfirmLogs, nil)), stalePlanRequest)
	if err != nil {
		t.Fatal(err)
	}
	if reconfirmResult.OK || !reconfirmResult.ReconfirmRequired || reconfirmResult.Reason != "plan_changed" || reconfirmResult.Plan == nil || reconfirmResult.Plan.PlanID != plan.PlanID {
		t.Fatalf("reconfirm result = %+v", reconfirmResult)
	}
	for _, message := range []string{"workspace reconciliation requires reconfirmation", "expected_plan_id=different-plan", "actual_plan_id=" + plan.PlanID, "new_change_count=1", "old_change_count=1"} {
		if !strings.Contains(reconfirmLogs.String(), message) {
			t.Fatalf("reconfirmation log is missing %q:\n%s", message, reconfirmLogs.String())
		}
	}
	if _, err := os.Stat(plan.Changes[0].Path); !os.IsNotExist(err) {
		t.Fatalf("stale plan mutated the worktree: %v", err)
	}
	request.ExpectedPlanID = plan.PlanID
	var reconciliationLogs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&reconciliationLogs, nil))
	result, err := ApplyReconcileWorkspace(ctx, runner, logger, request)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{
		"workspace reconciliation started",
		"workspace reconciliation worktrees completed",
		"workspace reconciliation completed",
		"workspace_id=" + plan.WorkspaceID,
		"plan_id=" + plan.PlanID,
		"worktrees_added=1",
	} {
		if !strings.Contains(reconciliationLogs.String(), message) {
			t.Fatalf("reconciliation log is missing %q:\n%s", message, reconciliationLogs.String())
		}
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
		Workspace: anchor, WorkspaceRoot: root, Revision: revision,
		Desired: DesiredWorkspaceDescription{Worktrees: []DesiredWorkspaceWorktree{primary}},
	}
	memberPath := ""
	for _, member := range loaded.Members {
		if sameCleanPath(member.Repository, targetRepo) {
			memberPath = member.Path
		}
	}
	if err := os.WriteFile(filepath.Join(memberPath, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := PreviewReconcileWorkspace(ctx, runner, remove); err == nil {
		t.Fatal("dirty removal unexpectedly succeeded")
	} else if problem, ok := ReconcileWorkspaceErrorDetails(err); !ok || problem.Reason != "dirty_removal" || problem.ChangeCount != 1 || !strings.Contains(problem.Message, "include repository") {
		t.Fatalf("dirty removal error = %#v, %v", problem, err)
	}
	if err := os.Remove(filepath.Join(memberPath, "dirty.txt")); err != nil {
		t.Fatal(err)
	}
	removePlan, err := PreviewReconcileWorkspace(ctx, runner, remove)
	if err != nil {
		t.Fatal(err)
	}
	if len(removePlan.Warnings) != 1 || !strings.Contains(removePlan.Warnings[0], "without verifying that its commits exist remotely") {
		t.Fatalf("removal warnings = %#v", removePlan.Warnings)
	}
	remove.ExpectedPlanID = removePlan.PlanID
	removed, err := ApplyReconcileWorkspace(ctx, runner, nil, remove)
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
	branchCheck := exec.CommandContext(ctx, "git", "show-ref", "--verify", "--quiet", "refs/heads/feature/api")
	branchCheck.Dir = targetRepo
	if err := branchCheck.Run(); err == nil {
		t.Fatal("removed worktree branch still exists")
	}
	revision, err = workspaceRevision(loaded, nil)
	if err != nil {
		t.Fatal(err)
	}
	empty := ReconcileWorkspaceRequest{
		Workspace: anchor, WorkspaceRoot: root, Revision: revision,
		Desired: DesiredWorkspaceDescription{Worktrees: []DesiredWorkspaceWorktree{}, Sandbox: nil},
	}
	emptyPlan, err := PreviewReconcileWorkspace(ctx, runner, empty)
	if err != nil {
		t.Fatal(err)
	}
	empty.ExpectedPlanID = emptyPlan.PlanID
	emptied, err := ApplyReconcileWorkspace(ctx, runner, nil, empty)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := workspacegroup.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	remaining, found := workspacegroup.FindByID(registry, group.ID)
	if !emptied.OK || emptied.WorktreesRemoved != 1 || !found || len(remaining.Members) != 0 {
		t.Fatalf("empty result = %+v, workspace = %+v, found=%v", emptied, remaining, found)
	}
	if _, err := os.Stat(anchor); err != nil {
		t.Fatalf("anchor was removed with last worktree: %v", err)
	}
	if runner.sbxCalls != 0 {
		t.Fatalf("sandbox-less reconciliation made %d sbx calls", runner.sbxCalls)
	}
}

func TestReconcileWorkspaceSupportsMultipleBranchesFromOneRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	ctx := context.Background()
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(tmp, "workspaces")
	repository := filepath.Join(tmp, "source")
	initRepository(t, ctx, repository)
	anchor := filepath.Join(root, "primary")
	primaryPath := filepath.Join(anchor, "source--primary")
	runGitE2E(t, ctx, repository, "worktree", "add", "-b", "primary", primaryPath, "HEAD")
	group := workspacegroup.Workspace{
		ID: workspacegroup.ID(anchor), Name: "primary", Path: anchor,
		Members: []workspacegroup.Member{{Repository: repository, Path: primaryPath, Branch: "primary", SetupScheduled: true}},
	}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	revision, err := workspaceRevision(group, nil)
	if err != nil {
		t.Fatal(err)
	}
	primary := DesiredWorkspaceWorktree{Repository: repository, BranchMode: integration.WorkspaceBranchExisting, Branch: "primary"}
	second := DesiredWorkspaceWorktree{Repository: repository, BranchMode: integration.WorkspaceBranchNew, Name: "feature/second", Base: "HEAD"}
	request := ReconcileWorkspaceRequest{
		Workspace: primaryPath, WorkspaceRoot: root, Revision: revision,
		Desired: DesiredWorkspaceDescription{Worktrees: []DesiredWorkspaceWorktree{primary, second}},
	}
	runner := &sbxRejectingRunner{Runner: ExecRunner{}}
	plan, err := PreviewReconcileWorkspace(ctx, runner, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 1 || plan.Changes[0].Branch != "feature-second" {
		t.Fatalf("plan = %+v", plan)
	}
	request.ExpectedPlanID = plan.PlanID
	result, err := ApplyReconcileWorkspace(ctx, runner, nil, request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.WorktreesAdded != 1 {
		t.Fatalf("result = %+v", result)
	}
	stored := loadTestWorkspace(t, root, primaryPath)
	if len(stored.Members) != 2 || stored.Members[0].Repository != stored.Members[1].Repository || stored.Members[0].Branch == stored.Members[1].Branch {
		t.Fatalf("stored workspace = %+v", stored)
	}

	existingSecond := DesiredWorkspaceWorktree{Repository: repository, BranchMode: integration.WorkspaceBranchExisting, Branch: "feature-second"}
	duplicate := request
	duplicate.Revision = result.Revision
	duplicate.ExpectedPlanID = ""
	duplicate.Desired.Worktrees = []DesiredWorkspaceWorktree{primary, existingSecond, existingSecond}
	if _, err := PreviewReconcileWorkspace(ctx, runner, duplicate); err == nil {
		t.Fatal("duplicate repository branch unexpectedly succeeded")
	} else if problem, ok := ReconcileWorkspaceErrorDetails(err); !ok || problem.Reason != "duplicate_member" {
		t.Fatalf("duplicate member error = %#v, %v", problem, err)
	}

	remove := ReconcileWorkspaceRequest{
		Workspace: primaryPath, WorkspaceRoot: root, Revision: result.Revision,
		Desired: DesiredWorkspaceDescription{Worktrees: []DesiredWorkspaceWorktree{primary}},
	}
	removePlan, err := PreviewReconcileWorkspace(ctx, runner, remove)
	if err != nil {
		t.Fatal(err)
	}
	remove.ExpectedPlanID = removePlan.PlanID
	removed, err := ApplyReconcileWorkspace(ctx, runner, nil, remove)
	if err != nil {
		t.Fatal(err)
	}
	if !removed.OK || removed.WorktreesRemoved != 1 || len(loadTestWorkspace(t, root, primaryPath).Members) != 1 {
		t.Fatalf("removed = %+v", removed)
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
	anchor := filepath.Join(root, "work")
	primary := filepath.Join(anchor, "repo--work")
	common := filepath.Join(repository, ".git")
	additional := filepath.Join(root, "shared")
	for _, path := range []string{repository, primary, common} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	group := workspacegroup.Workspace{
		ID: workspacegroup.ID(anchor), Name: "work", Path: anchor,
		Sandbox: &workspacegroup.Sandbox{Name: "work-sandbox", Agent: "shell", Mounts: []string{anchor, common}},
		Members: []workspacegroup.Member{{Repository: repository, Path: primary, Branch: "work"}},
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

	request.Desired.Sandbox.AdditionalMounts = []DesiredSandboxMount{{Path: anchor}}
	if _, err := PreviewReconcileWorkspace(context.Background(), runner, request); err == nil || !strings.Contains(err.Error(), "already managed") {
		t.Fatalf("managed mount collision error = %v", err)
	}
	request.Desired.Sandbox.AdditionalMounts = []DesiredSandboxMount{{Path: root}}
	if _, err := PreviewReconcileWorkspace(context.Background(), runner, request); err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("managed mount parent overlap error = %v", err)
	}

	largeMounts := make([]DesiredSandboxMount, 0, largeEffectiveMountCount-2)
	for index := 0; index < largeEffectiveMountCount-2; index++ {
		largeMounts = append(largeMounts, DesiredSandboxMount{Path: filepath.Join(root, fmt.Sprintf("large-mount-%d", index))})
	}
	request.Desired.Sandbox.AdditionalMounts = largeMounts
	largePlan, err := PreviewReconcileWorkspace(context.Background(), runner, request)
	if err != nil {
		t.Fatal(err)
	}
	if largePlan.EffectiveMountCount != largeEffectiveMountCount {
		t.Fatalf("effective mount count = %d, want %d", largePlan.EffectiveMountCount, largeEffectiveMountCount)
	}
	if !strings.Contains(strings.Join(largePlan.Warnings, "\n"), "20 effective mounts") {
		t.Fatalf("large mount warnings = %+v", largePlan.Warnings)
	}
	if !strings.Contains(largePlan.Changes[len(largePlan.Changes)-1].Summary, "20 effective mounts") {
		t.Fatalf("sandbox recreate summary = %q", largePlan.Changes[len(largePlan.Changes)-1].Summary)
	}

	request.Desired.Sandbox.AdditionalMounts = []DesiredSandboxMount{{Path: additional}}
	request.ExpectedPlanID = plan.PlanID
	result, err := ApplyReconcileWorkspace(context.Background(), runner, nil, request)
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

func TestDesiredSandboxMountsDeduplicateCommonGitDirectoryForSameRepository(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "source")
	common := filepath.Join(repository, ".git")
	anchor := filepath.Join(root, "workspaces", "work")
	primary := filepath.Join(anchor, "source--primary")
	second := filepath.Join(anchor, "source--second")
	for _, path := range []string{repository, common, primary, second} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	group := workspacegroup.Workspace{
		ID: workspacegroup.ID(anchor), Name: "work", Path: anchor,
		Members: []workspacegroup.Member{
			{Repository: repository, Path: primary, Branch: "primary"},
			{Repository: repository, Path: second, Branch: "second"},
		},
	}
	runner := &sandboxPlanningRunner{primary: primary, common: common}
	mounts, err := desiredReconciledSandboxMounts(context.Background(), runner, group, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{anchor, common} {
		count := 0
		for _, mount := range mounts {
			if mount == path {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("mount %s occurs %d times in %+v", path, count, mounts)
		}
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
		ID: "workspace", Name: "work", Path: "/workspaces/work",
		Members: []workspacegroup.Member{{Repository: "/repos/repo", Path: "/workspaces/work/repo--work", Branch: "work"}},
	}
	runner := &reconcileResolveRunner{group: group}
	root := runner.root(t)
	_, err := PreviewReconcileWorkspace(context.Background(), runner, ReconcileWorkspaceRequest{
		Workspace: runner.group.Path, WorkspaceRoot: root, Revision: "stale",
		Desired: DesiredWorkspaceDescription{Worktrees: []DesiredWorkspaceWorktree{{Repository: group.Members[0].Repository, BranchMode: integration.WorkspaceBranchExisting, Branch: "work"}}},
	})
	if err == nil || !strings.Contains(err.Error(), "workspace changed") {
		t.Fatalf("error = %v", err)
	}
	if problem, ok := ReconcileWorkspaceErrorDetails(err); !ok || problem.Reason != "stale_revision" {
		t.Fatalf("stale revision problem = %#v", problem)
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
		r.group.Path = filepath.Join(r.base, "work")
		r.group.Members[0].Path = filepath.Join(r.group.Path, "repo--work")
		r.group.ID = workspacegroup.ID(r.group.Path)
		if err := os.MkdirAll(r.group.Path, 0o755); err != nil {
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
		return r.group.Path, nil
	}
	return "", fmt.Errorf("unexpected command: %s %s", name, strings.Join(args, " "))
}

type portStateRunner struct {
	bindings []sandboxPortBinding
	commands []string
}

func (r *portStateRunner) LookPath(string) error { return nil }
func (r *portStateRunner) Run(_ context.Context, _ string, name string, args ...string) (string, error) {
	if name != "sbx" || len(args) < 3 || args[0] != "ports" {
		return "", fmt.Errorf("unexpected command: %s %s", name, strings.Join(args, " "))
	}
	if args[2] == "--json" {
		items := make([]map[string]any, 0, len(r.bindings))
		for _, binding := range r.bindings {
			items = append(items, map[string]any{
				"host_ip": binding.HostIP, "host_port": binding.Port.HostPort,
				"sandbox_port": binding.Port.SandboxPort, "protocol": binding.Protocol,
			})
		}
		data, _ := json.Marshal(items)
		return string(data), nil
	}
	if len(args) != 4 {
		return "", fmt.Errorf("unexpected ports command: %s", strings.Join(args, " "))
	}
	r.commands = append(r.commands, strings.Join(args[2:], " "))
	port, protocol, err := parseTestPortSpec(args[3])
	if err != nil {
		return "", err
	}
	switch args[2] {
	case "--publish":
		r.bindings = append(r.bindings, sandboxPortBinding{Port: port, HostIP: "127.0.0.1", Protocol: protocol})
	case "--unpublish":
		next := r.bindings[:0]
		for _, current := range r.bindings {
			if current.Port != port {
				next = append(next, current)
			}
		}
		r.bindings = next
	default:
		return "", fmt.Errorf("unexpected action %s", args[2])
	}
	return "", nil
}

func parseTestPortSpec(spec string) (workspacegroup.SandboxPort, string, error) {
	portSpec, protocol, _ := strings.Cut(spec, "/")
	parts := strings.Split(portSpec, ":")
	if len(parts) != 2 {
		return workspacegroup.SandboxPort{}, "", fmt.Errorf("unexpected port spec %q", spec)
	}
	host, _ := strconv.Atoi(parts[0])
	sandbox, _ := strconv.Atoi(parts[1])
	return workspacegroup.SandboxPort{HostPort: host, SandboxPort: sandbox}, protocol, nil
}

func TestParseSandboxPortsJSONAcceptsSBXObjectShape(t *testing.T) {
	bindings, err := parseSandboxPortsJSON([]byte(`{"ports":[{"host_ip":"127.0.0.1","hostPort":"3000","sandboxPort":8080,"protocol":"tcp4"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 1 || bindings[0].Port.HostPort != 3000 || bindings[0].Port.SandboxPort != 8080 || bindings[0].HostIP != "127.0.0.1" || bindings[0].Protocol != "tcp4" {
		t.Fatalf("bindings = %+v", bindings)
	}
}

func TestLogicalSandboxPortsCollapsesDualStackBindings(t *testing.T) {
	bindings, err := parseSandboxPortsJSON([]byte(`[
		{"host_ip":"127.0.0.1","host_port":3002,"sandbox_port":3002,"protocol":"tcp"},
		{"host_ip":"::1","host_port":3002,"sandbox_port":3002,"protocol":"tcp"}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	ports, err := logicalSandboxPorts(bindings)
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) != 1 || ports[0].HostPort != 3002 || len(incompatibleSandboxPorts(bindings)) != 1 {
		t.Fatalf("ports=%+v incompatible=%+v", ports, incompatibleSandboxPorts(bindings))
	}
}

func TestReconcileSandboxPortsAppliesSetDifferenceAsIPv4(t *testing.T) {
	runner := &portStateRunner{bindings: []sandboxPortBinding{{
		Port: workspacegroup.SandboxPort{HostPort: 8080, SandboxPort: 8080}, HostIP: "127.0.0.1", Protocol: "tcp4",
	}}}
	published, unpublished, err := reconcileSandboxPorts(context.Background(), runner, "sandbox", []workspacegroup.SandboxPort{{HostPort: 3000, SandboxPort: 3000}})
	if err != nil {
		t.Fatal(err)
	}
	if published != 1 || unpublished != 1 || len(runner.bindings) != 1 || runner.bindings[0].Port.HostPort != 3000 || runner.bindings[0].Protocol != "tcp4" {
		t.Fatalf("published=%d unpublished=%d bindings=%+v", published, unpublished, runner.bindings)
	}
	if got := strings.Join(runner.commands, "; "); got != "--unpublish 8080:8080/tcp4; --publish 3000:3000/tcp4" {
		t.Fatalf("commands = %q", got)
	}
}

func TestReconcileSandboxPortsReplacesDualStackWithIPv4(t *testing.T) {
	port := workspacegroup.SandboxPort{HostPort: 3002, SandboxPort: 3002}
	runner := &portStateRunner{bindings: []sandboxPortBinding{
		{Port: port, HostIP: "127.0.0.1", Protocol: "tcp"},
		{Port: port, HostIP: "::1", Protocol: "tcp"},
	}}
	published, unpublished, err := reconcileSandboxPorts(context.Background(), runner, "sandbox", []workspacegroup.SandboxPort{port})
	if err != nil {
		t.Fatal(err)
	}
	if published != 1 || unpublished != 1 || len(runner.bindings) != 1 || runner.bindings[0].HostIP != "127.0.0.1" || runner.bindings[0].Protocol != "tcp4" {
		t.Fatalf("published=%d unpublished=%d bindings=%+v", published, unpublished, runner.bindings)
	}
	if got := strings.Join(runner.commands, "; "); got != "--unpublish 3002:3002/tcp; --publish 3002:3002/tcp4" {
		t.Fatalf("commands = %q", got)
	}
}

func TestReconcileSandboxPortsRemovesEveryIncompatibleProtocol(t *testing.T) {
	port := workspacegroup.SandboxPort{HostPort: 3002, SandboxPort: 3002}
	runner := &portStateRunner{bindings: []sandboxPortBinding{
		{Port: port, HostIP: "127.0.0.1", Protocol: "tcp"},
		{Port: port, HostIP: "::1", Protocol: "tcp"},
		{Port: port, HostIP: "::1", Protocol: "tcp6"},
	}}
	published, unpublished, err := reconcileSandboxPorts(context.Background(), runner, "sandbox", []workspacegroup.SandboxPort{port})
	if err != nil {
		t.Fatal(err)
	}
	if published != 1 || unpublished != 2 || len(runner.bindings) != 1 || runner.bindings[0].Protocol != "tcp4" {
		t.Fatalf("published=%d unpublished=%d bindings=%+v", published, unpublished, runner.bindings)
	}
	if got := strings.Join(runner.commands, "; "); got != "--unpublish 3002:3002/tcp; --unpublish 3002:3002/tcp6; --publish 3002:3002/tcp4" {
		t.Fatalf("commands = %q", got)
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
