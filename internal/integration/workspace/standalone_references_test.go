package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

func TestStandaloneSandboxReferencesRejectActualCleanupBeforeMountEffects(t *testing.T) {
	for _, kind := range []string{"additional-mount", "readonly-mount", "git-common-dir"} {
		for _, alias := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "/physical", true: "/alias"}[alias], func(t *testing.T) {
				withWorkspaceGOOS(t, "darwin")
				base := t.TempDir()
				t.Setenv("RADAR_SOCKET", filepath.Join(base, "radar.sock"))
				target, path := filepath.Join(base, "cleanup"), filepath.Join(base, "worktree")
				for _, p := range []string{target, path} {
					if err := os.MkdirAll(p, 0700); err != nil {
						t.Fatal(err)
					}
				}
				referenceRoot := target
				if alias {
					referenceRoot = filepath.Join(base, "alias")
					if err := os.Symlink(target, referenceRoot); err != nil {
						t.Fatal(err)
					}
				}
				reference := filepath.Join(referenceRoot, "new-mount")
				entered, resume := make(chan struct{}), make(chan struct{})
				done := make(chan error, 1)
				service := cleanup.New([]integration.CleanupProvider{activationCleanup{started: entered, resume: resume}})
				go func() {
					defer close(done)
					_, err := service.Execute(context.Background(), protocol.CleanupPreview{Targets: []protocol.CleanupTarget{{Source: "fixture", Kind: "workspace", Path: target}}}, cleanup.ExecuteOptions{Mode: integration.CleanupConfirmed})
					done <- err
				}()
				defer func() {
					close(resume)
					if err := <-done; err != nil {
						t.Error(err)
					}
				}()
				select {
				case <-entered:
				case err := <-done:
					t.Fatal("cleanup failed admission", err)
				case <-time.After(5 * time.Second):
					t.Fatal("cleanup did not start")
				}
				runner := &fakeRunner{}
				options := CreateSessionOptions{Path: path, SessionName: "other", Sandbox: true}
				if kind != "git-common-dir" {
					mount := reference
					if kind == "readonly-mount" {
						mount += ":ro"
					}
					options.AdditionalSandboxMounts = []string{mount}
				} else {
					runner.gitCommonDir = filepath.Join(reference, ".git")
				}
				_, err := CreateSessionWithOptions(context.Background(), runner, options)
				if err == nil || !strings.Contains(err.Error(), "in use") {
					t.Error("missing cleanup ancestor rejection", err)
				}
				for _, suffix := range []string{"", ":ro"} {
					if _, err := os.Lstat(filepath.Join(target, "new-mount") + suffix); !os.IsNotExist(err) {
						t.Error("conflicting mount preparation created a directory", err)
					}
				}
				assertNotCalledContains(t, runner.calls, "sbx", "create")
				assertNotCalledContains(t, runner.calls, "tmux", "new-session")
				assertSessionLeaseReleased(t, path)
			})
		}
	}
}

func TestStandaloneSandboxReferencesRetainedThroughRuntimeAndReleasedEveryOutcome(t *testing.T) {
	for _, outcome := range []string{"success", "create-error", "switch-error", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			withWorkspaceGOOS(t, "darwin")
			base := t.TempDir()
			t.Setenv("RADAR_SOCKET", filepath.Join(base, "radar.sock"))
			path := filepath.Join(base, "worktree")
			mount := filepath.Join(base, "mount", "new")
			common := filepath.Join(base, "repository", ".git")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fake := &fakeRunner{gitCommonDir: common}
			created, switched := false, false
			runner := activationRunner{Runner: fake, run: func(ctx context.Context, cwd, name string, args ...string) (string, error) {
				if (name == "sbx" && args[0] == "create") || (name == "tmux" && args[0] == "switch-client") {
					for _, reference := range []string{path, mount, filepath.Dir(mount), common, filepath.Dir(common)} {
						if release, err := workspacelease.Acquire(reference, true); err == nil {
							release()
							t.Fatal("runtime lost reference/ancestor deletion admission", reference)
						}
					}
					if release, err := workspacelease.Acquire(filepath.Join(base, "unrelated"), true); err != nil {
						t.Fatal("shared ancestors blocked unrelated cleanup", err)
					} else {
						release()
					}
					if name == "sbx" {
						created = true
						if info, err := os.Stat(mount); err != nil || !info.IsDir() {
							t.Fatal("admitted mount was not prepared", err)
						}
						if _, err := os.Lstat(mount + ":ro"); !os.IsNotExist(err) {
							t.Fatal("read-only suffix became a directory name", err)
						}
						if outcome == "create-error" {
							return "", errors.New("fixture sandbox creation failed")
						}
					} else {
						switched = true
						if outcome == "switch-error" {
							return "", errors.New("fixture switch failed")
						}
						if outcome == "cancelled" {
							cancel()
							return "", ctx.Err()
						}
					}
				}
				return fake.Run(ctx, cwd, name, args...)
			}}
			_, err := CreateSessionWithOptions(ctx, runner, CreateSessionOptions{Path: path, SessionName: "other", Sandbox: true, AdditionalSandboxMounts: []string{mount + ":ro"}, Switch: true})
			if !created || (switched != (outcome != "create-error")) || ((err == nil) != (outcome == "success")) {
				t.Fatalf("created=%t switched=%t outcome=%s err=%v", created, switched, outcome, err)
			}
			if outcome == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatal("lost cancellation cause", err)
			}
			for _, reference := range []string{base, path, mount, filepath.Dir(mount), common, filepath.Dir(common)} {
				assertSessionLeaseReleased(t, reference)
			}
		})
	}
}

func TestStandaloneSandboxReferencesFailedPlanningHasNoMountEffects(t *testing.T) {
	for _, failure := range []string{"invalid-mount", "cancelled-plan"} {
		t.Run(failure, func(t *testing.T) {
			withWorkspaceGOOS(t, "darwin")
			base := t.TempDir()
			t.Setenv("RADAR_SOCKET", filepath.Join(base, "radar.sock"))
			mount, path := filepath.Join(base, "mount", "new"), filepath.Join(base, "worktree")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fake := &fakeRunner{}
			runner := activationRunner{Runner: fake, run: func(ctx context.Context, cwd, name string, args ...string) (string, error) {
				if failure == "cancelled-plan" && name == "git" && args[0] == "rev-parse" {
					cancel()
				}
				return fake.Run(ctx, cwd, name, args...)
			}}
			mounts := []string{mount}
			if failure == "invalid-mount" {
				mounts = append(mounts, "relative")
			}
			_, err := CreateSessionWithOptions(ctx, runner, CreateSessionOptions{Path: path, SessionName: "other", Sandbox: true, AdditionalSandboxMounts: mounts})
			if err == nil || (failure == "cancelled-plan" && !errors.Is(err, context.Canceled)) {
				t.Fatal("failed planning did not reject creation", err)
			}
			if _, err := os.Lstat(mount); !os.IsNotExist(err) {
				t.Fatal("failed planning prepared an earlier mount", err)
			}
			assertNotCalledContains(t, fake.calls, "sbx", "create")
			assertNotCalledContains(t, fake.calls, "tmux", "new-session")
			assertSessionLeaseReleased(t, base)
		})
	}
}
