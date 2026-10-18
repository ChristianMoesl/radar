package tmux

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"radar/internal/protocol"
)

func TestReadActivityMatchesLiveTaskSessionsAndWorkspaceMembers(t *testing.T) {
	home := t.TempDir()
	binary := filepath.Join(home, "tmux")
	t.Setenv("PATH", home)
	sessions := "101\t1700000001\t$1\tfirst\t1\t1\t/work/task/member\n101\t1700000002\t$2\tsecond\t0\t1\t/work/elsewhere\n"
	write := func(panes string) {
		t.Helper()
		script := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\nlist-sessions) printf '%%s' '%s';;\nlist-panes) printf '%%s' '%s';;\nesac\n", sessions, panes)
		if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	workspace := protocol.Task{SourceRefs: []protocol.SourceRef{{Role: protocol.SourceRefRoleAuthoritative, ProvidesWorkspace: true, Path: "/work/task"}}}
	write("$1\t0\tbusy\n$1\t0\twaiting\n$2\t0\tbusy\n")
	if activity, err := (Source{}).ReadActivity(context.Background(), workspace); err != nil || activity != protocol.ActivityWaiting {
		t.Fatalf("workspace activity=%s %v", activity, err)
	}
	write("$1\t1\twaiting\n$2\t0\tbusy\n")
	if activity, err := (Source{}).ReadActivity(context.Background(), workspace); err != nil || activity != protocol.ActivityIdle {
		t.Fatalf("dead/unrelated pane activity=%s %v", activity, err)
	}
	task := protocol.Task{SourceRefs: []protocol.SourceRef{{ID: "tmux:session:101:1700000002:$2", Source: "tmux", Kind: "session", Role: protocol.SourceRefRoleAuthoritative}}}
	if activity, err := (Source{}).ReadActivity(context.Background(), task); err != nil || activity != protocol.ActivityBusy {
		t.Fatalf("exact session activity=%s %v", activity, err)
	}
	task.SourceRefs[0].ID = "tmux:session:100:1700000002:$2"
	if activity, err := (Source{}).ReadActivity(context.Background(), task); err != nil || activity != protocol.ActivityIdle {
		t.Fatalf("reused session activity=%s %v", activity, err)
	}
	workspace.SourceRefs[0].Path = "/work/tas"
	if activity, err := (Source{}).ReadActivity(context.Background(), workspace); err != nil || activity != protocol.ActivityIdle {
		t.Fatalf("sibling path activity=%s %v", activity, err)
	}
}

func TestReadActivityFailsClosedExceptForAbsentServer(t *testing.T) {
	home := t.TempDir()
	binary := filepath.Join(home, "tmux")
	t.Setenv("PATH", home)
	task := protocol.Task{SourceRefs: []protocol.SourceRef{{Role: protocol.SourceRefRoleAuthoritative, ProvidesWorkspace: true, Path: "/work/task"}}}
	for _, tc := range []struct {
		message   string
		wantError bool
	}{
		{"no server running on /tmp/tmux-100/default", false},
		{"error connecting to /tmp/tmux-100/default (No such file or directory)", false},
		{"error connecting to /tmp/tmux-100/default (Permission denied)", true},
		{"unexpected runtime failure", true},
	} {
		script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' %q >&2\nexit 1\n", tc.message)
		if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := (Source{}).ReadActivity(context.Background(), task)
		if (err != nil) != tc.wantError {
			t.Fatalf("%s: %v", tc.message, err)
		}
	}
	// A task without a relevant resource never needs a tmux command.
	if _, err := (Source{}).ReadActivity(context.Background(), protocol.Task{}); err != nil {
		t.Fatal(err)
	}
}
