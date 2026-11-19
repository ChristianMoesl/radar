package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"radar/internal/agentservice"
	"radar/internal/cleanup"
	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

func TestDirectAnchorCleanupRejectsActiveCanonicalOwners(t *testing.T) {
	for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired} {
		for _, alias := range []bool{false, true} {
			t.Run(fmt.Sprint(mode, "/alias=", alias), func(t *testing.T) {
				root, owner, task := disposableAnchorFixture(t)
				t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
				t.Setenv("RADAR_AGENT_ROOT", filepath.Join(t.TempDir(), "agents"))
				configureDisposableEntries(t, ".pnpm-store")
				target := previewAnchor(t, task, mode)
				release, err := workspacelease.Acquire(sharedCleanupReference(t, owner.Path, alias), false)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
				_, err = (Source{}).Cleanup(context.Background(), integration.CleanupRequest{Target: target, Mode: mode})
				if err == nil || !strings.Contains(err.Error(), "in use by another operation") {
					t.Fatal("direct Source cleanup bypassed active canonical owner", err)
				}
				assertAnchorFileKept(t, filepath.Join(owner.Path, ".pnpm-store", "v10", "content"))
				assertAnchorRegistered(t, root, owner.ID)
			})
		}
	}
}

func TestDirectAnchorCleanupRetainsStoppedAgentOwnership(t *testing.T) {
	for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			root, owner, task := disposableAnchorFixture(t)
			t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
			agents := filepath.Join(t.TempDir(), "agents")
			t.Setenv("RADAR_AGENT_ROOT", agents)
			configureDisposableEntries(t, ".pnpm-store")
			target := previewAnchor(t, task, mode)
			dir := filepath.Join(agents, "fixture-owner")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(agentservice.Registration{Version: 1, ID: "fixture-owner", Workspace: protocol.AgentWorkspaceBinding{ID: owner.ID, Path: owner.Path, Mode: "local"}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "registration.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			_, err = (Source{}).Cleanup(context.Background(), integration.CleanupRequest{Target: target, Mode: mode})
			if err == nil || !strings.Contains(err.Error(), "associated with agent fixture-owner") {
				t.Fatal("stopped agent ownership was ignored", err)
			}
			assertAnchorFileKept(t, filepath.Join(owner.Path, ".pnpm-store", "v10", "content"))
			assertAnchorRegistered(t, root, owner.ID)
			assertDirectAnchorAdmissionReleased(t, owner.Path)
		})
	}
}

func TestDirectAnchorCleanupHoldsActualOwnerThroughRuntimeLookup(t *testing.T) {
	for _, outcome := range []string{"success", "lookup-error", "cancelled"} {
		for _, alias := range []bool{false, true} {
			t.Run(fmt.Sprint(outcome, "/alias=", alias), func(t *testing.T) {
				root, owner, other, target := cleanupRuntimeFixture(t, false)
				t.Setenv("RADAR_AGENT_ROOT", filepath.Join(t.TempDir(), "agents"))
				target.Path = sharedCleanupReference(t, owner.Path, alias)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				started, resume, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
				finished := make(chan struct{})
				base := &fakeRunner{}
				runner := activationRunner{Runner: base, run: func(ctx context.Context, cwd, name string, args ...string) (string, error) {
					if name == "sbx" && len(args) == 2 && args[0] == "ls" && args[1] == "--json" {
						close(started)
						select {
						case <-resume:
						case <-ctx.Done():
							return "", ctx.Err()
						}
						if outcome == "lookup-error" {
							return "", errors.New("fixture lookup failed")
						}
					}
					return base.Run(ctx, cwd, name, args...)
				}}
				go func() {
					defer close(finished)
					_, err := (Source{}).cleanupWithRunner(ctx, runner, integration.CleanupRequest{Target: target})
					done <- err
				}()
				t.Cleanup(func() { cancel(); <-finished })
				select {
				case <-started:
				case err := <-done:
					t.Fatal("direct cleanup failed before runtime lookup", err)
				case <-time.After(5 * time.Second):
					t.Fatal("direct cleanup did not reach runtime lookup")
				}
				if release, err := workspacelease.Acquire(target.Path, false); err == nil {
					release()
					cancel()
					<-done
					t.Fatal("direct lookup did not retain canonical anchor deletion admission")
				}
				assertAnchorRegistered(t, root, owner.ID)
				releaseOther, err := workspacelease.Acquire(other.Path, true)
				if err != nil {
					cancel()
					<-done
					t.Fatal("unrelated anchor was admitted as the owner", err)
				}
				releaseOther()
				if outcome == "cancelled" {
					cancel()
				} else {
					close(resume)
				}
				err = <-done
				if (err == nil) != (outcome == "success") || outcome == "cancelled" && !errors.Is(err, context.Canceled) {
					t.Fatal("unexpected direct cleanup outcome", outcome, err)
				}
				if outcome != "success" {
					assertAnchorFileKept(t, filepath.Join(owner.Path, ".pnpm-store", "content"))
					assertAnchorRegistered(t, root, owner.ID)
				} else if _, err := os.Stat(owner.Path); !os.IsNotExist(err) {
					t.Fatal("successful cleanup left owner anchor", err)
				}
				assertAnchorRegistered(t, root, other.ID)
				assertDirectAnchorAdmissionReleased(t, owner.Path)
			})
		}
	}
}

func TestDirectAnchorCleanupReleasesAnchorOnSharedAdmissionFailure(t *testing.T) {
	root, owner, _, target := cleanupRuntimeFixture(t, false)
	t.Setenv("RADAR_AGENT_ROOT", filepath.Join(t.TempDir(), "agents"))
	owner.Sandbox.SharedDirectory = filepath.Join(t.TempDir(), workspacegroup.SharedDirectoryParent, owner.ID)
	if err := ensureSharedDirectory(owner); err != nil {
		t.Fatal(err)
	}
	updateSharedCleanupGroup(t, root, owner)
	release, err := workspacelease.Acquire(owner.Sandbox.SharedDirectory, false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	base := &fakeRunner{}
	_, err = (Source{}).cleanupWithRunner(context.Background(), base, integration.CleanupRequest{Target: target})
	if err == nil || !strings.Contains(err.Error(), "in use by another operation") || len(base.calls) != 0 {
		t.Fatal("shared admission failure reached provider I/O", err, base.calls)
	}
	assertDirectAnchorAdmissionReleased(t, owner.Path)
	assertAnchorFileKept(t, filepath.Join(owner.Path, ".pnpm-store", "content"))
	assertAnchorRegistered(t, root, owner.ID)
}

func TestDirectAnchorCleanupUsesRegisteredOwnerNotPreviewPath(t *testing.T) {
	root, owner, task := disposableAnchorFixture(t)
	t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
	t.Setenv("RADAR_AGENT_ROOT", filepath.Join(t.TempDir(), "agents"))
	configureDisposableEntries(t, ".pnpm-store")
	target := previewDisposableAnchor(t, task)
	target.Path = t.TempDir()
	release, err := workspacelease.Acquire(owner.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_, err = (Source{}).Cleanup(context.Background(), integration.CleanupRequest{Target: target})
	if err == nil || !strings.Contains(err.Error(), "in use by another operation") {
		t.Fatal("preview borrowed deletion authority for another path", err)
	}
	assertAnchorFileKept(t, filepath.Join(owner.Path, ".pnpm-store", "v10", "content"))
	assertAnchorRegistered(t, root, owner.ID)
	assertDirectAnchorAdmissionReleased(t, target.Path)
}

func TestDirectAndBoundAnchorCleanupAllowUnrelatedOwners(t *testing.T) {
	for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired} {
		for _, bound := range []bool{false, true} {
			t.Run(fmt.Sprint(mode, "/bound=", bound), func(t *testing.T) {
				_, owner, task := disposableAnchorFixture(t)
				t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
				t.Setenv("RADAR_AGENT_ROOT", filepath.Join(t.TempDir(), "agents"))
				configureDisposableEntries(t, ".pnpm-store")
				target := previewAnchor(t, task, mode)
				release, err := workspacelease.Acquire(t.TempDir(), false)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
				if bound {
					_, err = cleanup.New([]integration.CleanupProvider{Source{}}).Execute(context.Background(), protocol.CleanupPreview{Targets: []protocol.CleanupTarget{target}}, cleanup.ExecuteOptions{Mode: mode})
				} else {
					_, err = (Source{}).Cleanup(context.Background(), integration.CleanupRequest{Target: target, Mode: mode})
				}
				if err != nil {
					t.Fatal("unrelated owner or already-held Service admission blocked cleanup", err)
				}
				if _, err := os.Stat(owner.Path); !os.IsNotExist(err) {
					t.Fatal("successful cleanup left owner anchor", err)
				}
				assertDirectAnchorAdmissionReleased(t, owner.Path)
			})
		}
	}
}

func assertDirectAnchorAdmissionReleased(t *testing.T, path string) {
	t.Helper()
	release, err := workspacelease.Acquire(path, true)
	if err != nil {
		t.Fatal("direct cleanup leaked anchor admission", err)
	}
	release()
}
