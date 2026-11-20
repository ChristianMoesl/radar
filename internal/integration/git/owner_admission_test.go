package git

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/agentservice"
	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

func TestManagedGitCleanupRejectsActualOwnerConflicts(t *testing.T) {
	for _, service := range []bool{false, true} {
		for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired} {
			for _, kind := range []string{"anchor", "alias", "member", "durable-agent"} {
				t.Run(fmt.Sprintf("service=%t/mode=%d/%s", service, mode, kind), func(t *testing.T) {
					f := newCleanupExpiryFixture(t, "main")
					t.Setenv("RADAR_SOCKET", filepath.Join(f.home, "run", "radar.sock"))
					t.Setenv("RADAR_AGENT_ROOT", filepath.Join(f.home, "agents"))
					target := f.preview(t, mode)
					registry, head := f.snapshot(t)
					var release func()
					if kind == "durable-agent" {
						dir := filepath.Join(os.Getenv("RADAR_AGENT_ROOT"), "fixture")
						if err := os.MkdirAll(dir, 0700); err != nil {
							t.Fatal(err)
						}
						data, err := json.Marshal(agentservice.Registration{Version: 1, ID: "fixture", Workspace: protocol.AgentWorkspaceBinding{ID: f.group.ID, Path: f.group.Path, Mode: "local"}})
						if err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(dir, "registration.json"), data, 0600); err != nil {
							t.Fatal(err)
						}
					} else {
						path := f.group.Path
						if kind == "member" {
							path = f.path
						}
						if kind == "alias" {
							path = filepath.Join(f.home, "anchor-alias")
							if err := os.Symlink(f.group.Path, path); err != nil {
								t.Fatal(err)
							}
						}
						var err error
						release, err = workspacelease.Acquire(path, false)
						if err != nil {
							t.Fatal(err)
						}
						defer func() {
							if release != nil {
								release()
							}
						}()
					}
					var err error
					if service {
						_, err = cleanup.New([]integration.CleanupProvider{Source{}}).Execute(f.ctx, protocol.CleanupPreview{Targets: []protocol.CleanupTarget{target}}, cleanup.ExecuteOptions{Mode: mode})
					} else {
						_, err = (Source{}).Cleanup(f.ctx, integration.CleanupRequest{Target: target, Mode: mode})
					}
					if err == nil {
						t.Fatal("managed Git cleanup bypassed actual owner admission")
					}
					want := "in use by another operation"
					if kind == "durable-agent" {
						want = "associated with agent fixture"
					}
					if !strings.Contains(err.Error(), want) {
						t.Fatal("wrong owner refusal", err)
					}
					f.assertPreserved(t, registry, head)
					if release != nil {
						release()
						release = nil
					}
					for _, path := range []string{f.group.Path, f.path} {
						release, err := workspacelease.Acquire(path, true)
						if err != nil {
							t.Fatal("partial admission leaked", err)
						}
						release()
					}
				})
			}
		}
	}
}

type ownerAdmissionProbe struct {
	Source
	run func() error
}

func (ownerAdmissionProbe) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "owner-probe"}
}
func (p ownerAdmissionProbe) Cleanup(_ context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	return req.Target, p.run()
}

func assertGitOwnerPathsReleased(t *testing.T, f *cleanupExpiryFixture) {
	t.Helper()
	for _, path := range []string{f.group.Path, f.path} {
		release, err := workspacelease.Acquire(path, true)
		if err != nil {
			t.Fatal("managed Git admission leaked", err)
		}
		release()
	}
}

func TestManagedGitOwnerAdmissionRetainedThroughLaterProvider(t *testing.T) {
	for _, fullBundle := range []bool{false, true} {
		for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired} {
			for _, outcome := range []string{"success", "provider-error", "cancelled"} {
				t.Run(fmt.Sprintf("full-bundle=%t/mode=%d/%s", fullBundle, mode, outcome), func(t *testing.T) {
					f := newCleanupExpiryFixture(t, "main")
					t.Setenv("RADAR_SOCKET", filepath.Join(f.home, "run", "radar.sock"))
					t.Setenv("RADAR_AGENT_ROOT", filepath.Join(f.home, "agents"))
					target := f.preview(t, mode)
					head := f.branchHead(t)
					ctx, cancel := context.WithCancel(f.ctx)
					defer cancel()
					alias := filepath.Join(f.home, "owner-alias")
					if err := os.Symlink(f.group.Path, alias); err != nil {
						t.Fatal(err)
					}
					later := protocol.CleanupTarget{Source: "owner-probe"}
					if fullBundle {
						later.Path = alias
					}
					failure := errors.New("fixture later provider failure")
					calls := 0
					probe := ownerAdmissionProbe{run: func() error {
						calls++
						for _, path := range []string{f.group.Path, alias, f.path} {
							if release, err := workspacelease.Acquire(path, true); err == nil {
								release()
								t.Fatal("actual owner/member admission dropped before later provider", path)
							}
						}
						if outcome == "provider-error" {
							return failure
						}
						if outcome == "cancelled" {
							cancel()
							return ctx.Err()
						}
						return nil
					}}
					result, err := cleanup.New([]integration.CleanupProvider{Source{}, probe}).Execute(ctx, protocol.CleanupPreview{Targets: []protocol.CleanupTarget{target, later}}, cleanup.ExecuteOptions{Mode: mode})
					if (err != nil) != (outcome != "success") || calls != 1 {
						t.Fatal("unexpected bundle outcome", result, err, calls)
					}
					if outcome == "provider-error" && !errors.Is(err, failure) {
						t.Fatal("provider cause lost", err)
					}
					if outcome == "cancelled" && !errors.Is(err, context.Canceled) {
						t.Fatal("cancellation lost", err)
					}
					wantCompleted := 2
					if outcome != "success" {
						wantCompleted = 1
					}
					if len(result.Targets) != wantCompleted {
						t.Fatal("incorrect partial completion", result)
					}
					f.assertRemoved(t)
					if got := f.branchHead(t); got != head {
						t.Fatal("protected branch changed", got)
					}
					assertGitOwnerPathsReleased(t, f)
				})
			}
		}
	}
}

func TestManagedGitOwnerAdmissionPreservesPendingMemberRules(t *testing.T) {
	for _, held := range []bool{false, true} {
		for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired} {
			t.Run(fmt.Sprintf("held=%t/mode=%d", held, mode), func(t *testing.T) {
				f := newCleanupExpiryFixture(t, "main")
				t.Setenv("RADAR_SOCKET", filepath.Join(f.home, "run", "radar.sock"))
				t.Setenv("RADAR_AGENT_ROOT", filepath.Join(f.home, "agents"))
				f.group.Members = nil
				f.save(t)
				registry, head := f.snapshot(t)
				target := protocol.CleanupTarget{Source: "git", Kind: "worktree", Path: f.path}
				var hold func()
				if held {
					var err error
					hold, err = workspacelease.Acquire(f.group.Path, false)
					if err != nil {
						t.Fatal(err)
					}
					defer func() {
						if hold != nil {
							hold()
						}
					}()
				}
				_, err := (Source{}).Cleanup(f.ctx, integration.CleanupRequest{Target: target, Mode: mode})
				wantError := held || mode == integration.CleanupExpired
				if (err != nil) != wantError {
					t.Fatal("pending-member rules changed", err)
				}
				if wantError {
					f.assertPreserved(t, registry, head)
				} else {
					f.assertRemoved(t)
				}
				if hold != nil {
					hold()
					hold = nil
				}
				assertGitOwnerPathsReleased(t, f)
			})
		}
	}
}

func TestManagedGitOwnerAdmissionPreCancelledPreservesResources(t *testing.T) {
	for _, service := range []bool{false, true} {
		for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired} {
			t.Run(fmt.Sprintf("service=%t/mode=%d", service, mode), func(t *testing.T) {
				f := newCleanupExpiryFixture(t, "main")
				t.Setenv("RADAR_SOCKET", filepath.Join(f.home, "run", "radar.sock"))
				t.Setenv("RADAR_AGENT_ROOT", filepath.Join(f.home, "agents"))
				target := f.preview(t, mode)
				registry, head := f.snapshot(t)
				ctx, cancel := context.WithCancel(f.ctx)
				cancel()
				var err error
				if service {
					_, err = cleanup.New([]integration.CleanupProvider{Source{}}).Execute(ctx, protocol.CleanupPreview{Targets: []protocol.CleanupTarget{target}}, cleanup.ExecuteOptions{Mode: mode})
				} else {
					_, err = (Source{}).Cleanup(ctx, integration.CleanupRequest{Target: target, Mode: mode})
				}
				if !errors.Is(err, context.Canceled) {
					t.Fatal("cancellation not respected", err)
				}
				f.assertPreserved(t, registry, head)
				assertGitOwnerPathsReleased(t, f)
			})
		}
	}
}
