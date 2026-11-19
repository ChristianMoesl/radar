package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"radar/internal/cleanup"
	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

func writerRequest(t *testing.T, root string, group workspacegroup.Workspace) ReconcileWorkspaceRequest {
	t.Helper()
	revision, err := workspaceRevision(group, sandboxPorts(group))
	if err != nil {
		t.Fatal(err)
	}
	request := ReconcileWorkspaceRequest{Workspace: group.Path, WorkspaceRoot: root, Revision: revision}
	if group.Sandbox != nil {
		request.Desired.Sandbox = &DesiredWorkspaceSandbox{Ports: group.Sandbox.Ports}
		for _, mount := range group.Sandbox.AdditionalMounts {
			readOnly := mount.ReadOnly
			request.Desired.Sandbox.AdditionalMounts = append(request.Desired.Sandbox.AdditionalMounts, DesiredSandboxMount{Path: mount.Path, ReadOnly: &readOnly})
		}
	}
	for _, member := range group.Members {
		request.Desired.Worktrees = append(request.Desired.Worktrees, DesiredWorkspaceWorktree{Repository: member.Repository, Branch: member.Branch, BranchMode: integration.WorkspaceBranchExisting})
	}
	return request
}

// Providers make no deletions; the actual Service still acquires cleanup's
// exclusive canonical path/SBX admission before invoking this callback.
func assertWriterCleanupConflict(t *testing.T, path, sandbox string) {
	t.Helper()
	started, resume := make(chan struct{}), make(chan struct{})
	close(resume)
	target := protocol.CleanupTarget{Source: "fixture", Kind: "workspace", Path: path}
	var provider integration.CleanupProvider = activationCleanup{started: started, resume: resume}
	if sandbox != "" {
		target.Source, target.Kind, target.ResourceID = "sbx", "sandbox", sandbox
		provider = namedSandboxCleanup{activationCleanup{started: started, resume: resume}}
	}
	_, err := cleanup.New([]integration.CleanupProvider{provider}).Execute(context.Background(), protocol.CleanupPreview{Targets: []protocol.CleanupTarget{target}}, cleanup.ExecuteOptions{Mode: integration.CleanupConfirmed})
	if err == nil || !strings.Contains(err.Error(), "in use by another operation") {
		t.Error("cleanup bypassed active workspace writer", err)
	}
	select {
	case <-started:
		t.Error("conflicting cleanup reached provider")
	default:
	}
}

func TestWorkspaceWriterReconcileRetainsCleanupBundleAndAgentCompatibility(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			root, group := activationFixture(t)
			// Long-lived agents retain shared deletion leases. Writer exclusion
			// must not upgrade these into incompatible blanket exclusivity.
			releaseAgent, err := workspacelease.Acquire(group.Path, false)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if releaseAgent != nil {
					releaseAgent()
				}
			}()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			base := &fakeRunner{}
			checked := false
			runner := activationRunner{Runner: base, run: func(ctx context.Context, cwd, name string, args ...string) (string, error) {
				if name == "tmux" && args[0] == "has-session" {
					checked = true
					assertWriterCleanupConflict(t, group.Path, "")
					assertWriterCleanupConflict(t, group.Members[0].Path, "")
					if outcome == "cancelled" {
						cancel()
						return "", ctx.Err()
					}
				}
				if outcome == "failure" && name == "tmux" && args[0] == "display-message" {
					return "", errors.New("fixture identity failure")
				}
				return base.Run(ctx, cwd, name, args...)
			}}
			result, err := ApplyReconcileWorkspace(ctx, runner, nil, writerRequest(t, root, group))
			if !checked || (err != nil) != (outcome != "success") || (outcome == "success" && !result.OK) {
				t.Fatal("reconciliation outcome", checked, result, err)
			}
			releaseAgent()
			releaseAgent = nil
			assertActivationLeasesReleased(t, group)
		})
	}
}

func TestWorkspaceWriterRecreationRetainsCleanupBundle(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	for _, outcome := range []string{"success", "failure", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
			req, groups, runner := recreationFixture(t, 1)
			group := groups[0]
			group.Members = []workspacegroup.Member{{Path: filepath.Join(group.Path, "missing-member"), Repository: t.TempDir(), Branch: "fixture"}}
			saveRecreationGroups(t, req.WorkspaceRoot, []workspacegroup.Workspace{group})
			req = recreateFixturePlan(t, context.Background(), req, runner)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			checked := false
			runner.beforeRun = func(args []string) {
				if args[0] != "rm" {
					return
				}
				checked = true
				assertWriterCleanupConflict(t, group.Path, "")
				assertWriterCleanupConflict(t, group.Members[0].Path, "")
				assertWriterCleanupConflict(t, t.TempDir(), group.Sandbox.Name)
				if outcome == "cancelled" {
					cancel()
				}
			}
			if outcome == "failure" {
				runner.failCreate = group.Sandbox.Name
			}
			result, err := recreateSandboxes(ctx, runner, nil, req, sandboxReconcilePolicy{})
			if !checked || err != nil || result.OK != (outcome == "success") {
				t.Fatal("recreation outcome", checked, result, err)
			}
			assertSandboxActivationReleased(t, group)
		})
	}
}

func TestWorkspaceWriterRefreshRejectsStaleRegistrationWithoutRepair(t *testing.T) {
	for _, change := range []string{"member", "removed"} {
		t.Run(change, func(t *testing.T) {
			root, group := activationFixture(t)
			link := filepath.Join(group.Path, "notes.md")
			if err := os.Remove(link); err != nil {
				t.Fatal(err)
			}
			if err := workspacegroup.Update(root, func(registry *workspacegroup.Registry) error {
				if change == "removed" {
					registry.Workspaces = nil
				} else {
					registry.Workspaces[0].Members = nil
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			before, err := workspacegroup.Load(root)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := RefreshWorkspaceNote(root, group); err == nil || !strings.Contains(err.Error(), "registration changed") {
				t.Error("stale collection repaired a workspace", err)
			}
			if _, err := os.Lstat(link); !os.IsNotExist(err) {
				t.Error("stale collection recreated a managed link", err)
			}
			after, err := workspacegroup.Load(root)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Error("stale collection overwrote registration", err)
			}
		})
	}
}
