package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/integration/workspace"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

type memberScanSource struct {
	Source
	scan func(func() error) error
}

func (s memberScanSource) Cleanup(ctx context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	return s.Source.cleanupWithMemberScan(ctx, req, s.scan)
}

type memberRecoveryRunner struct {
	calls []string
	live  bool
}

func (*memberRecoveryRunner) LookPath(string) error { return nil }
func (r *memberRecoveryRunner) Run(_ context.Context, _ string, name string, args ...string) (string, error) {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	if name == "tmux" && len(args) > 0 {
		switch args[0] {
		case "has-session":
			if !r.live {
				return "", errors.New("missing")
			}
		case "new-session":
			r.live = true
			return "12:34:$9 @1 %1", nil
		case "display-message":
			if args[len(args)-1] == "#{session_name}" {
				return "unrelated-session\n", nil
			}
			if r.live {
				return "12:34:$9\n", nil
			}
			return "12::\n", nil
		case "new-window", "split-window":
			return "@1 %1", nil
		}
	}
	return "", nil
}

func addMemberRecoveryWorkspace(t *testing.T, f *cleanupExpiryFixture) workspacegroup.Workspace {
	t.Helper()
	path, note := filepath.Join(f.root, "unrelated"), filepath.Join(f.home, "unrelated.md")
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(note, []byte("canonical unrelated\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(note, filepath.Join(path, "notes.md")); err != nil {
		t.Fatal(err)
	}
	g := workspacegroup.Workspace{ID: workspacegroup.ID(path), Name: "unrelated", Path: path, SessionName: "unrelated-session", TaskLinkingKey: "obsidian:task:unrelated", NotePath: note, Members: []workspacegroup.Member{}}
	if err := workspacegroup.Update(f.root, func(r *workspacegroup.Registry) error { r.Workspaces = append(r.Workspaces, g); return nil }); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestGitMemberScanAllowsUnrelatedRecovery(t *testing.T) {
	for _, service := range []bool{false, true} {
		for _, entry := range []string{"open", "create-existing"} {
			t.Run(fmt.Sprintf("service=%t/%s", service, entry), func(t *testing.T) {
				f := newCleanupExpiryFixture(t, "feature")
				t.Setenv("RADAR_AGENT_ROOT", filepath.Join(f.home, "agents"))
				t.Setenv("RADAR_SOCKET", filepath.Join(f.home, "run", "radar.sock"))
				target := f.preview(t, integration.CleanupConfirmed)
				other := addMemberRecoveryWorkspace(t, f)
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
				t.Cleanup(func() { unblock(); <-done })
				select {
				case <-entered:
				case err := <-result:
					t.Fatal("actual member scan not reached", err)
				case <-time.After(5 * time.Second):
					t.Fatal("actual member scan not reached")
				}
				runner := &memberRecoveryRunner{}
				recovered, recoveryDone := make(chan error, 1), make(chan struct{})
				go func() {
					var err error
					if entry == "open" {
						_, err = workspace.OpenRegisteredWorkspace(f.ctx, runner, other.Path, true)
					} else {
						_, err = workspace.Create(f.ctx, runner, workspace.CreateOptions{WorkspaceRoot: f.root, TaskLinkingKey: other.NoteKey(), Switch: true})
					}
					recovered <- err
					close(recoveryDone)
				}()
				t.Cleanup(func() { unblock(); <-recoveryDone })
				select {
				case err := <-recovered:
					if err != nil {
						t.Fatal("recovery fixture failed", err)
					}
				case <-time.After(3 * time.Second):
					unblock()
					<-recoveryDone
					if err := <-recovered; err != nil {
						t.Fatal("recovery fixture failed after release", err)
					}
					t.Fatal("unrelated recovery stalled in actual managed member scan callback")
				}
				calls := strings.Join(runner.calls, "\n")
				if !strings.Contains(calls, "tmux new-session") || !strings.Contains(calls, "tmux switch-client") {
					t.Fatal("missing recovery effects", calls)
				}
				unblock()
				if err := <-result; err != nil {
					t.Fatal(err)
				}
				f.assertRemoved(t)
				assertGitOwnerPathsReleased(t, f)
				if data, err := os.ReadFile(other.NotePath); err != nil || string(data) != "canonical unrelated\n" {
					t.Fatal("canonical note changed", err)
				}
			})
		}
	}
}
