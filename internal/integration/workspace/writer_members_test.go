package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/workspacelease"
)

func TestWorkspaceWriterReconcileProtectsAddedAndRemovedMembers(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	ctx := context.Background()
	root, repo := t.TempDir(), filepath.Join(t.TempDir(), "source")
	t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
	initRepository(t, ctx, repo)
	runGitE2E(t, ctx, repo, "branch", "fixture")
	anchor := filepath.Join(root, "writer")
	if err := os.Mkdir(anchor, 0755); err != nil {
		t.Fatal(err)
	}
	group := workspacegroup.Workspace{ID: workspacegroup.ID(anchor), Name: "Writer", Path: anchor, Members: []workspacegroup.Member{}}
	if err := registerWorkspace(root, group); err != nil {
		t.Fatal(err)
	}
	_, group, _, _ = RegisteredWorkspace(anchor, root)
	addedPath := filepath.Join(anchor, WorktreeDirectoryName(repo, "fixture"))
	for _, phase := range []string{"add", "remove"} {
		t.Run(phase, func(t *testing.T) {
			request := writerRequest(t, root, group)
			if phase == "add" {
				request.Desired.Worktrees = []DesiredWorkspaceWorktree{{Repository: repo, BranchMode: integration.WorkspaceBranchExisting, Branch: "fixture"}}
			} else {
				request.Desired.Worktrees = nil
			}
			// The destination is absent for addition and omitted from the desired
			// bundle for removal. Both must still contend before provider effects.
			release, err := workspacelease.AcquireMutation(addedPath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ApplyReconcileWorkspace(ctx, ExecRunner{}, nil, request); err == nil || !strings.Contains(err.Error(), "mutation is in use") {
				t.Error("reconciliation omitted changed member admission", err)
			}
			release()
			checked := false
			runner := activationRunner{Runner: ExecRunner{}, run: func(ctx context.Context, cwd, name string, args ...string) (string, error) {
				if name == "git" && strings.Contains(strings.Join(args, " "), "worktree "+phase) {
					checked = true
					assertWriterCleanupConflict(t, addedPath, "")
					assertWriterCleanupConflict(t, anchor, "")
					if release, err := workspacelease.AcquireMutation(addedPath); err == nil {
						release()
						t.Error("member mutation ownership was dropped before provider effect")
					}
				}
				return (ExecRunner{}).Run(ctx, cwd, name, args...)
			}}
			result, err := ApplyReconcileWorkspace(ctx, runner, nil, request)
			if err != nil || !result.OK || !checked {
				t.Fatal("member reconciliation", checked, result, err)
			}
			_, group, _, err = RegisteredWorkspace(anchor, root)
			if err != nil || (len(group.Members) == 1) != (phase == "add") {
				t.Fatal("member registration", group, err)
			}
			for _, path := range []string{anchor, addedPath} {
				for _, acquire := range []func() (func(), error){
					func() (func(), error) { return workspacelease.Acquire(path, true) },
					func() (func(), error) { return workspacelease.AcquireMutation(path) },
				} {
					release, err := acquire()
					if err != nil {
						t.Fatal("member admission leaked", err)
					}
					release()
				}
			}
		})
	}
}
