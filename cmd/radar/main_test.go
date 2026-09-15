package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"radar/internal/app"
	"radar/internal/integration/workspace"
	"radar/internal/notification"
	"radar/internal/protocol"
	"radar/internal/workspacegc"
)

func TestFatalMessageSerializesWorkspaceReconciliationProblem(t *testing.T) {
	message := fatalMessage(&workspace.ReconcileWorkspaceError{
		Reason: "dirty_removal", Message: "cannot remove dirty member", Path: "/work/member", ChangeCount: 2,
	})
	var problem workspace.ReconcileWorkspaceError
	if err := json.Unmarshal([]byte(message), &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Reason != "dirty_removal" || problem.Path != "/work/member" || problem.ChangeCount != 2 {
		t.Fatalf("problem = %+v", problem)
	}
}

func TestRefreshLocalSourcesAfterReconcileRequestsLocalRefresh(t *testing.T) {
	temporary, err := os.CreateTemp("/tmp", "radar-test-*.sock")
	if err != nil {
		t.Fatal(err)
	}
	socketPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(socketPath); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RADAR_SOCKET", socketPath)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	defer os.Remove(socketPath)

	method := make(chan string, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		var request protocol.Request
		if decodeErr := json.NewDecoder(connection).Decode(&request); decodeErr != nil {
			return
		}
		method <- request.Method
		_ = json.NewEncoder(connection).Encode(protocol.Response{OK: true})
	}()

	if err := refreshLocalSourcesAfterReconcile(); err != nil {
		t.Fatal(err)
	}
	if got := <-method; got != "refresh-local" {
		t.Fatalf("method = %q, want refresh-local", got)
	}
}

func TestCleanupTargetDescriptionUsesProviderDescription(t *testing.T) {
	path := filepath.Join("workspaces", "radar", "small-fix")
	want := "worktree ~/workspaces/radar/small-fix (dirty)"
	target := protocol.CleanupTarget{Kind: "worktree", Path: path, Description: want}

	got := cleanupTargetDescription(target)
	if got != want {
		t.Fatalf("cleanupTargetDescription() = %q, want %q", got, want)
	}
	if target.Path != path {
		t.Fatalf("cleanupTargetDescription() changed target path to %q", target.Path)
	}
}

func TestGarbageCollectionResultConvertsDeletedAndSkippedWorkspaces(t *testing.T) {
	result := garbageCollectionResult(workspacegc.Result{
		Deleted: []workspacegc.Candidate{{TaskID: 7, Path: "/workspaces/deleted"}},
		Skipped: []workspacegc.Skipped{{TaskID: 8, Path: "/workspaces/skipped", Reason: "workspace has local changes"}},
	})
	if len(result.Deleted) != 1 || result.Deleted[0].TaskID != 7 || result.Deleted[0].Path != "/workspaces/deleted" {
		t.Fatalf("deleted = %+v", result.Deleted)
	}
	if len(result.Skipped) != 1 || result.Skipped[0].TaskID != 8 || result.Skipped[0].Reason != "workspace has local changes" {
		t.Fatalf("skipped = %+v", result.Skipped)
	}
}

type recordingNotificationSender struct {
	titles []string
}

func (s *recordingNotificationSender) Send(_ context.Context, message notification.Notification) error {
	s.titles = append(s.titles, message.Title)
	return nil
}

func TestNotifyActionableTransitionsAppliesConfiguredFilters(t *testing.T) {
	configHome := filepath.Join(t.TempDir(), "config")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	path := filepath.Join(configHome, "radar", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{
  "linking_mark_prefixes": ["XYZ"],
  "github": {
    "filters": {
      "mute_repos": ["org/muted"],
      "deprioritize_repos": ["org/deprioritized"]
    }
  }
}`), 0o600); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sender := &recordingNotificationSender{}
	service := notification.NewWithSender(logger, sender)
	notifyActionableTransitions(context.Background(), nil, []protocol.Task{
		{ID: 1, Title: "Muted", Repo: "org/muted", Attention: "attention"},
		{ID: 2, Title: "Deprioritized", Repo: "org/deprioritized", Attention: "attention"},
		{ID: 3, Title: "Useful", Repo: "org/useful", Attention: "attention"},
	}, logger, app.DefaultIntegrations(), service)

	if len(sender.titles) != 1 || sender.titles[0] != "Radar: Useful" {
		t.Fatalf("notification titles = %#v, want only useful task", sender.titles)
	}
}

func TestActivityPublishesAndRequestsOnlyLocalRefresh(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "radar-activity-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "s")
	output := filepath.Join(dir, "args")
	t.Setenv("RADAR_SOCKET", path)
	t.Setenv("TMUX_PANE", "%12")
	t.Setenv("ACTIVITY_TEST_OUTPUT", output)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ACTIVITY_TEST_OUTPUT\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	requests := make(chan protocol.Request, 3)
	go func() {
		for range 3 {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			var req protocol.Request
			if json.NewDecoder(conn).Decode(&req) == nil {
				requests <- req
			}
			_ = json.NewEncoder(conn).Encode(protocol.Response{OK: true})
			_ = conn.Close()
		}
	}()
	for _, activity := range []string{"busy", "waiting", "idle"} {
		runActivity([]string{activity})
		select {
		case request := <-requests:
			if request.Method != "refresh-local" {
				t.Fatalf("unexpected request: %+v", request)
			}
		case <-time.After(time.Second):
			t.Fatal("activity did not request refresh")
		}
		data, err := os.ReadFile(output)
		if err != nil || !strings.Contains(string(data), "@radar_activity") {
			t.Fatalf("pane publication = %q, %v", data, err)
		}
	}
	// An absent daemon must not make a successful pane publication fail.
	_ = listener.Close()
	runActivity([]string{"waiting"})
}
