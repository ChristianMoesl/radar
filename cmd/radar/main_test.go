package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"radar/internal/notification"
	"radar/internal/protocol"
	"radar/internal/workspacegc"
)

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

func (s *recordingNotificationSender) Send(_ context.Context, title, _ string) error {
	s.titles = append(s.titles, title)
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
  "filters": {
    "mute_repos": ["org/muted"],
    "deprioritize_repos": ["org/deprioritized"]
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
