package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

func sessionLeasePaths(t *testing.T, missing bool) (string, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("RADAR_SOCKET", filepath.Join(root, "runtime", "radar.sock"))
	t.Setenv("RADAR_AGENT_ROOT", filepath.Join(root, "agents"))
	physical, alias := filepath.Join(root, "physical"), filepath.Join(root, "alias")
	if err := os.Mkdir(physical, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(physical, alias); err != nil {
		t.Fatal(err)
	}
	if missing {
		physical, alias = filepath.Join(physical, "missing"), filepath.Join(alias, "missing")
	}
	return physical, alias
}

func assertSessionLeaseReleased(t *testing.T, path string) {
	t.Helper()
	release, err := workspacelease.Acquire(path, true)
	if err != nil {
		t.Fatal("standalone session leaked its path lease", err)
	}
	release()
}

func TestCreateSessionRejectsCleanupLeaseBeforeRuntime(t *testing.T) {
	for _, live := range []bool{false, true} {
		for _, missing := range []bool{false, true} {
			for _, useAlias := range []bool{false, true} {
				name := map[bool]string{false: "recovery", true: "live"}[live] + "/" + map[bool]string{false: "existing", true: "missing"}[missing] + "/" + map[bool]string{false: "physical", true: "alias"}[useAlias]
				t.Run(name, func(t *testing.T) {
					physical, alias := sessionLeasePaths(t, missing)
					release, err := workspacelease.Acquire(physical, true)
					if err != nil {
						t.Fatal(err)
					}
					defer release()
					path := physical
					if useAlias {
						path = alias
					}
					runner := &fakeRunner{hasSession: live}
					_, err = CreateSession(context.Background(), runner, path, "fixture-session", true)
					if err == nil || !strings.Contains(err.Error(), "in use by another operation") || len(runner.calls) != 0 {
						t.Fatalf("standalone activation bypassed cleanup: %v calls=%+v", err, runner.calls)
					}
				})
			}
		}
	}
}

func TestCreateSessionHoldsLeaseThroughRuntimeAndReleasesEveryOutcome(t *testing.T) {
	for _, live := range []bool{false, true} {
		for _, outcome := range []string{"success", "failure", "cancelled"} {
			t.Run(map[bool]string{false: "recovery", true: "live"}[live]+"/"+outcome, func(t *testing.T) {
				physical, alias := sessionLeasePaths(t, false)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				base := &fakeRunner{hasSession: live}
				checked := false
				runner := activationRunner{Runner: base, run: func(ctx context.Context, cwd, name string, args ...string) (string, error) {
					if name == "tmux" && args[0] == "switch-client" {
						checked = true
						for _, path := range []string{physical, alias} {
							if release, err := workspacelease.Acquire(path, true); err == nil {
								release()
								t.Fatal("cleanup acquired a path before standalone activation returned")
							}
						}
						release, err := workspacelease.Acquire(t.TempDir(), true)
						if err != nil {
							t.Fatal("unrelated resource lease was blocked", err)
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
				_, err := CreateSession(ctx, runner, alias, "fixture-session", true)
				if !checked || (err != nil) != (outcome != "success") {
					t.Fatalf("outcome=%s runtime checked=%t err=%v", outcome, checked, err)
				}
				if outcome == "cancelled" && !errors.Is(err, context.Canceled) {
					t.Fatal("lost cancellation cause", err)
				}
				assertSessionLeaseReleased(t, physical)
			})
		}
	}
}

func TestCreateSessionCleanupServiceBlocksMatchingButNotUnrelatedPath(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(map[bool]string{false: "recovery", true: "live"}[live], func(t *testing.T) {
			physical, alias := sessionLeasePaths(t, false)
			started, resume := make(chan struct{}), make(chan struct{})
			finished, done := make(chan error, 1), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(resume) }) }
			service := cleanup.New([]integration.CleanupProvider{activationCleanup{started: started, resume: resume}})
			preview := protocol.CleanupPreview{Targets: []protocol.CleanupTarget{{Source: "fixture", Kind: "worktree", Path: physical}}}
			go func() {
				_, err := service.Execute(context.Background(), preview, cleanup.ExecuteOptions{Mode: integration.CleanupConfirmed})
				finished <- err
				close(done)
			}()
			defer func() { unblock(); <-done }()
			select {
			case <-started:
			case err := <-finished:
				t.Fatalf("cleanup did not reach provider: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("cleanup did not start")
			}
			runner := &fakeRunner{hasSession: live}
			_, err := CreateSession(context.Background(), runner, alias, "matching-session", true)
			if err == nil || !strings.Contains(err.Error(), "in use by another operation") || len(runner.calls) != 0 {
				t.Fatalf("matching standalone activation entered paused cleanup: %v %+v", err, runner.calls)
			}
			unrelated := &fakeRunner{hasSession: live}
			if _, err := CreateSession(context.Background(), unrelated, t.TempDir(), "unrelated-session", true); err != nil {
				t.Fatal("unrelated standalone activation blocked by cleanup", err)
			}
			unblock()
			if err := <-finished; err != nil {
				t.Fatal(err)
			}
			if _, err := CreateSession(context.Background(), runner, alias, "matching-session", true); err != nil {
				t.Fatal("activation did not recover after cleanup returned", err)
			}
			assertSessionLeaseReleased(t, physical)
		})
	}
}

func TestCreateSessionCancellationAndValidationReleasePathLease(t *testing.T) {
	for _, outcome := range []string{"pre-cancelled", "thinking", "repo-config"} {
		t.Run(outcome, func(t *testing.T) {
			physical, alias := sessionLeasePaths(t, false)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			options := CreateSessionOptions{Path: alias, SessionName: "fixture-session"}
			switch outcome {
			case "pre-cancelled":
				cancel()
			case "thinking":
				options.Thinking = "invalid-fixture-thinking"
			case "repo-config":
				if err := os.WriteFile(filepath.Join(physical, ".radar.yaml"), []byte("sbx: ["), 0600); err != nil {
					t.Fatal(err)
				}
			}
			runner := &fakeRunner{}
			_, err := CreateSessionWithOptions(ctx, runner, options)
			if err == nil || len(runner.calls) != 0 {
				t.Fatalf("invalid/cancelled standalone activation ran: %v %+v", err, runner.calls)
			}
			if outcome == "pre-cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatal("lost pre-admission cancellation", err)
			}
			assertSessionLeaseReleased(t, physical)
		})
	}
}

func TestCreateSessionHoldsPathLeaseDuringSandboxPreparation(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	physical, alias := sessionLeasePaths(t, false)
	base := &fakeRunner{gitCommonDir: filepath.Join(physical, ".git")}
	checked := map[string]bool{}
	runner := activationRunner{Runner: base, run: func(ctx context.Context, cwd, name string, args ...string) (string, error) {
		if name == "sbx" && (args[0] == "ls" || args[0] == "create") {
			checked[args[0]] = true
			if release, err := workspacelease.Acquire(physical, true); err == nil {
				release()
				t.Fatal("sandbox preparation ran without a matching path lease")
			}
		}
		return base.Run(ctx, cwd, name, args...)
	}}
	if _, err := CreateSessionWithOptions(context.Background(), runner, CreateSessionOptions{Path: alias, SessionName: "fixture-session", Sandbox: true}); err != nil {
		t.Fatal(err)
	}
	if !checked["ls"] || !checked["create"] {
		t.Fatal("sandbox lookup and recreation boundaries were not exercised")
	}
	assertSessionLeaseReleased(t, physical)
}
