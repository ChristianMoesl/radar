package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"radar/internal/integration"
	sbxsettings "radar/internal/integration/sbx/settings"
	workspacegroup "radar/internal/integration/workspace/group"
)

type attachmentRunner struct {
	fakeRunner
	sandboxName                   string
	mounts                        []string
	exists, failCreate, failPorts bool
	creates, removes              int
}

func (r *attachmentRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	if name == "sbx" && len(args) > 0 {
		switch args[0] {
		case "ls":
			if r.sbxListOutput != "" {
				return r.sbxListOutput, nil
			}
			rows := []any{}
			if r.exists {
				rows = append(rows, map[string]any{"name": r.sandboxName, "id": "test-runtime", "workspaces": r.mounts, "status": "running"})
			}
			data, _ := json.Marshal(rows)
			return string(data), nil
		case "create":
			r.creates++
			// Runtime effects must follow intent persistence, including failed creates.
			_, group, found, err := RegisteredWorkspace(cwd, "")
			if err != nil || !found || group.Sandbox == nil {
				return "", errors.New("sandbox intent was not persisted before create")
			}
			if r.failCreate {
				return "", errors.New("invalid sandbox kit")
			}
			r.exists = true
			r.sandboxName = args[2]
			r.mounts = sandboxCreateMountArgs(args)
			return "", nil
		case "rm":
			r.removes++
			r.exists = false
			return "", nil
		case "ports":
			if r.failPorts {
				return "", errors.New("occupied host port")
			}
			return "[]", nil
		}
	}
	return r.fakeRunner.Run(ctx, cwd, name, args...)
}

func attachmentFixture(t *testing.T) (ReconcileWorkspaceRequest, workspacegroup.Workspace, *attachmentRunner) {
	t.Helper()
	withWorkspaceGOOS(t, "darwin")
	t.Setenv(hostTempDirEnv, t.TempDir())
	root := configureWorkspaceRoot(t)
	anchor := filepath.Join(root, "Existing workspace")
	if err := os.MkdirAll(anchor, 0700); err != nil {
		t.Fatal(err)
	}
	author := testNoteAuthor(t)
	note, err := author.PrepareWorkspaceNote(context.Background(), "Existing task")
	if err != nil {
		t.Fatal(err)
	}
	if err := author.EnsureWorkspaceNote(context.Background(), note); err != nil {
		t.Fatal(err)
	}
	if err := ensureNoteLink(anchor, note.Path); err != nil {
		t.Fatal(err)
	}
	group := workspacegroup.Workspace{ID: workspacegroup.ID(anchor), Name: "Existing workspace", Path: anchor, SessionName: "same-session", NotePath: note.Path, TaskLinkingKey: note.LinkingKey, Members: []workspacegroup.Member{}}
	if err := registerWorkspace(root, group); err != nil {
		t.Fatal(err)
	}
	revision, err := workspaceRevision(group, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := sbxsettings.Default()
	cfg.Kit.Name = "shell"
	req := ReconcileWorkspaceRequest{Workspace: anchor, WorkspaceRoot: root, Revision: revision, SandboxConfig: &cfg, Desired: DesiredWorkspaceDescription{Worktrees: []DesiredWorkspaceWorktree{}, Sandbox: &DesiredWorkspaceSandbox{}}}
	r := &attachmentRunner{fakeRunner: fakeRunner{hasSession: true}}
	return req, group, r
}

func TestAttachSandboxToExistingNoteOnlyWorkspace(t *testing.T) {
	req, before, runner := attachmentFixture(t)
	noteBefore, _ := os.ReadFile(before.NotePath)
	ctx := context.Background()
	plan, err := PreviewReconcileWorkspace(ctx, runner, req)
	if err != nil {
		t.Fatal(err)
	}
	attached := false
	for _, change := range plan.Changes {
		if change.Resource == "sandbox" {
			attached = change.Action == "attach"
		}
	}
	if !attached {
		t.Fatalf("not a distinct attachment: %+v", plan.Changes)
	}
	if _, err := os.Stat(plan.group.Sandbox.SharedDirectory); !os.IsNotExist(err) {
		t.Fatalf("preview created shared directory: %v", err)
	}
	if stored := loadAttachmentWorkspace(t, req.WorkspaceRoot, before.Path); stored.Sandbox != nil {
		t.Fatal("preview persisted intent")
	}
	req.ExpectedPlanID = plan.PlanID
	result, err := ApplyReconcileWorkspace(ctx, runner, nil, req)
	if err != nil || !result.OK {
		t.Fatalf("%+v, %v", result, err)
	}
	after := loadAttachmentWorkspace(t, req.WorkspaceRoot, before.Path)
	if after.ID != before.ID || after.Path != before.Path || after.NotePath != before.NotePath || after.SessionName != before.SessionName || after.TaskLinkingKey != before.TaskLinkingKey || len(after.Members) != 0 {
		t.Fatalf("identity changed: %+v", after)
	}
	if target, err := os.Readlink(filepath.Join(before.Path, "notes.md")); err != nil || target != before.NotePath {
		t.Fatalf("link %q, %v", target, err)
	}
	if noteAfter, _ := os.ReadFile(before.NotePath); !reflect.DeepEqual(noteBefore, noteAfter) {
		t.Fatal("canonical note changed")
	}
	if !contains(after.Sandbox.Mounts, filepath.Dir(before.NotePath)) {
		t.Fatalf("missing canonical note mount: %v", after.Sandbox.Mounts)
	}
	req.Revision = result.Revision
	req.ExpectedPlanID = ""
	again, err := ApplyReconcileWorkspace(ctx, runner, nil, req)
	if err != nil || !again.OK || runner.creates != 1 {
		t.Fatalf("not idempotent: %+v, %v, creates=%d", again, err, runner.creates)
	}
	req.Revision = again.Revision
	req.Desired.Sandbox = nil
	if _, err := PreviewReconcileWorkspace(ctx, runner, req); err == nil {
		t.Fatal("silently detached sandbox")
	}
}

func TestSandboxAttachmentFailurePersistsIntentAndRetries(t *testing.T) {
	for _, phase := range []string{"create", "ports"} {
		t.Run(phase, func(t *testing.T) {
			req, before, r := attachmentFixture(t)
			r.failCreate = phase == "create"
			r.failPorts = phase == "ports"
			result, err := ApplyReconcileWorkspace(context.Background(), r, nil, req)
			if err != nil || result.OK || !result.Retryable {
				t.Fatalf("%+v, %v", result, err)
			}
			stored := loadAttachmentWorkspace(t, req.WorkspaceRoot, before.Path)
			if stored.Sandbox == nil || stored.NotePath != before.NotePath {
				t.Fatal("lost partial attachment intent")
			}
			r.failCreate = false
			r.failPorts = false
			req.Revision, _ = workspaceRevision(stored, stored.Sandbox.Ports)
			retry, err := ApplyReconcileWorkspace(context.Background(), r, nil, req)
			if err != nil || !retry.OK {
				t.Fatalf("retry %+v, %v", retry, err)
			}
		})
	}
}

func TestAttachmentRejectsCollisionAndChangedRecipe(t *testing.T) {
	req, before, r := attachmentFixture(t)
	plan, err := PreviewReconcileWorkspace(context.Background(), r, req)
	if err != nil {
		t.Fatal(err)
	}
	req.ExpectedPlanID = plan.PlanID
	req.SandboxConfig.Kit.Name = "different-kit"
	changed, err := ApplyReconcileWorkspace(context.Background(), r, nil, req)
	if err != nil || !changed.ReconfirmRequired || r.creates != 0 {
		t.Fatalf("recipe bypassed confirmation: %+v, %v", changed, err)
	}
	r.exists = true
	r.sandboxName = plan.group.Sandbox.Name
	r.mounts = []string{before.Path}
	if _, err := PreviewReconcileWorkspace(context.Background(), r, req); err == nil || !strings.Contains(err.Error(), "occupied") {
		t.Fatalf("collision accepted: %v", err)
	}
	if r.removes != 0 {
		t.Fatal("removed unrelated sandbox")
	}
}

func TestAttachmentPreservesMultipleExistingMembers(t *testing.T) {
	req, before, r := attachmentFixture(t)
	common := filepath.Join(t.TempDir(), ".git")
	r.gitCommonDir = common
	for _, name := range []string{"first", "second"} {
		repo := t.TempDir()
		path := filepath.Join(before.Path, name)
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		before.Members = append(before.Members, workspacegroup.Member{Repository: repo, Path: path, Branch: name, SetupScheduled: true})
		req.Desired.Worktrees = append(req.Desired.Worktrees, DesiredWorkspaceWorktree{Repository: repo, BranchMode: integration.WorkspaceBranchExisting, Branch: name})
	}
	if err := registerWorkspace(req.WorkspaceRoot, before); err != nil {
		t.Fatal(err)
	}
	req.Revision, _ = workspaceRevision(before, nil)
	plan, err := PreviewReconcileWorkspace(context.Background(), r, req)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.group.Members, before.Members) || !contains(plan.group.Sandbox.Mounts, common) {
		t.Fatalf("lost members/mounts: %+v", plan.group)
	}
}

func loadAttachmentWorkspace(t *testing.T, root, path string) workspacegroup.Workspace {
	t.Helper()
	registry, err := workspacegroup.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	group, found := workspacegroup.FindByContainingPath(registry, path)
	if !found {
		t.Fatal("workspace not found")
	}
	return group
}

func TestInspectRetainsBrokenSandboxContextAndDesiredPorts(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(fmt.Sprint(malformed), func(t *testing.T) {
			request, group, r := attachmentFixture(t)
			group.Sandbox = &workspacegroup.Sandbox{Name: "missing-runtime", Agent: "shell", Ports: []workspacegroup.SandboxPort{{HostPort: 59123, SandboxPort: 3000}}}
			if err := registerWorkspace(request.WorkspaceRoot, group); err != nil {
				t.Fatal(err)
			}
			if malformed {
				r.fakeRunner.sbxListOutput = "malformed"
			}
			context, err := InspectWorkspace(context.Background(), r, group.Path, request.WorkspaceRoot)
			if err != nil {
				t.Fatal(err)
			}
			if context.WorkspaceID != group.ID || context.Note == nil || context.Sandbox == nil || context.Sandbox.RuntimeError == "" || !context.Capabilities.Sandbox || context.Capabilities.SandboxAttachment {
				t.Fatalf("lost registered repair context: %+v", context)
			}
			if len(context.Desired.Sandbox.Ports) != 1 || context.Desired.Sandbox.Ports[0].HostPort != 59123 {
				t.Fatalf("lost retained desired ports: %+v", context.Desired)
			}
		})
	}
}

func TestMalformedManagedInventoryIsNotAbsence(t *testing.T) {
	for _, output := range []string{`[{}]`, `[{"name":""}]`, `[{"name":"duplicate"},{"name":"duplicate"}]`} {
		if _, err := listSandboxes(context.Background(), &fakeRunner{sbxListOutput: output}); err == nil {
			t.Fatalf("accepted malformed inventory %s", output)
		}
	}
}

func TestSandboxAttachmentCapabilityIsNotRuntimeReadiness(t *testing.T) {
	for _, platform := range []string{"darwin", "linux", "freebsd"} {
		t.Run(platform, func(t *testing.T) {
			request, group, r := attachmentFixture(t)
			withWorkspaceGOOS(t, platform)
			inspected, err := InspectWorkspace(context.Background(), r, group.Path, request.WorkspaceRoot)
			if err != nil {
				t.Fatal(err)
			}
			if inspected.Capabilities.SandboxAttachment != (platform != "freebsd") || inspected.Capabilities.Sandbox || inspected.Desired.Sandbox != nil {
				t.Fatalf("eligibility changed intent: %+v", inspected)
			}
		})
	}
	request, group, _ := attachmentFixture(t)
	withWorkspaceGOOS(t, "linux")
	workspaceWSL = true
	inspected, err := InspectWorkspace(context.Background(), &windowsOnlyRunner{}, group.Path, request.WorkspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	if inspected.Capabilities.SandboxAttachment {
		t.Fatal("Windows-only backend advertised managed attachment")
	}
}
