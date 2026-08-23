package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"

	"radar/internal/notification"
	"radar/internal/protocol"
	"radar/internal/workspace"
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

func TestCleanupTargetDescriptionShortensHomePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, "workspaces", "radar", "small-fix")
	target := protocol.CleanupTarget{Kind: "worktree", Path: path, Branch: "small-fix", Dirty: true, DeleteBranch: true, Unpublished: true}

	got := cleanupTargetDescription(target)
	want := "worktree " + filepath.Join("~", "workspaces", "radar", "small-fix") + " (dirty; uncommitted changes will be discarded; deletes local branch small-fix; branch commits were not found remotely)"
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

func TestShouldEnsureSBXLogin(t *testing.T) {
	for _, test := range []struct {
		name    string
		mode    string
		sources []protocol.SourceStatus
		want    bool
	}{
		{name: "normal startup", want: false},
		{name: "create", mode: "create", want: true},
		{name: "fork", mode: "fork", want: true},
		{name: "expired session", sources: []protocol.SourceStatus{{Name: "sbx", Status: "error", Detail: "not signed in; run sbx login"}}, want: true},
		{name: "unrelated sbx failure", sources: []protocol.SourceStatus{{Name: "sbx", Status: "error", Detail: "sbx daemon is unavailable"}}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldEnsureSBXLogin(test.mode, test.sources); got != test.want {
				t.Errorf("shouldEnsureSBXLogin(%q, %+v) = %t, want %t", test.mode, test.sources, got, test.want)
			}
		})
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
  "linking_mark_prefixes": ["RAD"],
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
	}, logger, service)

	if len(sender.titles) != 1 || sender.titles[0] != "Radar: Useful" {
		t.Fatalf("notification titles = %#v, want only useful task", sender.titles)
	}
}
