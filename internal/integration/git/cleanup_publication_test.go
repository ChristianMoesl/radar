package git

import (
	"context"
	"encoding/json"
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
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

func pausedPublicationCleanup(t *testing.T, f *cleanupExpiryFixture, target protocol.CleanupTarget, service bool, later ...integration.CleanupProvider) (func(), <-chan error, string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	gate := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> %q\nif [ \"$*\" = 'fetch --prune origin' ]; then\n : > %q\n while [ ! -e %q ]; do sleep 0.01; done\nfi\n%q \"$@\"\nresult=$?\nprintf 'done %%s\\n' \"$*\" >> %q\nexit $result\n", filepath.Join(gate, "calls"), filepath.Join(gate, "entered"), filepath.Join(gate, "resume"), realGit, filepath.Join(gate, "completed"))
	if err := os.WriteFile(filepath.Join(gate, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", gate+string(os.PathListSeparator)+os.Getenv("PATH"))
	var once sync.Once
	unblock := func() {
		once.Do(func() {
			if err := os.WriteFile(filepath.Join(gate, "resume"), nil, 0600); err != nil {
				t.Error(err)
			}
		})
	}
	result, done := make(chan error, 1), make(chan struct{})
	go func() {
		var err error
		if service {
			providers := append([]integration.CleanupProvider{Source{}}, later...)
			targets := []protocol.CleanupTarget{target}
			for _, provider := range later {
				targets = append(targets, protocol.CleanupTarget{Source: provider.Descriptor().Name})
			}
			_, err = cleanup.New(providers).Execute(f.ctx, protocol.CleanupPreview{Targets: targets}, cleanup.ExecuteOptions{})
		} else {
			_, err = (Source{}).Cleanup(f.ctx, integration.CleanupRequest{Target: target})
		}
		result <- err
		close(done)
	}()
	t.Cleanup(func() { unblock(); <-done })
	deadline := time.After(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(gate, "entered")); err == nil {
			break
		}
		select {
		case err := <-result:
			t.Fatal("publication fetch not reached", err)
		case <-deadline:
			t.Fatal("publication fetch not reached")
		case <-time.After(10 * time.Millisecond):
		}
	}
	return unblock, result, gate
}

func TestGitPublicationRevalidatesBeforeAnyDeletion(t *testing.T) {
	for _, service := range []bool{false, true} {
		for _, kind := range []string{"success", "unrelated-update", "owner", "member", "canonical-note", "sandbox-config", "removed-member", "removed-owner", "configured-root", "root-replaced", "anchor-replaced", "member-replaced", "repository-replaced", "dirty", "branch-moved", "locked", "actual-branch-changed", "git-file-changed", "agent-appeared", "cancelled", "fetch-failure", "unpublished"} {
			t.Run(fmt.Sprintf("service=%t/%s", service, kind), func(t *testing.T) {
				f := newCleanupExpiryFixture(t, "feature")
				t.Setenv("RADAR_SOCKET", filepath.Join(f.home, "run", "radar.sock"))
				t.Setenv("RADAR_AGENT_ROOT", filepath.Join(f.home, "agents"))
				target := f.preview(t, integration.CleanupSafe)
				ctx, cancel := context.WithCancel(f.ctx)
				defer cancel()
				f.ctx = ctx
				unblock, result, gate := pausedPublicationCleanup(t, f, target, service)
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
				case "unrelated-update":
					changeRegistry(func(r *workspacegroup.Registry) {
						path := filepath.Join(f.root, "other")
						r.Workspaces = append(r.Workspaces, workspacegroup.Workspace{ID: workspacegroup.ID(path), Name: "other", Path: path})
					})
				case "configured-root":
					config := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "radar", "config.yaml")
					if err := os.WriteFile(config, fmt.Appendf(nil, "workspace: {root_dir: %q}\n", filepath.Join(f.home, "other-root")), 0600); err != nil {
						t.Fatal(err)
					}
				case "root-replaced", "anchor-replaced", "member-replaced", "repository-replaced":
					path := f.root
					if kind == "anchor-replaced" {
						path = f.group.Path
					}
					if kind == "member-replaced" {
						path = f.path
					}
					if kind == "repository-replaced" {
						path = f.repo
					}
					moved := path + "-original"
					if err := os.Rename(path, moved); err != nil {
						t.Fatal(err)
					}
					if err := os.MkdirAll(path, 0755); err != nil {
						t.Fatal(err)
					}
					if kind == "root-replaced" || kind == "anchor-replaced" {
						if err := os.MkdirAll(f.path, 0755); err != nil {
							t.Fatal(err)
						}
					}
					// Restore the original metadata so only physical identity changed.
					if kind == "root-replaced" {
						f.save(t)
					}
				case "dirty":
					f.dirty(t)
				case "branch-moved":
					f.commitLocal(t)
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
				case "fetch-failure":
					runGit(t, f.ctx, f.repo, "remote", "set-url", "origin", filepath.Join(f.home, "missing-origin.git"))
				case "unpublished":
					// Leave the ref unchanged but remove its only published reachability.
					runGit(t, f.ctx, filepath.Join(f.home, "origin.git"), "update-ref", "-d", "refs/heads/main")
					gh := filepath.Join(gate, "gh")
					if err := os.WriteFile(gh, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
						t.Fatal(err)
					}
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
				calls, readErr := os.ReadFile(filepath.Join(gate, "calls"))
				if readErr != nil {
					t.Fatal(readErr)
				}
				wantFetches := 1
				if kind == "fetch-failure" {
					wantFetches = 2 // Keep the existing bounded verification retry.
				}
				if strings.Count(string(calls), "fetch --prune origin\n") != wantFetches {
					t.Fatal("verification was repeated or skipped", string(calls))
				}
				if success {
					f.assertRemoved(t)
				} else {
					if _, err := os.Stat(f.path); err != nil {
						t.Fatal("refused cleanup removed member", err)
					}
					after, err := os.ReadFile(workspacegroup.Path(f.root))
					if err != nil || string(after) != string(registry) {
						t.Fatal("refused cleanup changed registration", err)
					}
					if strings.Contains(string(calls), "worktree remove") || strings.Contains(string(calls), "update-ref -d refs/heads/feature") {
						t.Fatal("refused cleanup attempted deletion", string(calls))
					}
				}
				assertGitOwnerPathsReleased(t, f)
			})
		}
	}
}

func TestGitPublicationRetainsActualOwnerAdmission(t *testing.T) {
	for _, service := range []bool{false, true} {
		for _, kind := range []string{"anchor", "member", "alias", "reference"} {
			t.Run(fmt.Sprintf("service=%t/%s", service, kind), func(t *testing.T) {
				f := newCleanupExpiryFixture(t, "feature")
				target := f.preview(t, integration.CleanupSafe)
				unblock, result, _ := pausedPublicationCleanup(t, f, target, service)
				path := f.group.Path
				if kind == "member" {
					path = f.path
				}
				if kind == "alias" {
					path = filepath.Join(f.home, "alias")
					if err := os.Symlink(f.group.Path, path); err != nil {
						t.Fatal(err)
					}
				}
				var release func()
				var err error
				if kind == "reference" {
					release, err = workspacelease.AcquireReferences(context.Background(), filepath.Join(f.path, "new-note.md"))
				} else {
					release, err = workspacelease.Acquire(path, false)
				}
				if err == nil {
					release()
					t.Fatal("actual owner admission dropped during proof", kind)
				}
				unblock()
				if err := <-result; err != nil {
					t.Fatal(err)
				}
				assertGitOwnerPathsReleased(t, f)
			})
		}
	}
}

func TestGitPublicationProofIsRecheckedAfterWaitingForNoteTransaction(t *testing.T) {
	for _, service := range []bool{false, true} {
		t.Run(fmt.Sprintf("service=%t", service), func(t *testing.T) {
			f := newCleanupExpiryFixture(t, "feature")
			target := f.preview(t, integration.CleanupSafe)
			unblock, result, gate := pausedPublicationCleanup(t, f, target, service)
			err := workspacegroup.WithNoteLock(f.root, func() error {
				unblock()
				deadline := time.Now().Add(5 * time.Second)
				for {
					data, err := os.ReadFile(filepath.Join(gate, "completed"))
					if err != nil {
						return err
					}
					if strings.Count(string(data), "done rev-parse --verify refs/heads/feature\n") >= 3 {
						break
					}
					if time.Now().After(deadline) {
						return fmt.Errorf("publication did not finish outside held note transaction")
					}
					time.Sleep(10 * time.Millisecond)
				}
				f.commitLocal(t)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			registry, head := f.snapshot(t)
			if err := <-result; err == nil || !strings.Contains(err.Error(), "branch changed") {
				t.Fatal("stale proof survived final note-transaction wait", err)
			}
			f.assertPreserved(t, registry, head)
			assertGitOwnerPathsReleased(t, f)
		})
	}
}

func TestGitPublicationDiscoveredOwnerRetainedThroughLaterProviders(t *testing.T) {
	for _, outcome := range []string{"success", "provider-error", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			f := newCleanupExpiryFixture(t, "feature")
			target := f.preview(t, integration.CleanupSafe)
			ctx, cancel := context.WithCancel(f.ctx)
			defer cancel()
			f.ctx = ctx
			calls := 0
			probe := ownerAdmissionProbe{run: func() error {
				calls++
				for _, path := range []string{f.group.Path, f.path} {
					if release, err := workspacelease.Acquire(path, false); err == nil {
						release()
						t.Error("discovered Git ownership dropped before later provider")
					}
				}
				if outcome == "provider-error" {
					return fmt.Errorf("fixture later provider failure")
				}
				if outcome == "cancelled" {
					cancel()
					return ctx.Err()
				}
				return nil
			}}
			unblock, result, _ := pausedPublicationCleanup(t, f, target, true, probe)
			unblock()
			err := <-result
			if calls != 1 || (err == nil) != (outcome == "success") {
				t.Fatal("incorrect later provider outcome", calls, err)
			}
			f.ctx = context.Background() // Inspect completed effects after cancellation.
			f.assertRemoved(t)
			assertGitOwnerPathsReleased(t, f)
		})
	}
}
