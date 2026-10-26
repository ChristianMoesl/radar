package workspace

import (
	"context"
	"testing"
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
			assertCalled(t, runner.calls, "tmux", "has-session -t "+created.SessionName)
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
