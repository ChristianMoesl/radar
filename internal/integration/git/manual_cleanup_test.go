package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/integration/workspace"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func failManualStatus(t *testing.T) {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\ncase \"$*\" in *'status --porcelain') echo 'fixture index inspection failed' >&2; exit 1;; esac\nexec %q \"$@\"\n", real)
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestManualCleanupCanConfirmUnavailableStatusWithoutChangingGC(t *testing.T) {
	for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupExpired, integration.CleanupConfirmed} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			f := newCleanupExpiryFixture(t, "feature")
			f.dirty(t)
			task := protocol.Task{ID: 7, Title: "Cleanup fixture", SourceRefs: []protocol.SourceRef{f.ref(), {ID: "workspace:" + f.group.ID, Source: "workspace", Kind: "workspace", Path: f.group.Path, WorkspaceID: f.group.ID, ProvidesWorkspace: true}}}
			service := cleanup.New([]integration.CleanupProvider{Source{}, workspace.NewSource(nil)})
			registryBefore, headBefore := f.snapshot(t)
			failManualStatus(t)
			preview, err := service.Preview(f.ctx, task, mode)
			if mode != integration.CleanupConfirmed {
				if err == nil {
					t.Fatal("automatic cleanup ignored failed inspection")
				}
				f.assertPreserved(t, registryBefore, headBefore)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(preview.Targets) != 2 || !hasCleanupSafety(preview.Targets[0], "safety_check_unavailable") || !strings.Contains(preview.Targets[0].Safety[0].Message, "fixture index inspection failed") {
				t.Fatalf("failed check not disclosed: %+v", preview)
			}
			// Preview alone is read-only. Confirmation executes actual Git removal,
			// deletes only the owned branch/anchor, and keeps the primary repository.
			if _, err := os.Stat(f.path); err != nil {
				t.Fatal(err)
			}
			result, err := service.Execute(f.ctx, preview, cleanup.ExecuteOptions{Mode: mode})
			if err != nil || len(result.Targets) != 2 {
				t.Fatalf("cleanup failed: %+v %v", result, err)
			}
			if _, err := os.Stat(f.group.Path); !os.IsNotExist(err) {
				t.Fatalf("anchor remains: %v", err)
			}
			if _, err := os.Stat(f.repo); err != nil {
				t.Fatal("primary repository removed", err)
			}
			registry, err := workspacegroup.Load(f.root)
			if err != nil || len(registry.Workspaces) != 0 {
				t.Fatalf("registration remains: %+v %v", registry, err)
			}
			command := exec.CommandContext(context.Background(), "git", "show-ref", "--verify", "--quiet", "refs/heads/feature")
			command.Dir = f.repo
			if command.Run() == nil {
				t.Fatal("owned branch remains")
			}
		})
	}
}

type failAnchorOnce struct {
	workspace.Source
	fail bool
}

func (s *failAnchorOnce) Cleanup(ctx context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	if s.fail {
		s.fail = false
		return protocol.CleanupTarget{}, errors.New("fixture anchor removal unavailable")
	}
	return s.Source.Cleanup(ctx, req)
}

func TestManualPartialCleanupRetainsRegistrationAndRetriesRemainingResources(t *testing.T) {
	f := newCleanupExpiryFixture(t, "feature")
	ref := protocol.SourceRef{ID: "workspace:" + f.group.ID, Source: "workspace", Kind: "workspace", Path: f.group.Path, WorkspaceID: f.group.ID, ProvidesWorkspace: true}
	task := protocol.Task{ID: 7, SourceRefs: []protocol.SourceRef{f.ref(), ref}}
	anchor := &failAnchorOnce{Source: workspace.NewSource(nil), fail: true}
	service := cleanup.New([]integration.CleanupProvider{Source{}, anchor})
	preview, err := service.Preview(f.ctx, task, integration.CleanupConfirmed)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Execute(f.ctx, preview, cleanup.ExecuteOptions{Mode: integration.CleanupConfirmed})
	if err == nil || len(result.Targets) != 1 || !strings.Contains(err.Error(), f.group.Path) || !strings.Contains(err.Error(), "remaining resources") {
		t.Fatalf("partial cleanup falsely succeeded or lost retry evidence: %+v %v", result, err)
	}
	if _, err := os.Stat(f.path); !os.IsNotExist(err) {
		t.Fatalf("worktree was not actually deleted: %v", err)
	}
	registry, err := workspacegroup.Load(f.root)
	if err != nil || len(registry.Workspaces) != 1 || len(registry.Workspaces[0].Members) != 0 {
		t.Fatalf("failed cleanup discarded registration or retained deleted member: %+v %v", registry, err)
	}
	// Model the refreshed observation, not replay of a stale confirmed plan.
	task.SourceRefs = []protocol.SourceRef{ref}
	retry, err := service.Preview(f.ctx, task, integration.CleanupConfirmed)
	if err != nil || len(retry.Targets) != 1 {
		t.Fatalf("remaining resources cannot be retried: %+v %v", retry, err)
	}
	result, err = service.Execute(f.ctx, retry, cleanup.ExecuteOptions{Mode: integration.CleanupConfirmed})
	if err != nil || len(result.Targets) != 1 {
		t.Fatalf("retry failed: %+v %v", result, err)
	}
	if _, err := os.Stat(f.group.Path); !os.IsNotExist(err) {
		t.Fatalf("retry only unregistered resources: %v", err)
	}
	registry, err = workspacegroup.Load(f.root)
	if err != nil || len(registry.Workspaces) != 0 {
		t.Fatalf("retry retained registry: %+v %v", registry, err)
	}
}

func TestManualUnavailableStatusStillPreservesProtectedAndSharedBranches(t *testing.T) {
	for _, branch := range []string{"main", "shared"} {
		t.Run(branch, func(t *testing.T) {
			f := newCleanupExpiryFixture(t, branch)
			if branch == "shared" {
				runGit(t, f.ctx, f.repo, "worktree", "add", "--force", filepath.Join(f.home, "other-checkout"), branch)
			}
			head := f.branchHead(t)
			failManualStatus(t)
			target := f.preview(t, integration.CleanupConfirmed)
			if target.Operation["delete_branch"] != "" || !hasCleanupSafety(target, "safety_check_unavailable") {
				t.Fatalf("unsafe preview: %+v", target)
			}
			if _, err := (Source{}).Cleanup(f.ctx, integration.CleanupRequest{Target: target, Mode: integration.CleanupConfirmed}); err != nil {
				t.Fatal(err)
			}
			f.assertRemoved(t)
			if got := f.branchHead(t); got != head {
				t.Fatalf("protected/shared branch changed: %s", got)
			}
		})
	}
}

func TestManualUnavailableStatusDoesNotBypassLockedWorktree(t *testing.T) {
	f := newCleanupExpiryFixture(t, "feature")
	target := f.preview(t, integration.CleanupConfirmed)
	runGit(t, f.ctx, f.repo, "worktree", "lock", "--reason", "keep this worktree", f.path)
	registry, head := f.snapshot(t)
	failManualStatus(t)
	if _, err := (Source{}).PreviewCleanup(f.ctx, integration.CleanupPreviewRequest{Task: protocol.Task{SourceRefs: []protocol.SourceRef{f.ref()}}, Mode: integration.CleanupConfirmed}); err == nil {
		t.Fatal("manual preview ignored structural lock")
	}
	if _, err := (Source{}).Cleanup(f.ctx, integration.CleanupRequest{Target: target, Mode: integration.CleanupConfirmed}); err == nil {
		t.Fatal("manual removal ignored structural lock")
	}
	f.assertPreserved(t, registry, head)
}
