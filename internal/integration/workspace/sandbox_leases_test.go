package workspace

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

func sandboxActivationFixture(t *testing.T) (string, workspacegroup.Workspace) {
	t.Helper()
	withWorkspaceGOOS(t, "darwin")
	root, group := activationFixture(t)
	group.Sandbox = &workspacegroup.Sandbox{Name: "shared-runtime", Agent: "shell", Mounts: []string{group.Path}}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	return root, group
}
func assertSandboxActivationReleased(t *testing.T, group workspacegroup.Workspace) {
	t.Helper()
	assertActivationLeasesReleased(t, group)
	release, err := workspacelease.AcquireSandbox(group.Sandbox.Name, true)
	if err != nil {
		t.Fatal("sandbox activation lease leaked", err)
	}
	release()
}
func sandboxActivate(ctx context.Context, runner Runner, group workspacegroup.Workspace, registered bool) (Workspace, error) {
	if registered {
		return OpenRegisteredWorkspace(ctx, runner, group.Path, true)
	}
	return CreateSessionWithOptions(ctx, runner, CreateSessionOptions{Path: group.Path, SessionName: group.SessionName, SandboxName: group.Sandbox.Name, Switch: true})
}

func TestSandboxActivationRejectsNamedCleanupAcrossDifferentPaths(t *testing.T) {
	for _, registered := range []bool{false, true} {
		for _, live := range []bool{false, true} {
			t.Run(map[bool]string{false: "standalone", true: "registered"}[registered]+"/"+map[bool]string{false: "recovery", true: "live"}[live], func(t *testing.T) {
				_, group := sandboxActivationFixture(t)
				release, err := workspacelease.AcquireSandbox(group.Sandbox.Name, true)
				if err != nil {
					t.Fatal(err)
				}
				runner := &fakeRunner{hasSession: live}
				_, err = sandboxActivate(context.Background(), runner, group, registered)
				release()
				if err == nil || !strings.Contains(err.Error(), "sandbox is in use by another operation") || len(runner.calls) != 0 {
					t.Fatalf("activation bypassed named cleanup: %v %+v", err, runner.calls)
				}
				assertSandboxActivationReleased(t, group)
			})
		}
	}
}

func TestSandboxActivationHoldsNameThroughSwitchAndReleasesAllOutcomes(t *testing.T) {
	for _, registered := range []bool{false, true} {
		for _, live := range []bool{false, true} {
			for _, outcome := range []string{"success", "failure", "cancelled"} {
				t.Run(map[bool]string{false: "standalone", true: "registered"}[registered]+"/"+map[bool]string{false: "recovery", true: "live"}[live]+"/"+outcome, func(t *testing.T) {
					_, group := sandboxActivationFixture(t)
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					base := &fakeRunner{hasSession: live}
					checked := false
					runner := activationRunner{Runner: base, run: func(ctx context.Context, cwd, name string, args ...string) (string, error) {
						if name == "tmux" && args[0] == "switch-client" {
							checked = true
							if release, err := workspacelease.AcquireSandbox(group.Sandbox.Name, true); err == nil {
								release()
								t.Fatal("cleanup entered active shared sandbox")
							}
							release, err := workspacelease.AcquireSandbox("unrelated-runtime", true)
							if err != nil {
								t.Fatal(err)
							}
							release()
							if outcome == "failure" {
								return "", errors.New("fixture switch failed")
							}
							if outcome == "cancelled" {
								cancel()
								return "", ctx.Err()
							}
						}
						return base.Run(ctx, cwd, name, args...)
					}}
					_, err := sandboxActivate(ctx, runner, group, registered)
					if !checked || (err != nil) != (outcome != "success") {
						t.Fatalf("switch checked=%t err=%v", checked, err)
					}
					if outcome == "cancelled" && !errors.Is(err, context.Canceled) {
						t.Fatal(err)
					}
					assertSandboxActivationReleased(t, group)
				})
			}
		}
	}
}

type namedSandboxCleanup struct{ activationCleanup }

func (namedSandboxCleanup) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "sbx"}
}
func TestSandboxActivationPausedCleanupBlocksNameNotUnrelatedSandbox(t *testing.T) {
	for _, registered := range []bool{false, true} {
		for _, live := range []bool{false, true} {
			t.Run(map[bool]string{false: "standalone", true: "registered"}[registered]+"/"+map[bool]string{false: "recovery", true: "live"}[live], func(t *testing.T) {
				root, group := sandboxActivationFixture(t)
				started, resume := make(chan struct{}), make(chan struct{})
				finished, done := make(chan error, 1), make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(resume) }) }
				service := cleanup.New([]integration.CleanupProvider{namedSandboxCleanup{activationCleanup{started: started, resume: resume}}})
				preview := protocol.CleanupPreview{Targets: []protocol.CleanupTarget{{Source: "sbx", Kind: "sandbox", ResourceID: group.Sandbox.Name, Path: t.TempDir()}}}
				go func() {
					_, err := service.Execute(context.Background(), preview, cleanup.ExecuteOptions{Mode: integration.CleanupConfirmed})
					finished <- err
					close(done)
				}()
				defer func() { unblock(); <-done }()
				select {
				case <-started:
				case err := <-finished:
					t.Fatal("cleanup did not enter provider", err)
				case <-time.After(5 * time.Second):
					t.Fatal("cleanup did not start")
				}
				runner := &fakeRunner{hasSession: live}
				if _, err := sandboxActivate(context.Background(), runner, group, registered); err == nil || len(runner.calls) != 0 {
					t.Fatalf("different-path cleanup bypassed: %v %+v", err, runner.calls)
				}
				group.Sandbox.Name = "unrelated-runtime"
				if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
					t.Fatal(err)
				}
				if _, err := sandboxActivate(context.Background(), runner, group, registered); err != nil {
					t.Fatal("unrelated sandbox was blocked", err)
				}
				unblock()
				if err := <-finished; err != nil {
					t.Fatal(err)
				}
				assertSandboxActivationReleased(t, group)
			})
		}
	}
}

func TestSandboxActivationNameContentionRollsBackMemberLeases(t *testing.T) {
	_, group := sandboxActivationFixture(t)
	release, err := workspacelease.AcquireSandbox(group.Sandbox.Name, true)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := OpenRegisteredWorkspace(context.Background(), &fakeRunner{}, filepath.Join(group.Path, "nested"), true); err == nil {
		t.Fatal("shared sandbox conflict ignored")
	}
	assertActivationLeasesReleased(t, group)
}
