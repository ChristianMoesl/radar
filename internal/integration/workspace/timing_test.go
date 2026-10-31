package workspace

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	workspacegroup "radar/internal/integration/workspace/group"
)

func TestApplyLogsSeparatePhasesWithoutPrivateReadinessOutput(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	root, group, _ := inspectionFixture(t)
	group.Members = nil
	group.Sandbox.ReadyCommand = []string{"sh", "-lc", readySecret}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	revision, err := workspaceRevision(group, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := ReconcileWorkspaceRequest{Workspace: group.Path, WorkspaceRoot: root, Revision: revision, prepareOnly: true, Desired: DesiredWorkspaceDescription{Worktrees: []DesiredWorkspaceWorktree{}, Sandbox: &DesiredWorkspaceSandbox{Ports: []workspacegroup.SandboxPort{}}}}
	runner := &readyRunner{Runner: &sandboxPlanningRunner{sandboxName: group.Sandbox.Name, mounts: group.Sandbox.Mounts, exists: true}, ready: func(context.Context) (string, error) { return readySecret, nil }}
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, nil))
	result, err := ApplyReconcileWorkspace(context.Background(), runner, logger, request)
	if err != nil || !result.OK {
		t.Fatalf("apply: %+v %v", result, err)
	}
	for _, phase := range []string{"operation_lock_wait", "note_lock_wait", "workspace_planning", "worktrees", "sbx_version", "sbx_rm", "sbx_create", "sandbox_readiness", "port_reconciliation", "workspace_setup", "workspace_apply"} {
		if !strings.Contains(output.String(), "phase="+phase+" ") {
			t.Fatalf("missing phase %s:\n%s", phase, output.String())
		}
	}
	if strings.Contains(output.String(), readySecret) {
		t.Fatal("timing diagnostics exposed readiness argv/output")
	}
}

type deadlineProbeRunner struct {
	Runner
	deadlines []time.Time
}

func (r *deadlineProbeRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	if name == "sbx" {
		deadline, ok := ctx.Deadline()
		if !ok {
			return "", errors.New("unbounded provider read")
		}
		r.deadlines = append(r.deadlines, deadline)
	}
	return r.Runner.Run(ctx, cwd, name, args...)
}
func TestSandboxReadsBoundBackgroundContexts(t *testing.T) {
	_, group, base := inspectionFixture(t)
	runner := &deadlineProbeRunner{Runner: base}
	start := time.Now()
	if _, err := listSandboxes(context.Background(), runner); err != nil {
		t.Fatal(err)
	}
	if _, err := listSandboxPortBindings(context.Background(), runner, group.Sandbox.Name); err != nil {
		t.Fatal(err)
	}
	if len(runner.deadlines) != 2 {
		t.Fatal("missing probes")
	}
	for _, deadline := range runner.deadlines {
		if deadline.Before(start) || deadline.After(start.Add(sandboxObservationTimeout+time.Second)) {
			t.Fatalf("unbounded deadline: %v", deadline)
		}
	}
}

func TestSetupBatchDoesNotRepeatPreflightPerMember(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	root, group, runner := inspectionFixture(t)
	// Run apply on a plan with existing members and a matching runtime. Only the
	// setup phase needs new work; no Git/registry planning assumptions are mocked.
	shared, err := newSharedDirectory(group.Path)
	if err != nil {
		t.Fatal(err)
	}
	group.Sandbox.SharedDirectory = shared
	group.Sandbox.Mounts = append(group.Sandbox.Mounts, shared)
	runner.group = group
	for _, member := range group.Members {
		if err := os.MkdirAll(member.Repository, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(member.Repository, ".radar.yaml"), []byte("setup:\n  - echo private-setup\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// A setup-only apply skips preview construction, but must still perform a
	// fresh runtime preflight and a fresh setup preflight before executing hooks.
	runnerWithSetup := setupProbeRunner{inspectionProbeRunner: runner}
	plan := ReconcileWorkspacePlan{WorkspaceID: group.ID, root: root, group: group, prepareOnly: true}
	result, err := applyWorkspacePlan(context.Background(), runnerWithSetup, nil, ReconcileWorkspaceRequest{}, plan)
	if err != nil || !result.OK {
		t.Fatalf("setup apply: %+v %v", result, err)
	}
	if runner.calls["sbx version"] != 2 {
		t.Fatalf("expected one runtime plus one setup preflight for four members: %v", runner.calls)
	}
	if runner.calls["sbx exec"] != 4 {
		t.Fatalf("setup hooks not completed: %v", runner.calls)
	}
}

type setupProbeRunner struct{ *inspectionProbeRunner }

func (r setupProbeRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	if name == "sbx" && args[0] == "exec" {
		r.mu.Lock()
		r.calls["sbx exec"]++
		r.mu.Unlock()
		return "", ctx.Err()
	}
	return r.inspectionProbeRunner.Run(ctx, cwd, name, args...)
}

type failingSetupPreflightRunner struct {
	setupProbeRunner
	versions int
}

func (r *failingSetupPreflightRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	if name == "sbx" && args[0] == "version" {
		r.versions++
		if r.versions == 2 {
			return "", errors.New("setup preflight unavailable")
		}
	}
	return r.setupProbeRunner.Run(ctx, cwd, name, args...)
}
func TestSetupBatchPreflightFailurePreservesCompletionContract(t *testing.T) {
	for _, prepareOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "interactive warning", true: "preparation error"}[prepareOnly], func(t *testing.T) {
			withWorkspaceGOOS(t, "darwin")
			root, group, base := inspectionFixture(t)
			shared, err := newSharedDirectory(group.Path)
			if err != nil {
				t.Fatal(err)
			}
			group.Sandbox.SharedDirectory = shared
			group.Sandbox.Mounts = append(group.Sandbox.Mounts, shared)
			base.group = group
			for _, member := range group.Members {
				if err := os.MkdirAll(member.Repository, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(member.Repository, ".radar.yaml"), []byte("setup:\n  - echo private-setup\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			runner := &failingSetupPreflightRunner{setupProbeRunner: setupProbeRunner{base}}
			plan := ReconcileWorkspacePlan{WorkspaceID: group.ID, root: root, group: group, prepareOnly: prepareOnly}
			result, err := applyWorkspacePlan(context.Background(), runner, nil, ReconcileWorkspaceRequest{}, plan)
			if prepareOnly {
				if err == nil || result.OK {
					t.Fatalf("preparation ignored failed preflight: %+v %v", result, err)
				}
			} else if err != nil || !result.OK || !strings.Contains(result.Warning, "could not be started") {
				t.Fatalf("interactive setup contract changed: %+v %v", result, err)
			}
			if base.calls["sbx exec"] != 0 || base.calls["sh -lc"] != 0 {
				t.Fatal("setup ran despite unavailable runtime")
			}
			registry, err := workspacegroup.Load(root)
			if err != nil {
				t.Fatal(err)
			}
			for _, member := range registry.Workspaces[0].Members {
				if member.SetupScheduled {
					t.Fatal("failed setup marked scheduled")
				}
			}
		})
	}
}
