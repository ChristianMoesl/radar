package git

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"radar/internal/agentservice"
	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/integration/workspace"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

// Only the private traversal boundary is paused. Actual Git validation, owner
// admission, rooted traversal, final transaction and removal remain production.
func pausedMemberCleanup(t *testing.T, f *cleanupExpiryFixture, target protocol.CleanupTarget, mode integration.CleanupMode, service bool, later ...integration.CleanupProvider) (func(), <-chan error, <-chan struct{}) {
	t.Helper()
	entered, resume, done, returned := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(resume) }) }
	source := memberScanSource{scan: func(scan func() error) error {
		if err := scan(); err != nil {
			return err
		}
		close(entered)
		<-resume
		close(returned)
		return nil
	}}
	result := make(chan error, 1)
	go func() {
		var err error
		if service {
			providers := append([]integration.CleanupProvider{source}, later...)
			targets := []protocol.CleanupTarget{target}
			for _, provider := range later {
				targets = append(targets, protocol.CleanupTarget{Source: provider.Descriptor().Name})
			}
			_, err = cleanup.New(providers).Execute(f.ctx, protocol.CleanupPreview{Targets: targets}, cleanup.ExecuteOptions{Mode: mode})
		} else {
			_, err = source.Cleanup(f.ctx, integration.CleanupRequest{Target: target, Mode: mode})
		}
		result <- err
		close(done)
	}()
	t.Cleanup(func() { unblock(); <-done })
	select {
	case <-entered:
	case err := <-result:
		t.Fatal("actual member scan not reached", err)
	case <-time.After(5 * time.Second):
		t.Fatal("actual member scan not reached")
	}
	return unblock, result, returned
}

func recordMemberCleanupGit(t *testing.T) string {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> %q\nexec %q \"$@\"\n", calls, git)
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

func assertNoMemberDeletion(t *testing.T, calls string) {
	t.Helper()
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "worktree remove") || strings.Contains(string(data), "update-ref -d") {
		t.Fatal("refused cleanup attempted deletion", string(data))
	}
}

func TestGitMemberScanRevalidatesBeforeAnyDeletion(t *testing.T) {
	for _, mode := range []integration.CleanupMode{integration.CleanupConfirmed, integration.CleanupExpired} {
		for _, service := range []bool{false, true} {
			for _, kind := range []string{"success", "unrelated-update", "owner", "member", "canonical-note", "sandbox-config", "removed-member", "removed-owner", "configured-root", "root-replaced", "anchor-replaced", "member-replaced", "member-symlink", "locked", "actual-branch-changed", "git-file-changed", "agent-appeared", "cancelled", "canonical-reference", "configured-reference", "registered-reference"} {
				t.Run(fmt.Sprintf("%d/service=%t/%s", mode, service, kind), func(t *testing.T) {
					f := newCleanupExpiryFixture(t, "feature")
					t.Setenv("RADAR_SOCKET", filepath.Join(f.home, "run", "radar.sock"))
					t.Setenv("RADAR_AGENT_ROOT", filepath.Join(f.home, "agents"))
					target := f.preview(t, mode)
					ctx, cancel := context.WithCancel(f.ctx)
					defer cancel()
					f.ctx = ctx
					calls := recordMemberCleanupGit(t)
					unblock, result, _ := pausedMemberCleanup(t, f, target, mode, service)
					changeRegistry := func(change func(*workspacegroup.Registry)) {
						t.Helper()
						if err := workspacegroup.WithNoteLock(f.root, func() error {
							return workspacegroup.Update(f.root, func(r *workspacegroup.Registry) error { change(r); return nil })
						}); err != nil {
							t.Fatal(err)
						}
					}
					switch kind {
					case "owner":
						changeRegistry(func(r *workspacegroup.Registry) { r.Workspaces[0].Name = "changed" })
					case "member":
						changeRegistry(func(r *workspacegroup.Registry) { r.Workspaces[0].Members[0].SetupScheduled = true })
					case "canonical-note":
						changeRegistry(func(r *workspacegroup.Registry) { r.Workspaces[0].NotePath = filepath.Join(f.home, "note.md") })
					case "sandbox-config":
						changeRegistry(func(r *workspacegroup.Registry) {
							r.Workspaces[0].Sandbox = &workspacegroup.Sandbox{Name: "appeared", Agent: "shell", Mounts: []string{f.group.Path}}
						})
					case "removed-member":
						changeRegistry(func(r *workspacegroup.Registry) { r.Workspaces[0].Members = nil })
					case "removed-owner":
						changeRegistry(func(r *workspacegroup.Registry) { r.Workspaces = nil })
					case "unrelated-update", "canonical-reference", "registered-reference":
						changeRegistry(func(r *workspacegroup.Registry) {
							path := filepath.Join(f.root, "other")
							g := workspacegroup.Workspace{ID: workspacegroup.ID(path), Name: "other", Path: path}
							if kind == "canonical-reference" {
								g.NotePath = filepath.Join(f.path, "README.md")
							}
							if kind == "registered-reference" {
								g.Sandbox = &workspacegroup.Sandbox{Name: "other", Agent: "shell", Mounts: []string{filepath.Join(f.path, "shared")}}
							}
							r.Workspaces = append(r.Workspaces, g)
						})
					case "configured-root", "configured-reference":
						config := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "radar", "config.yaml")
						data := fmt.Appendf(nil, "workspace: {root_dir: %q}\n", filepath.Join(f.home, "other-root"))
						if kind == "configured-reference" {
							data = fmt.Appendf(nil, "workspace: {root_dir: %q}\nsbx: {additional_mounts: [%q]}\n", f.root, filepath.Join(f.path, "shared"))
						}
						if err := os.WriteFile(config, data, 0600); err != nil {
							t.Fatal(err)
						}
					case "root-replaced", "anchor-replaced", "member-replaced", "member-symlink":
						path := f.root
						if kind == "anchor-replaced" {
							path = f.group.Path
						}
						if kind == "member-replaced" || kind == "member-symlink" {
							path = f.path
						}
						moved := path + "-original"
						if err := os.Rename(path, moved); err != nil {
							t.Fatal(err)
						}
						if kind == "member-symlink" {
							if err := os.Symlink(moved, path); err != nil {
								t.Fatal(err)
							}
						} else {
							if err := os.MkdirAll(f.path, 0755); err != nil {
								t.Fatal(err)
							}
						}
						if kind == "root-replaced" {
							f.save(t)
						}
					case "locked":
						runGit(t, f.ctx, f.repo, "worktree", "lock", f.path)
					case "actual-branch-changed":
						runGit(t, f.ctx, f.path, "switch", "-c", "changed")
					case "git-file-changed":
						if err := os.WriteFile(filepath.Join(f.path, ".git"), []byte("gitdir: /missing\n"), 0600); err != nil {
							t.Fatal(err)
						}
					case "agent-appeared":
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
					case "cancelled":
						cancel()
					}
					registry, err := os.ReadFile(workspacegroup.Path(f.root))
					if err != nil {
						t.Fatal(err)
					}
					unblock()
					err = <-result
					success := kind == "success" || kind == "unrelated-update"
					if (err == nil) != success {
						t.Fatal("unexpected cleanup outcome", kind, err)
					}
					if success {
						f.assertRemoved(t)
					} else {
						if _, err := os.Lstat(f.path); err != nil {
							t.Fatal("refused cleanup removed member", err)
						}
						after, err := os.ReadFile(workspacegroup.Path(f.root))
						if err != nil || string(after) != string(registry) {
							t.Fatal("refused cleanup changed registration", err)
						}
						assertNoMemberDeletion(t, calls)
					}
					assertGitOwnerPathsReleased(t, f)
				})
			}
		}
	}
}

func TestGitMemberScanRetainsOwnerAndReferenceAdmission(t *testing.T) {
	for _, service := range []bool{false, true} {
		for _, entry := range []string{"open", "create-existing"} {
			for _, alias := range []bool{false, true} {
				t.Run(fmt.Sprintf("service=%t/%s/alias=%t", service, entry, alias), func(t *testing.T) {
					f := newCleanupExpiryFixture(t, "feature")
					f.group.TaskLinkingKey = "obsidian:task:cleanup"
					f.group.SessionName = "cleanup-session"
					f.group.NotePath = filepath.Join(f.home, "owner.md")
					if err := os.WriteFile(f.group.NotePath, []byte("canonical owner\n"), 0600); err != nil {
						t.Fatal(err)
					}
					f.save(t)
					target := f.preview(t, integration.CleanupConfirmed)
					unblock, result, _ := pausedMemberCleanup(t, f, target, integration.CleanupConfirmed, service)
					selected := f.group
					path := f.group.Path
					if alias {
						path = filepath.Join(f.root, "alias")
						if err := os.Symlink(f.group.Path, path); err != nil {
							t.Fatal(err)
						}
						selected = workspacegroup.Workspace{ID: workspacegroup.ID(path), Name: "alias", Path: path, SessionName: "alias-session", TaskLinkingKey: "obsidian:task:alias", NotePath: filepath.Join(f.home, "alias.md")}
						// Raw registry insertion deliberately bypasses public writer
						// admission to test canonical conflict and fresh protection.
						if err := workspacegroup.Update(f.root, func(r *workspacegroup.Registry) error { r.Workspaces = append(r.Workspaces, selected); return nil }); err != nil {
							t.Fatal(err)
						}
					}
					runner := &memberRecoveryRunner{}
					var err error
					if entry == "open" {
						_, err = workspace.OpenRegisteredWorkspace(f.ctx, runner, path, true)
					} else {
						_, err = workspace.Create(f.ctx, runner, workspace.CreateOptions{WorkspaceRoot: f.root, TaskLinkingKey: selected.NoteKey(), Switch: true})
					}
					if err == nil || !strings.Contains(err.Error(), "in use by another operation") || len(runner.calls) != 0 {
						t.Fatal("conflicting recovery was not refused before provider effects", err, runner.calls)
					}
					for _, path := range []string{f.group.Path, f.path} {
						if release, err := workspacelease.Acquire(path, false); err == nil {
							release()
							t.Fatal("owner/member admission dropped")
						}
					}
					if release, err := workspacelease.AcquireReferences(f.ctx, filepath.Join(f.path, "canonical.md")); err == nil {
						release()
						t.Fatal("protected reference admission dropped")
					}
					registry, head := f.snapshot(t)
					unblock()
					err = <-result
					if alias {
						if err == nil {
							t.Fatal("appeared alias reference did not block deletion")
						}
						f.assertPreserved(t, registry, head)
					} else if err != nil {
						t.Fatal(err)
					}
					assertGitOwnerPathsReleased(t, f)
				})
			}
		}
	}
}

func TestGitMemberScanRechecksAfterWaitingForNoteTransaction(t *testing.T) {
	for _, service := range []bool{false, true} {
		t.Run(fmt.Sprintf("service=%t", service), func(t *testing.T) {
			f := newCleanupExpiryFixture(t, "feature")
			target := f.preview(t, integration.CleanupConfirmed)
			calls := recordMemberCleanupGit(t)
			unblock, result, returned := pausedMemberCleanup(t, f, target, integration.CleanupConfirmed, service)
			if err := workspacegroup.WithNoteLock(f.root, func() error {
				unblock()
				select {
				case <-returned:
				case <-time.After(5 * time.Second):
					return errors.New("scan did not finish outside held note transaction")
				}
				return workspacegroup.Update(f.root, func(r *workspacegroup.Registry) error { r.Workspaces[0].Name = "changed while waiting"; return nil })
			}); err != nil {
				t.Fatal(err)
			}
			registry, head := f.snapshot(t)
			if err := <-result; err == nil {
				t.Fatal("stale scan survived final transaction wait")
			}
			f.assertPreserved(t, registry, head)
			assertNoMemberDeletion(t, calls)
			assertGitOwnerPathsReleased(t, f)
		})
	}
}

func TestGitMemberScanErrorAndCancellationReleaseAdmission(t *testing.T) {
	for _, service := range []bool{false, true} {
		for _, kind := range []string{"nested-checkout", "scan-error", "cancelled"} {
			t.Run(fmt.Sprintf("service=%t/%s", service, kind), func(t *testing.T) {
				f := newCleanupExpiryFixture(t, "feature")
				target := f.preview(t, integration.CleanupConfirmed)
				ctx, cancel := context.WithCancel(f.ctx)
				defer cancel()
				f.ctx = ctx
				calls := recordMemberCleanupGit(t)
				stop := errors.New("fixture scan failure")
				source := memberScanSource{scan: func(scan func() error) error {
					if kind == "nested-checkout" {
						initCleanupExpiryRepository(t, f.ctx, filepath.Join(f.path, "nested"), "main")
					}
					if kind == "cancelled" {
						cancel()
					}
					if err := scan(); err != nil {
						return err
					}
					return stop
				}}
				var err error
				if service {
					_, err = cleanup.New([]integration.CleanupProvider{source}).Execute(f.ctx, protocol.CleanupPreview{Targets: []protocol.CleanupTarget{target}}, cleanup.ExecuteOptions{Mode: integration.CleanupConfirmed})
				} else {
					_, err = source.Cleanup(f.ctx, integration.CleanupRequest{Target: target, Mode: integration.CleanupConfirmed})
				}
				if err == nil {
					t.Fatal("scan failure accepted")
				}
				if kind == "nested-checkout" && !strings.Contains(err.Error(), "Git checkout") {
					t.Fatal("nested checkout not protected", err)
				}
				if kind == "scan-error" && !errors.Is(err, stop) {
					t.Fatal("scan error not preserved", err)
				}
				if kind == "cancelled" && !errors.Is(err, context.Canceled) {
					t.Fatal("cancellation not preserved", err)
				}
				assertNoMemberDeletion(t, calls)
				assertGitOwnerPathsReleased(t, f)
			})
		}
	}
}

func TestGitMemberScanDiscoveredOwnerRetainedThroughLaterProviders(t *testing.T) {
	for _, outcome := range []string{"success", "provider-error", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			f := newCleanupExpiryFixture(t, "feature")
			target := f.preview(t, integration.CleanupConfirmed)
			ctx, cancel := context.WithCancel(f.ctx)
			defer cancel()
			f.ctx = ctx
			calls := 0
			probe := ownerAdmissionProbe{run: func() error {
				calls++
				for _, path := range []string{f.group.Path, f.path} {
					if release, err := workspacelease.Acquire(path, false); err == nil {
						release()
						t.Error("discovered owner released before later provider")
					}
				}
				if outcome == "provider-error" {
					return errors.New("fixture later provider failure")
				}
				if outcome == "cancelled" {
					cancel()
					return ctx.Err()
				}
				return nil
			}}
			unblock, result, _ := pausedMemberCleanup(t, f, target, integration.CleanupConfirmed, true, probe)
			unblock()
			err := <-result
			if calls != 1 || (err == nil) != (outcome == "success") {
				t.Fatal("unexpected later provider outcome", calls, err)
			}
			f.ctx = context.Background()
			f.assertRemoved(t)
			assertGitOwnerPathsReleased(t, f)
		})
	}
}

// A creator may publish a member after Cleanup's initial unlocked observation.
// Once captured for scanning, a later removal of that registration must not
// reopen the standalone/partial-addition destructive fallback.
func TestGitMemberScanNeverFallsBackAfterPendingRegistrationDisappears(t *testing.T) {
	for _, service := range []bool{false, true} {
		t.Run(fmt.Sprintf("service=%t", service), func(t *testing.T) {
			f := newCleanupExpiryFixture(t, "feature")
			member := f.group.Members[0]
			f.group.Members = nil
			f.save(t)
			unlock := holdWorktreeCleanupNoteLock(t, f.root)
			calls := recordMemberCleanupGit(t)
			entered, resume, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(resume) }) }
			source := memberScanSource{scan: func(scan func() error) error {
				if err := scan(); err != nil {
					return err
				}
				close(entered)
				<-resume
				return nil
			}}
			target := protocol.CleanupTarget{Source: "git", Kind: "worktree", Path: f.path}
			result := make(chan error, 1)
			go func() {
				var err error
				if service {
					_, err = cleanup.New([]integration.CleanupProvider{source}).Execute(f.ctx, protocol.CleanupPreview{Targets: []protocol.CleanupTarget{target}}, cleanup.ExecuteOptions{Mode: integration.CleanupConfirmed})
				} else {
					_, err = source.Cleanup(f.ctx, integration.CleanupRequest{Target: target, Mode: integration.CleanupConfirmed})
				}
				result <- err
				close(done)
			}()
			t.Cleanup(func() { unlock(); unblock(); <-done })
			select {
			case err := <-result:
				t.Fatal("pending cleanup bypassed creator transaction", err)
			case <-time.After(100 * time.Millisecond):
			}
			f.group.Members = []workspacegroup.Member{member}
			f.save(t)
			unlock()
			select {
			case <-entered:
			case err := <-result:
				t.Fatal("newly published member did not reach scan", err)
			case <-time.After(5 * time.Second):
				t.Fatal("actual member scan not reached")
			}
			if err := workspacegroup.WithNoteLock(f.root, func() error { return workspacegroup.RemoveMember(f.root, f.path) }); err != nil {
				t.Fatal(err)
			}
			registry, head := f.snapshot(t)
			unblock()
			if err := <-result; err == nil || !strings.Contains(err.Error(), "registration changed") {
				t.Fatal("stale pending member fell back to removal", err)
			}
			f.assertPreserved(t, registry, head)
			assertNoMemberDeletion(t, calls)
			assertGitOwnerPathsReleased(t, f)
		})
	}
}
