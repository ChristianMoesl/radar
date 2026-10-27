package workspace

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"radar/internal/integration/workspace/group"
)

func TestOpenRegisteredWorkspaceRecoversMissingSessionAndReusesLiveSession(t *testing.T) {
	for _, tc := range []struct {
		name       string
		hasSession bool
		switchTo   bool
	}{
		{name: "missing session inside tmux", switchTo: true},
		{name: "missing session outside tmux"},
		{name: "live session inside tmux", hasSession: true, switchTo: true},
		{name: "live session outside tmux", hasSession: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			root := configureWorkspaceRoot(t)
			created, err := Create(ctx, &fakeRunner{}, CreateOptions{
				Name: "Plan", WorkspaceRoot: root, NoteAuthor: testNoteAuthor(t),
			})
			if err != nil {
				t.Fatal(err)
			}

			runner := &fakeRunner{hasSession: tc.hasSession}
			reopened, err := OpenRegisteredWorkspace(ctx, runner, created.Path, tc.switchTo)
			if err != nil {
				t.Fatal(err)
			}
			if reopened.Path != created.Path || reopened.SessionName != created.SessionName {
				t.Fatalf("workspace identity changed: created=%+v reopened=%+v", created, reopened)
			}
			assertCalled(t, runner.calls, "tmux", "has-session -t ="+created.SessionName)
			if tc.hasSession {
				assertNotCalledContains(t, runner.calls, "tmux", "new-session")
			} else {
				assertCalled(t, runner.calls, "tmux", "new-session -d -s "+created.SessionName)
				if tc.switchTo {
					assertCallOrder(t, runner.calls,
						call{name: "tmux", args: []string{"new-session"}},
						call{name: "tmux", args: []string{"switch-client"}})
				}
			}
			if tc.switchTo {
				assertCalled(t, runner.calls, "tmux", "switch-client -t "+created.SessionName)
			} else {
				assertNotCalledContains(t, runner.calls, "tmux", "switch-client")
			}
			assertNotCalledContains(t, runner.calls, "tmux", "kill-session")
		})
	}
}

func TestOpenLiveWorkspaceSkipsRuntimeRecovery(t *testing.T) {
	for _, switchTo := range []bool{false, true} {
		t.Run(map[bool]string{false: "attach", true: "switch"}[switchTo], func(t *testing.T) {
			ctx := context.Background()
			root := configureWorkspaceRoot(t)
			created, err := Create(ctx, &fakeRunner{}, CreateOptions{
				Name: "Plan", WorkspaceRoot: root, NoteAuthor: testNoteAuthor(t),
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := workspacegroup.Update(root, func(registry *workspacegroup.Registry) error {
				registry.Workspaces[0].Sandbox = &workspacegroup.Sandbox{Name: "plan", Agent: "shell", Mounts: []string{created.Path}, ReadyCommand: []string{"ready"}}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			// A live switch must not acquire the recovery/creation note lock.
			lock := filepath.Join(root, ".radar-notes.lock")
			if err := os.Remove(lock); err != nil {
				t.Fatal(err)
			}
			runner := &readyRunner{Runner: &fakeRunner{hasSession: true}}
			reopened, err := OpenRegisteredWorkspace(ctx, runner, created.Path, switchTo)
			if err != nil || reopened.SessionName != created.SessionName || reopened.SandboxName != "plan" {
				t.Fatalf("open = %+v, %v", reopened, err)
			}
			want := []call{{name: "tmux", args: []string{"has-session", "-t", "=" + created.SessionName}}}
			if switchTo {
				want = append(want, call{name: "tmux", args: []string{"switch-client", "-t", created.SessionName}})
			}
			if !reflect.DeepEqual(runner.calls, want) || len(runner.lookups) != 0 {
				t.Fatalf("live open ran recovery checks: calls=%+v lookups=%v; want %+v", runner.calls, runner.lookups, want)
			}
			if _, err := os.Stat(lock); !os.IsNotExist(err) {
				t.Fatalf("live open acquired the note lock: %v", err)
			}
		})
	}
}
